package mcpsampling

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
	"github.com/feiyu912/zenforge/model"
)

// samplingSession owns the client side of one served MCP connection, the way
// a stdio peer sees it. The server runs on its own goroutine so a model call
// can block in Sample while the test reads the sampling request and answers
// it, which is the exchange under test.
type samplingSession struct {
	t           *testing.T
	server      *mcp.Server
	reader      *bufio.Reader
	writer      *bufio.Writer
	clientWrite *io.PipeWriter
	serverWrite *io.PipeWriter
	done        chan error
}

// startSamplingSession builds a minimal server and serves it over pipes. The
// tool exists only because NewServer requires one; sampling never calls it.
func startSamplingSession(t *testing.T) *samplingSession {
	t.Helper()
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []mcp.ServerTool{{
			Name: "probe",
			Handler: func(ctx context.Context, arguments json.RawMessage) (mcp.CallResult, error) {
				return mcp.CallResult{Content: []mcp.Content{{Type: "text", Text: "ok"}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	session := &samplingSession{
		t:           t,
		server:      server,
		reader:      bufio.NewReader(clientRead),
		writer:      bufio.NewWriter(clientWrite),
		clientWrite: clientWrite,
		serverWrite: serverWrite,
		done:        make(chan error, 1),
	}
	go func() { session.done <- server.Serve(context.Background(), serverRead, serverWrite) }()
	t.Cleanup(func() {
		_ = clientWrite.Close()
		_ = serverWrite.Close()
		select {
		case <-session.done:
		case <-time.After(10 * time.Second):
			t.Errorf("the MCP server did not stop")
		}
	})
	return session
}

// initialize performs the handshake, advertising the sampling capability only
// when the test wants a client that can be asked.
func (s *samplingSession) initialize(advertise bool) {
	s.t.Helper()
	capabilities := ""
	if advertise {
		capabilities = `,"capabilities":{"sampling":{}}`
	}
	s.write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"` + capabilities + `}}`)
	response := s.read()
	if _, ok := response["result"]; !ok {
		s.t.Fatalf("initialize failed: %v", response)
	}
}

func (s *samplingSession) write(frame string) {
	s.t.Helper()
	if _, err := s.writer.WriteString(frame + "\n"); err != nil {
		s.t.Fatalf("write failed: %v", err)
	}
	if err := s.writer.Flush(); err != nil {
		s.t.Fatalf("flush failed: %v", err)
	}
}

// read returns the next frame the server wrote, failing rather than blocking
// forever when it writes nothing.
func (s *samplingSession) read() map[string]any {
	s.t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			lines <- ""
			return
		}
		lines <- line
	}()
	select {
	case line := <-lines:
		if line == "" {
			s.t.Fatal("the server closed its stream before writing")
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			s.t.Fatalf("the frame is not JSON (%v): %s", err, line)
		}
		return decoded
	case <-time.After(10 * time.Second):
		s.t.Fatal("the server wrote no frame")
		return nil
	}
}

// answer replies to the server-initiated request with the given id.
func (s *samplingSession) answer(id, result string) {
	s.t.Helper()
	s.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":%s}`, id, result))
}

// streamOutcome is one completed model call: the events it produced, or the
// error it failed with.
type streamOutcome struct {
	events <-chan model.Event
	err    error
}

// TestModelStreamDelegatesToTheClient pins the adapter's whole job: a harness
// model call becomes one sampling/createMessage carrying the conversation
// (with the system messages folded into systemPrompt), and the client's single
// answer becomes the delta-plus-done stream the interface expects.
func TestModelStreamDelegatesToTheClient(t *testing.T) {
	session := startSamplingSession(t)
	session.initialize(true)
	adapter := New(session.server)

	done := make(chan streamOutcome, 1)
	go func() {
		events, err := adapter.Stream(context.Background(), model.Request{Messages: []model.Message{
			{Role: "system", Content: "You are terse."},
			{Role: "user", Content: "Say hi."},
		}})
		done <- streamOutcome{events: events, err: err}
	}()

	frame := session.read()
	if frame["method"] != "sampling/createMessage" {
		t.Fatalf("the client was asked with %v", frame["method"])
	}
	id, ok := frame["id"].(string)
	if !ok {
		t.Fatalf("the sampling request has no string id: %v", frame)
	}
	params, _ := frame["params"].(map[string]any)
	if params["systemPrompt"] != "You are terse." {
		t.Fatalf("systemPrompt = %v", params["systemPrompt"])
	}
	if params["maxTokens"] != float64(mcp.DefaultSamplingMaxTokens) {
		t.Fatalf("maxTokens = %v, want the default %d", params["maxTokens"], mcp.DefaultSamplingMaxTokens)
	}
	messages, _ := params["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("the sampling messages = %#v", params["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if message["role"] != "user" {
		t.Fatalf("the user message role = %v", message["role"])
	}
	content, _ := message["content"].(map[string]any)
	if content["text"] != "Say hi." {
		t.Fatalf("the user message content = %#v", message["content"])
	}

	session.answer(id, `{"role":"assistant","content":{"type":"text","text":"hi there"},"model":"client-model"}`)

	result := <-done
	if result.err != nil {
		t.Fatalf("Stream returned error: %v", result.err)
	}
	var events []model.Event
	for event := range result.events {
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v, want a delta and a done", events)
	}
	if events[0].Type != model.EventDelta || events[0].Delta != "hi there" {
		t.Fatalf("the first event = %#v", events[0])
	}
	if events[1].Type != model.EventDone || events[1].Message == nil {
		t.Fatalf("the done event = %#v", events[1])
	}
	if events[1].Message.Role != "assistant" || events[1].Message.Content != "hi there" {
		t.Fatalf("the done message = %#v", events[1].Message)
	}
	if events[1].Meta["model"] != "client-model" {
		t.Fatalf("the done metadata did not report the client's model: %#v", events[1].Meta)
	}
}

// TestModelGenerateSurfacesAnUnsupportedClientAsAnError pins that an operator
// who opted into sampling is never handed an empty answer: a client that
// cannot sample fails the model call with the sampling sentinel still
// discoverable, which is how the run reports why it could not proceed.
func TestModelGenerateSurfacesAnUnsupportedClientAsAnError(t *testing.T) {
	session := startSamplingSession(t)
	session.initialize(false)
	adapter := New(session.server)

	response, err := adapter.Generate(context.Background(), model.Request{Messages: []model.Message{
		{Role: "user", Content: "Say hi."},
	}})
	if err == nil {
		t.Fatalf("Generate returned a response for a client that cannot sample: %#v", response)
	}
	if response != nil {
		t.Fatalf("Generate returned both a response and an error: %#v", response)
	}
	if !errors.Is(err, mcp.ErrSamplingUnsupported) {
		t.Fatalf("Generate error = %v, want it to wrap ErrSamplingUnsupported", err)
	}
}

// TestModelStreamSurfacesAClientRefusalAsAnError pins the other failure shape:
// a client that advertised sampling and then answered with a JSON-RPC error
// fails the model call instead of producing an empty turn.
func TestModelStreamSurfacesAClientRefusalAsAnError(t *testing.T) {
	session := startSamplingSession(t)
	session.initialize(true)
	adapter := New(session.server)

	done := make(chan streamOutcome, 1)
	go func() {
		events, err := adapter.Stream(context.Background(), model.Request{Messages: []model.Message{
			{Role: "user", Content: "Say hi."},
		}})
		done <- streamOutcome{events: events, err: err}
	}()

	frame := session.read()
	id, _ := frame["id"].(string)
	session.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"error":{"code":-32601,"message":"sampling is not supported here"}}`, id))

	result := <-done
	if result.err == nil {
		t.Fatalf("Stream returned a stream for a refused request: %v", result.events)
	}
	if !strings.Contains(result.err.Error(), "sampling") {
		t.Fatalf("the model error does not name sampling: %v", result.err)
	}
}

// TestModelRefusesWhatItCannotDelegate pins the two requests the adapter
// refuses outright: the protocol has no field for an output schema, and no way
// to demand a tool call, so pretending either was honored would be a lie about
// what the client's model was asked to do.
func TestModelRefusesWhatItCannotDelegate(t *testing.T) {
	adapter := New(nil)
	cases := []struct {
		name    string
		request model.Request
		want    error
	}{
		{
			"output-schema",
			model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}, OutputSchema: map[string]any{"type": "object"}},
			model.ErrUnsupportedOutputSchema,
		},
		{
			"required-tool-choice",
			model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}, ToolChoice: model.ToolChoiceRequired},
			nil,
		},
		{
			"unbound-server",
			model.Request{Messages: []model.Message{{Role: "user", Content: "hi"}}},
			nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			events, err := adapter.Stream(context.Background(), testCase.request)
			if err == nil {
				t.Fatalf("Stream accepted a request it cannot delegate: %v", events)
			}
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("Stream error = %v, want %v", err, testCase.want)
			}
		})
	}
}
