package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// serverStream drives a Server over two pipes, the way a stdio peer sees it.
// The test owns the client side: it writes frames on writer and reads the
// server's frames from reader. io.Pipe is unbuffered, so a Server write blocks
// until the test reads it. That is what makes the tests below deterministic:
// the handler cannot finish its server request until the test has seen the
// request, and a ping cannot overtake a response the test is still holding up.
type serverStream struct {
	t           *testing.T
	server      *Server
	reader      *bufio.Reader
	writer      *bufio.Writer
	clientWrite *io.PipeWriter
	serverWrite *io.PipeWriter
	done        chan error

	closeOnce sync.Once
}

func startServerStream(t *testing.T, server *Server) *serverStream {
	t.Helper()
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	stream := &serverStream{
		t:           t,
		server:      server,
		reader:      bufio.NewReader(clientRead),
		writer:      bufio.NewWriter(clientWrite),
		clientWrite: clientWrite,
		serverWrite: serverWrite,
		done:        make(chan error, 1),
	}
	// Serve runs on its own goroutine exactly as it would against a child
	// process's stdio, so the reader/handler split is exercised rather than
	// bypassed through Handle.
	go func() {
		stream.done <- server.Serve(context.Background(), serverRead, serverWrite)
	}()
	t.Cleanup(stream.cleanup)
	return stream
}

// cleanup releases both pipe ends so Serve and any goroutine blocked on a read
// in a test helper can finish. Leaving the pipe open would leak the Serve
// goroutine and the reader used by expectNoFrame.
func (s *serverStream) cleanup() {
	s.closeInput()
	_ = s.serverWrite.Close()
}

func (s *serverStream) send(message string) {
	s.t.Helper()
	if _, err := s.writer.WriteString(message + "\n"); err != nil {
		s.t.Fatalf("write failed: %v", err)
	}
	if err := s.writer.Flush(); err != nil {
		s.t.Fatalf("flush failed: %v", err)
	}
}

func (s *serverStream) read() map[string]any {
	s.t.Helper()
	line, err := s.reader.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read failed: %v", err)
	}
	return decodeFrame(s.t, []byte(line))
}

// closeInput ends the client's side of the input stream. It is idempotent so
// tests may call it and the cleanup may call it again.
func (s *serverStream) closeInput() {
	s.closeOnce.Do(func() {
		if err := s.clientWrite.Close(); err != nil {
			s.t.Fatalf("close failed: %v", err)
		}
	})
}

// wait returns Serve's result, failing the test rather than hanging forever if
// the shutdown path is broken.
func (s *serverStream) wait() error {
	s.t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(5 * time.Second):
		s.t.Fatal("Serve did not return")
		return nil
	}
}

// expectNoFrame asserts the server wrote nothing within a short window. It is
// how a test pins "ignored" as silence: a regression that answered an unwanted
// response would put a frame here, and the window keeps the failure quick
// instead of turning it into a read that never returns.
func expectNoFrame(t *testing.T, stream *serverStream) {
	t.Helper()
	frames := make(chan string, 1)
	go func() {
		line, err := stream.reader.ReadString('\n')
		if err != nil {
			frames <- ""
			return
		}
		frames <- line
	}()
	select {
	case line := <-frames:
		t.Fatalf("an unexpected frame arrived: %s", line)
	case <-time.After(200 * time.Millisecond):
	}
}

// requestServer builds a server whose "ask" tool sends one server request and
// reports the answer in its result. The handler captures the Server through a
// closure because a handler is handed a context, not the server it belongs to;
// the error it saw is pushed on handlerErr so a test can assert how Request
// ended even when the tool call itself turns the failure into a result.
func requestServer(t *testing.T, method string, timeout time.Duration, handlerErr chan<- error) *Server {
	t.Helper()
	var server *Server
	server, err := NewServer(ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []ServerTool{{
			Name: "ask",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				requestCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				var answer struct {
					Answer string `json:"answer"`
				}
				err := server.Request(requestCtx, method, map[string]any{"question": "?"}, &answer)
				if handlerErr != nil {
					handlerErr <- err
				}
				if err != nil {
					return CallResult{}, err
				}
				return CallResult{Content: []Content{{Type: "text", Text: answer.Answer}}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return server
}

// TestServerRequestRoundTripsWhileTheCallContinues pins the whole feature: a
// handler sends a request to its client, the client answers with the
// server-namespaced id echoed verbatim, the handler receives the decoded
// result, and the tool call that started it all still gets its own response.
func TestServerRequestRoundTripsWhileTheCallContinues(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := requestServer(t, "test/question", 5*time.Second, handlerErr)
	stream := startServerStream(t, server)

	stream.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`)

	// The handler's request reaches the client first, with a server id in its
	// own namespace: a string prefixed srv-, never the client's numeric id.
	request := stream.read()
	if request["method"] != "test/question" {
		t.Fatalf("the server request method = %v", request["method"])
	}
	id, ok := request["id"].(string)
	if !ok || !strings.HasPrefix(id, serverRequestIDPrefix) {
		t.Fatalf("the server request id = %v, want a %q string", request["id"], serverRequestIDPrefix)
	}
	params, ok := request["params"].(map[string]any)
	if !ok || params["question"] != "?" {
		t.Fatalf("the server request params = %v", request["params"])
	}

	// The client answers, echoing the id. Its own request id is untouched: the
	// tool-call response below still carries the numeric 1.
	stream.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"answer":"42"}}`, id))

	response := stream.read()
	if response["id"] != float64(1) {
		t.Fatalf("the call response carries id %v, want the client's 1", response["id"])
	}
	result := resultOf(t, response)
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("the tool result = %v", result)
	}
	if text := content[0].(map[string]any); text["text"] != "42" {
		t.Fatalf("the handler did not receive the decoded answer: %v", text)
	}
	if err := <-handlerErr; err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("a completed request left %d pending entries", got)
	}

	stream.closeInput()
	if err := stream.wait(); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
}

// TestServerKeepsAnsweringWhileARequestIsPending is the deadlock the reader
// split exists to fix: while one handler is blocked waiting for its client's
// answer, the reader must still read and answer the next request on the same
// stream. Before the split, the ping would have queued behind the blocked
// handler and this test would time out.
func TestServerKeepsAnsweringWhileARequestIsPending(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := requestServer(t, "test/question", 5*time.Second, handlerErr)
	stream := startServerStream(t, server)

	stream.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`)
	request := stream.read()
	id, ok := request["id"].(string)
	if !ok {
		t.Fatalf("the server request has no string id: %v", request)
	}

	// The handler is provably still blocked at this point -- it has not
	// received its answer yet -- and the pong still arrives.
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	pong := stream.read()
	if pong["id"] != float64(2) {
		t.Fatalf("the frame answered while the request was pending was not the pong: %v", pong)
	}

	// Only now does the client answer, and the original call follows.
	stream.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"answer":"late"}}`, id))
	result := resultOf(t, stream.read())
	content := result["content"].([]any)
	if text := content[0].(map[string]any); text["text"] != "late" {
		t.Fatalf("the handler did not receive the late answer: %v", text)
	}
	if err := <-handlerErr; err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
}

// TestServerIgnoresAResponseWithAnUnknownID pins the rule that a response is
// never dispatched as a request: an id nobody is waiting for is dropped, with
// no method-not-found error and no effect on the stream.
func TestServerIgnoresAResponseWithAnUnknownID(t *testing.T) {
	stream := startServerStream(t, testServer(t))

	// A response to a request this server never sent. It is not a request, so
	// answering it would be a protocol error aimed at the client.
	stream.send(`{"jsonrpc":"2.0","id":"srv-999","result":{}}`)
	stream.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	pong := stream.read()
	if pong["id"] != float64(1) {
		t.Fatalf("the ping was not the first frame answered: %v", pong)
	}
	// Silence: the unknown response produced no frame of its own.
	expectNoFrame(t, stream)
}

// TestServerRequestTimesOutAndLeavesNoPendingState pins the deadline half of
// "no unbounded wait": the caller's context ends the wait, the failure reaches
// the handler, the registry is empty again, and the late answer is dropped by
// id instead of resolving anything.
func TestServerRequestTimesOutAndLeavesNoPendingState(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := requestServer(t, "test/question", 50*time.Millisecond, handlerErr)
	stream := startServerStream(t, server)

	stream.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`)
	request := stream.read()
	id, ok := request["id"].(string)
	if !ok {
		t.Fatalf("the server request has no string id: %v", request)
	}

	// The tool call fails with the cause, rather than hanging the stream.
	result := resultOf(t, stream.read())
	if result["isError"] != true {
		t.Fatalf("a timed-out request did not fail its call: %v", result)
	}
	if err := <-handlerErr; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request returned %v, want the context deadline", err)
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("a timed-out request left %d pending entries", got)
	}

	// The late answer is ignored, and the stream still answers the next ping.
	stream.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"answer":"too late"}}`, id))
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	pong := stream.read()
	if pong["id"] != float64(2) {
		t.Fatalf("the server did not answer after a late response: %v", pong)
	}
	expectNoFrame(t, stream)
}

// TestServerRequestFailsWhenServeEnds pins the other unbounded-wait guard: the
// deadline is far away, the client simply goes away, and Serve returning must
// refuse the waiter instead of leaving it, and its handler goroutine, hanging.
func TestServerRequestFailsWhenServeEnds(t *testing.T) {
	handlerErr := make(chan error, 1)
	server := requestServer(t, "test/question", 30*time.Second, handlerErr)
	stream := startServerStream(t, server)

	stream.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`)
	request := stream.read()
	if _, ok := request["id"].(string); !ok {
		t.Fatalf("the server request has no string id: %v", request)
	}

	stream.closeInput()
	if err := stream.wait(); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	if err := <-handlerErr; !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Request returned %v, want ErrStreamClosed", err)
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("Serve left %d pending entries", got)
	}
}

// TestServerRequestRefusedWithNoStream pins the clear refusal a caller needs
// before it can fall back: without a stream there is nowhere to write, so the
// request fails at once instead of waiting for an answer that cannot come.
func TestServerRequestRefusedWithNoStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := testServer(t).Request(ctx, "test/question", nil, nil); !errors.Is(err, ErrNotServing) {
		t.Fatalf("Request error = %v, want ErrNotServing", err)
	}
}

// TestServerRequestNeedsACallerDeadline pins that Request refuses to wait
// forever: a context with no deadline is a programming error, and the caller
// is told so in the error rather than handed a wait that never ends.
func TestServerRequestNeedsACallerDeadline(t *testing.T) {
	err := testServer(t).Request(context.Background(), "test/question", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Request with no deadline = %v, want an error naming the deadline", err)
	}
}

// TestServerConcurrentPanicDoesNotKillTheStream pins the panic guarantee in the
// new shape: handlers now run on their own goroutines, and a panic there must
// still fail only its own call. The ping sent alongside it is answered, and
// the stream is usable afterwards.
func TestServerConcurrentPanicDoesNotKillTheStream(t *testing.T) {
	stream := startServerStream(t, testServer(t))

	stream.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"panic"}}`)
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	// The two responses race their goroutines, so they are collected by id
	// rather than in order.
	responses := map[float64]map[string]any{}
	for index := 0; index < 2; index++ {
		frame := stream.read()
		id, ok := frame["id"].(float64)
		if !ok {
			t.Fatalf("frame %d carries no numeric id: %v", index, frame)
		}
		responses[id] = frame
	}
	failed, ok := responses[1]
	if !ok {
		t.Fatalf("the panicking call was not answered: %v", responses)
	}
	result, ok := failed["result"].(map[string]any)
	if !ok || result["isError"] != true {
		t.Fatalf("a panicking handler did not fail its call: %v", failed)
	}
	if _, ok := responses[2]; !ok {
		t.Fatalf("the ping was not answered next to the panic: %v", responses)
	}

	// The stream is alive: the next request is answered normally.
	stream.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if pong := stream.read(); pong["id"] != float64(3) {
		t.Fatalf("the server did not survive the panic: %v", pong)
	}
}

// TestServerServeShutdownLeavesNothingRunning pins clean shutdown: Serve waits
// for the handler goroutines it started, so when it returns the blocked
// handler has already observed the stream error and finished.
func TestServerServeShutdownLeavesNothingRunning(t *testing.T) {
	handlerDone := make(chan struct{})
	handlerErr := make(chan error, 1)
	var server *Server
	server, err := NewServer(ServerConfig{
		Name:    "zenforge",
		Version: "test",
		Tools: []ServerTool{{
			Name: "ask",
			Handler: func(ctx context.Context, arguments json.RawMessage) (CallResult, error) {
				defer close(handlerDone)
				requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				err := server.Request(requestCtx, "test/question", map[string]any{}, nil)
				handlerErr <- err
				return CallResult{}, err
			},
		}},
	})
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	stream := startServerStream(t, server)
	stream.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask"}}`)
	stream.read()

	stream.closeInput()
	if err := stream.wait(); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("Serve returned while a handler was still running")
	}
	if err := <-handlerErr; !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Request returned %v, want ErrStreamClosed", err)
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("shutdown left %d pending entries", got)
	}
}
