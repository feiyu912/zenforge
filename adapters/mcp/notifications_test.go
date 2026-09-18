package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"
)

// notificationCapture is a NotificationSink that keeps every frame, so a test
// can assert both what a handler reported and where it landed relative to the
// response.
type notificationCapture struct {
	frames [][]byte
}

func (c *notificationCapture) sink(frame []byte) error {
	c.frames = append(c.frames, append([]byte(nil), frame...))
	return nil
}

func (c *notificationCapture) decoded(t *testing.T, index int) map[string]any {
	t.Helper()
	if index >= len(c.frames) {
		t.Fatalf("notification %d was never sent (sent %d)", index, len(c.frames))
	}
	return decodeFrame(t, c.frames[index])
}

func decodeFrame(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("the frame is not JSON (%v): %s", err, frame)
	}
	return decoded
}

// singleLine rejects a frame that carries more than the one JSON message a
// stdio line is allowed to hold.
func singleLine(t *testing.T, frame []byte) string {
	t.Helper()
	line := strings.TrimRight(string(frame), "\n")
	if strings.ContainsAny(line, "\n\r") {
		t.Fatalf("a frame carried more than one line: %q", frame)
	}
	if !json.Valid([]byte(line)) {
		t.Fatalf("a frame is not well-formed JSON: %q", frame)
	}
	return line
}

// progressServer builds a server whose handlers on all three progress-capable
// surfaces report two steps and then answer.
func progressServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []ServerTool{{
			Name: "stream",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				report := ProgressFrom(ctx)
				report(1, "started")
				report(2, "")
				return CallResult{Content: []Content{{Type: "text", Text: "done"}}}, nil
			},
		}},
		Resources: []ServerResource{{
			URI: "zenforge://runs",
			Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
				ProgressFrom(ctx)(1, "reading")
				return []ResourceContent{{URI: uri, Text: "contents"}}, nil
			},
		}},
		Prompts: []ServerPrompt{{
			Name: "review",
			Handler: func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
				ProgressFrom(ctx)(1, "rendering")
				return PromptResult{Messages: []PromptMessage{{
					Role:    PromptRoleUser,
					Content: Content{Type: "text", Text: "review this"},
				}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

// TestServerSendsProgressNotificationsBeforeTheResponse pins the shape and the
// timing: a handler's reports arrive as notifications/progress, in order, each
// one line, with the token echoed exactly as it was sent, all before the
// response Handle returns. The string and numeric cases are both the spec's
// token forms, and a numeric token must not come back quoted or rounded.
func TestServerSendsProgressNotificationsBeforeTheResponse(t *testing.T) {
	server := progressServer(t)
	cases := []struct {
		name    string
		token   string
		wantRaw string
	}{
		{"string token", `"run-7"`, `"progressToken":"run-7"`},
		{"numeric token", `42`, `"progressToken":42`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := &notificationCapture{}
			ctx := WithNotificationSink(context.Background(), capture.sink)
			response, respond := server.Handle(ctx, []byte(
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stream","_meta":{"progressToken":`+testCase.token+`}}}`,
			))
			if !respond {
				t.Fatal("the call was not answered")
			}
			if len(capture.frames) != 2 {
				t.Fatalf("progress notifications = %d, want 2: %q", len(capture.frames), capture.frames)
			}
			for index, wantProgress := range []float64{1, 2} {
				decoded := capture.decoded(t, index)
				if decoded["jsonrpc"] != "2.0" || decoded["method"] != methodProgress {
					t.Fatalf("notification %d = %v", index, decoded)
				}
				params, ok := decoded["params"].(map[string]any)
				if !ok {
					t.Fatalf("notification %d has no params: %v", index, decoded)
				}
				if params["progress"] != wantProgress {
					t.Fatalf("notification %d progress = %v, want %v", index, params["progress"], wantProgress)
				}
				line := singleLine(t, capture.frames[index])
				if !strings.Contains(line, testCase.wantRaw) {
					t.Fatalf("notification %d did not echo the token verbatim: %s", index, line)
				}
			}
			if message, _ := capture.decoded(t, 0)["params"].(map[string]any)["message"].(string); message != "started" {
				t.Fatalf("the message was lost: %v", capture.frames[0])
			}
			// An empty message is omitted, not sent as "": the field is
			// optional in the spec.
			if strings.Contains(string(capture.frames[1]), `"message"`) {
				t.Fatalf("an empty message was sent: %s", capture.frames[1])
			}
			// The response is one well-formed line that follows the
			// notifications: the sink ran during Handle, the response exists
			// only after it returned.
			singleLine(t, response)
			result := resultOf(t, decodeFrame(t, response))
			content := result["content"].([]any)
			if text := content[0].(map[string]any); text["text"] != "done" {
				t.Fatalf("the result changed: %v", text)
			}
		})
	}
}

// TestServerSendsNoProgressWithoutAToken pins the other half: a request with
// no progressToken gets no notification at all, even though the handler always
// asks for a reporter.
func TestServerSendsNoProgressWithoutAToken(t *testing.T) {
	server := progressServer(t)
	for _, message := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stream"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"stream","_meta":{}}}`,
	} {
		capture := &notificationCapture{}
		ctx := WithNotificationSink(context.Background(), capture.sink)
		response, respond := server.Handle(ctx, []byte(message))
		if !respond {
			t.Fatalf("the call was not answered: %s", message)
		}
		if len(capture.frames) != 0 {
			t.Fatalf("a request without a token received %q", capture.frames)
		}
		resultOf(t, decodeFrame(t, response))
	}
	// A token with no sink is still a valid call: the handler's reports are
	// dropped, not turned into an error or a panic.
	response, respond := server.Handle(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"stream","_meta":{"progressToken":"tok"}}}`,
	))
	if !respond {
		t.Fatal("a call with a token but no sink was not answered")
	}
	resultOf(t, decodeFrame(t, response))
}

// TestServerSendsProgressForResourcesAndPrompts pins that progress is not a
// tool-only feature: resources/read and prompts/get carry the same token and
// the same reporter.
func TestServerSendsProgressForResourcesAndPrompts(t *testing.T) {
	server := progressServer(t)
	cases := []struct {
		name         string
		message      string
		wantMessage  string
		wantContents string
	}{
		{
			"resources/read",
			`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"zenforge://runs","_meta":{"progressToken":"r"}}}`,
			"reading",
			"contents",
		},
		{
			"prompts/get",
			`{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"review","_meta":{"progressToken":"p"}}}`,
			"rendering",
			"review this",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := &notificationCapture{}
			ctx := WithNotificationSink(context.Background(), capture.sink)
			response, respond := server.Handle(ctx, []byte(testCase.message))
			if !respond {
				t.Fatal("the request was not answered")
			}
			if len(capture.frames) != 1 {
				t.Fatalf("progress notifications = %d, want 1", len(capture.frames))
			}
			params, _ := capture.decoded(t, 0)["params"].(map[string]any)
			if params["progress"] != float64(1) || params["message"] != testCase.wantMessage {
				t.Fatalf("notification = %v", capture.frames[0])
			}
			singleLine(t, response)
			if !strings.Contains(string(response), testCase.wantContents) {
				t.Fatalf("the response is %s", response)
			}
		})
	}
}

// dynamicServer is progressServer with DynamicLists on, used by the
// list-changed tests. The resource and prompt surfaces matter: a list-changed
// notification is only legal for a surface that is actually served.
func dynamicServer(t *testing.T, dynamic bool) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Name:         "zenforge",
		Version:      "test",
		DynamicLists: dynamic,
		Tools: []ServerTool{{
			Name: "noop",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				return CallResult{}, nil
			},
		}},
		Resources: []ServerResource{{
			URI: "zenforge://runs",
			Handler: func(ctx context.Context, uri string) ([]ResourceContent, error) {
				return nil, nil
			},
		}},
		Prompts: []ServerPrompt{{
			Name: "review",
			Handler: func(ctx context.Context, arguments map[string]string) (PromptResult, error) {
				return PromptResult{}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

// TestServerWithDynamicListsAdvertisesAndSendsListChanges drives the server
// over a real pipe, because the Notify methods write to the stream Serve
// attached: the capability, the notification, and the framing are all the
// client's view of the same promise.
func TestServerWithDynamicListsAdvertisesAndSendsListChanges(t *testing.T) {
	server := dynamicServer(t, true)
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer clientRead.Close()
	defer clientWrite.Close()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(context.Background(), serverRead, serverWrite)
	}()
	writer := bufio.NewWriter(clientWrite)
	reader := bufio.NewReader(clientRead)

	writeLine := func(message string) {
		t.Helper()
		if _, err := writer.WriteString(message + "\n"); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatalf("flush failed: %v", err)
		}
	}
	readLine := func() string {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}
		return line
	}

	writeLine(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	capabilities, ok := decodeFrame(t, []byte(readLine()))["result"].(map[string]any)["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("initialize returned no capabilities")
	}
	for surface, want := range map[string]any{
		"tools":     true,
		"resources": true,
		"prompts":   true,
	} {
		block, ok := capabilities[surface].(map[string]any)
		if !ok || block["listChanged"] != want {
			t.Fatalf("%s capability = %v, want listChanged %v", surface, capabilities[surface], want)
		}
	}

	cases := []struct {
		name   string
		call   func() error
		method string
	}{
		{"tools", server.NotifyToolsChanged, methodToolsListChanged},
		{"resources", server.NotifyResourcesChanged, methodResourcesListChanged},
		{"prompts", server.NotifyPromptsChanged, methodPromptsListChanged},
	}
	for _, testCase := range cases {
		// The write blocks until the read below happens -- an unbuffered pipe
		// joins the two -- so the call goes out on its own goroutine and the
		// client's view is read on this one.
		notified := make(chan error, 1)
		go func(call func() error) { notified <- call() }(testCase.call)
		frame := singleLine(t, []byte(readLine()))
		if err := <-notified; err != nil {
			t.Fatalf("Notify%sChanged returned error: %v", testCase.name, err)
		}
		decoded := decodeFrame(t, []byte(frame))
		if decoded["jsonrpc"] != "2.0" || decoded["method"] != testCase.method {
			t.Fatalf("%s notification = %s", testCase.name, frame)
		}
		if _, ok := decoded["params"]; ok {
			t.Fatalf("a list-changed notification carries no params: %s", frame)
		}
	}

	if err := clientWrite.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestServerWithoutDynamicListsRefusesListChanges pins the lie the default
// avoids: listChanged stays false, and a Notify call fails loudly instead of
// sending a notification the client was told never to expect.
func TestServerWithoutDynamicListsRefusesListChanges(t *testing.T) {
	server := dynamicServer(t, false)
	capabilities := capabilitiesOf(t, server)
	for _, surface := range []string{"tools", "resources", "prompts"} {
		block, ok := capabilities[surface].(map[string]any)
		if !ok {
			t.Fatalf("the server did not advertise %s", surface)
		}
		if block["listChanged"] != false {
			t.Fatalf("%s listChanged = %v, want false", surface, block["listChanged"])
		}
	}
	calls := []struct {
		name string
		call func() error
	}{
		{"NotifyToolsChanged", server.NotifyToolsChanged},
		{"NotifyResourcesChanged", server.NotifyResourcesChanged},
		{"NotifyPromptsChanged", server.NotifyPromptsChanged},
	}
	for _, testCase := range calls {
		err := testCase.call()
		if err == nil {
			t.Fatalf("%s sent a notification the client was told not to expect", testCase.name)
		}
		if !strings.Contains(err.Error(), "listChanged") {
			t.Fatalf("%s error does not say why: %v", testCase.name, err)
		}
	}
	// DynamicLists on a surface the server does not serve is still refused:
	// the capability was never advertised, so there is nothing to announce.
	toolsOnly, err := NewServer(ServerConfig{
		Name:         "zenforge",
		Version:      "test",
		DynamicLists: true,
		Tools: []ServerTool{{
			Name: "noop",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				return CallResult{}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	if err := toolsOnly.NotifyResourcesChanged(); err == nil || !strings.Contains(err.Error(), "serves no resources") {
		t.Fatalf("NotifyResourcesChanged on a tool-only server = %v", err)
	}
}

// TestServerDropsNonFiniteProgress pins the chosen handling of a value JSON
// cannot carry: a NaN or Infinity report is refused, the rest of the handler's
// reports still arrive, and the stream stays exactly one well-formed line per
// message.
func TestServerDropsNonFiniteProgress(t *testing.T) {
	server, err := NewServer(ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []ServerTool{{
			Name: "bad-progress",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				report := ProgressFrom(ctx)
				report(math.NaN(), "not a number")
				report(math.Inf(1), "not finite")
				report(math.Inf(-1), "nor this")
				report(1, "finite")
				return CallResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	capture := &notificationCapture{}
	ctx := WithNotificationSink(context.Background(), capture.sink)
	response, respond := server.Handle(ctx, []byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bad-progress","_meta":{"progressToken":9}}}`,
	))
	if !respond {
		t.Fatal("the call was not answered")
	}
	if len(capture.frames) != 1 {
		t.Fatalf("non-finite reports reached the stream: %q", capture.frames)
	}
	params, _ := capture.decoded(t, 0)["params"].(map[string]any)
	if params["progress"] != float64(1) || params["message"] != "finite" {
		t.Fatalf("the surviving notification = %s", capture.frames[0])
	}
	singleLine(t, response)
	resultOf(t, decodeFrame(t, response))
	// The stream is alive: the next request is answered normally.
	next, respond := server.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bad-progress"}}`))
	if !respond {
		t.Fatal("the server did not answer after a non-finite report")
	}
	resultOf(t, decodeFrame(t, next))
}
