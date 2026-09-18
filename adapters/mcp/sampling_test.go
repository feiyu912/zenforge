package mcp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// initializeWithSampling is a handshake that claims the sampling capability.
// It is the client's block, not this server's: the tests also pin that the
// server's own answer does not grow a sampling entry just because the client
// has one.
const initializeWithSampling = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"sampling":{}}}}`

// samplingOutcome is one completed Sample call.
type samplingOutcome struct {
	result SamplingResult
	err    error
}

// TestSamplingRoundTripsTheClientsAnswer pins the whole exchange: the request
// carries the messages and the optional fields the spec defines, the client is
// asked with the exact method name, and the decoded answer carries the text
// and the model the client actually used.
func TestSamplingRoundTripsTheClientsAnswer(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	stream.send(initializeWithSampling)
	// The server's own capability block is unchanged: sampling is the
	// client's capability, and advertising it here would promise a method
	// this server cannot serve.
	capabilities := resultOf(t, stream.read())["capabilities"].(map[string]any)
	if _, ok := capabilities["sampling"]; ok {
		t.Fatalf("the server advertised the client's sampling capability: %v", capabilities)
	}

	request := SamplingRequest{
		Messages: []SamplingMessage{{
			Role:    "user",
			Content: Content{Type: "text", Text: "What is 2+2?"},
		}},
		SystemPrompt: "Be brief.",
		ModelHint:    "claude-3-5-sonnet",
		MaxTokens:    64,
	}
	done := make(chan samplingOutcome, 1)
	go func() {
		result, err := server.Sample(context.Background(), request)
		done <- samplingOutcome{result: result, err: err}
	}()

	frame := stream.read()
	if frame["method"] != methodSamplingCreateMessage {
		t.Fatalf("the sampling method = %v, want %s", frame["method"], methodSamplingCreateMessage)
	}
	id, ok := frame["id"].(string)
	if !ok {
		t.Fatalf("the sampling request has no string id: %v", frame)
	}
	params, ok := frame["params"].(map[string]any)
	if !ok {
		t.Fatalf("the sampling request has no params: %v", frame)
	}
	if params["maxTokens"] != float64(64) {
		t.Fatalf("maxTokens = %v, want the caller's 64", params["maxTokens"])
	}
	if params["systemPrompt"] != "Be brief." {
		t.Fatalf("systemPrompt = %v", params["systemPrompt"])
	}
	preferences, _ := params["modelPreferences"].(map[string]any)
	hints, _ := preferences["hints"].([]any)
	if len(hints) != 1 {
		t.Fatalf("modelPreferences = %#v", params["modelPreferences"])
	}
	if hint, _ := hints[0].(map[string]any); hint["name"] != "claude-3-5-sonnet" {
		t.Fatalf("the model hint = %#v", hints[0])
	}
	messages, ok := params["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("the request did not carry the messages: %#v", params["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if message["role"] != "user" {
		t.Fatalf("the message role = %v", message["role"])
	}
	content, _ := message["content"].(map[string]any)
	if content["type"] != "text" || content["text"] != "What is 2+2?" {
		t.Fatalf("the message content = %#v", message["content"])
	}

	stream.send(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"result":{"role":"assistant","content":{"type":"text","text":"4"},"model":"client-model","stopReason":"endTurn"}}`,
		id,
	))
	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("Sample returned error: %v", outcome.err)
	}
	if outcome.result.Text() != "4" {
		t.Fatalf("the sampled text = %q", outcome.result.Text())
	}
	if outcome.result.Model != "client-model" {
		t.Fatalf("the sampling model = %q", outcome.result.Model)
	}
	if outcome.result.Role != "assistant" || outcome.result.StopReason != "endTurn" {
		t.Fatalf("the sampling result = %#v", outcome.result)
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("a completed sample left %d pending entries", got)
	}
}

// TestSamplingRefusedWhenTheClientDidNotAdvertiseIt pins the fallback gate: a
// client that never claimed sampling is refused before anything is written, so
// the caller can take another path instead of waiting out a timeout on a
// method the client never said it could answer.
func TestSamplingRefusedWhenTheClientDidNotAdvertiseIt(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	stream.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	resultOf(t, stream.read())

	_, err := server.Sample(context.Background(), SamplingRequest{
		Messages: []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "hi"}}},
	})
	if !errors.Is(err, ErrSamplingUnsupported) {
		t.Fatalf("Sample error = %v, want ErrSamplingUnsupported", err)
	}
	// Refused before the write: nothing was put on the wire.
	expectNoFrame(t, stream)
}

// TestSamplingRefusedWithNoStream pins the other refusal: a server that is not
// serving has nowhere to send the request at all.
func TestSamplingRefusedWithNoStream(t *testing.T) {
	_, err := testServer(t).Sample(context.Background(), SamplingRequest{
		Messages: []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "hi"}}},
	})
	if !errors.Is(err, ErrNotServing) {
		t.Fatalf("Sample error = %v, want ErrNotServing", err)
	}
}

// TestSamplingTimesOutAndLeavesNoPendingState pins the deadline half of "no
// unbounded wait": a client that is asked and never answers must not hold the
// caller, and the abandoned request must leave no registry entry behind.
func TestSamplingTimesOutAndLeavesNoPendingState(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	stream.send(initializeWithSampling)
	resultOf(t, stream.read())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan samplingOutcome, 1)
	go func() {
		result, err := server.Sample(ctx, SamplingRequest{
			Messages: []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "hi"}}},
		})
		done <- samplingOutcome{result: result, err: err}
	}()

	frame := stream.read()
	if frame["method"] != methodSamplingCreateMessage {
		t.Fatalf("the frame was not a sampling request: %v", frame)
	}
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Fatalf("Sample error = %v, want the context deadline", outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sampling timeout never returned")
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("a timed-out sample left %d pending entries", got)
	}
	// The stream is still usable after the abandoned request.
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if pong := stream.read(); pong["id"] != float64(2) {
		t.Fatalf("the server did not answer after a sampling timeout: %v", pong)
	}
}

// TestSamplingRejectsAnEmptyMessageList pins that a request with nothing to ask
// is refused before it is written, rather than sent as a malformed request the
// client would have to reject.
func TestSamplingRejectsAnEmptyMessageList(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	stream.send(initializeWithSampling)
	resultOf(t, stream.read())

	if _, err := server.Sample(context.Background(), SamplingRequest{}); err == nil {
		t.Fatal("an empty sampling request was accepted")
	}
	expectNoFrame(t, stream)
}

// TestClientSupportsSamplingFollowsTheHandshake mirrors the elicitation
// accessor: it reads the latest handshake only, so a client that never
// initialized or never claimed the capability is false, a client that claimed
// it is true, and a later handshake that drops it is false again rather than
// leaving the older claim behind.
func TestClientSupportsSamplingFollowsTheHandshake(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	if server.ClientSupportsSampling() {
		t.Fatal("a server that never initialized claims the client supports sampling")
	}
	stream.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	resultOf(t, stream.read())
	if server.ClientSupportsSampling() {
		t.Fatal("a client that did not advertise sampling reads as supporting it")
	}
	stream.send(initializeWithSampling)
	resultOf(t, stream.read())
	if !server.ClientSupportsSampling() {
		t.Fatal("a client that advertised sampling reads as not supporting it")
	}
	// A null capability is not an advertisement either: the spec's values are
	// objects, and presence means presence of an object.
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"sampling":null}}}`)
	resultOf(t, stream.read())
	if server.ClientSupportsSampling() {
		t.Fatal("a null sampling capability was read as an advertisement")
	}
}
