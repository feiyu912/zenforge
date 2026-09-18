package mcp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// initializeWithElicitation is a handshake that claims the elicitation
// capability. It is the client's block, not this server's: the tests also pin
// that the server's own answer does not grow an elicitation entry just because
// the client has one.
const initializeWithElicitation = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{}}}}`

// elicitationOutcome is one completed Elicit call.
type elicitationOutcome struct {
	result ElicitationResult
	err    error
}

// elicitWhileClientAnswers runs Elicit on its own goroutine and answers the
// request it puts on the wire. Elicit blocks, so the client's side of the
// exchange has to happen on this goroutine.
func elicitWhileClientAnswers(t *testing.T, stream *serverStream, server *Server, answer string) elicitationOutcome {
	t.Helper()
	done := make(chan elicitationOutcome, 1)
	go func() {
		result, err := Elicit(context.Background(), server, "Your name?", map[string]any{
			"type":       "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}},
		})
		done <- elicitationOutcome{result: result, err: err}
	}()

	request := stream.read()
	if request["method"] != methodElicitationCreate {
		t.Fatalf("the elicitation method = %v, want %s", request["method"], methodElicitationCreate)
	}
	id, ok := request["id"].(string)
	if !ok {
		t.Fatalf("the elicitation request has no string id: %v", request)
	}
	params, ok := request["params"].(map[string]any)
	if !ok || params["message"] != "Your name?" {
		t.Fatalf("the elicitation params = %v", request["params"])
	}
	if _, ok := params["requestedSchema"].(map[string]any); !ok {
		t.Fatalf("the elicitation did not carry the requested schema: %v", request["params"])
	}
	stream.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":%s}`, id, answer))
	return <-done
}

// TestElicitationAcceptsDeclinesAndCancels pins the three actions the spec
// defines, including the content an accept carries and the absence of content
// on a decision that has none.
func TestElicitationAcceptsDeclinesAndCancels(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		want   ElicitationResult
	}{
		{
			"accept",
			`{"action":"accept","content":{"name":"Ada"}}`,
			ElicitationResult{Action: ElicitationActionAccept, Content: map[string]any{"name": "Ada"}},
		},
		{"decline", `{"action":"decline"}`, ElicitationResult{Action: ElicitationActionDecline}},
		{"cancel", `{"action":"cancel"}`, ElicitationResult{Action: ElicitationActionCancel}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := testServer(t)
			stream := startServerStream(t, server)
			stream.send(initializeWithElicitation)
			// The server's own capability block is unchanged: elicitation is
			// the client's capability, and advertising it here would promise a
			// method this server cannot serve.
			capabilities := resultOf(t, stream.read())["capabilities"].(map[string]any)
			if _, ok := capabilities["elicitation"]; ok {
				t.Fatalf("the server advertised the client's elicitation capability: %v", capabilities)
			}

			outcome := elicitWhileClientAnswers(t, stream, server, testCase.answer)
			if outcome.err != nil {
				t.Fatalf("Elicit returned error: %v", outcome.err)
			}
			if !reflect.DeepEqual(outcome.result, testCase.want) {
				t.Fatalf("Elicit result = %#v, want %#v", outcome.result, testCase.want)
			}
			if got := server.pendingRequestCount(); got != 0 {
				t.Fatalf("a completed elicitation left %d pending entries", got)
			}
		})
	}
}

// TestElicitationRefusedWhenTheClientDidNotAdvertiseIt pins the fallback gate:
// a client that never claimed elicitation is refused before anything is
// written, so the caller can take another path instead of waiting out a
// timeout on a method the client never said it could answer.
func TestElicitationRefusedWhenTheClientDidNotAdvertiseIt(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	stream.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	resultOf(t, stream.read())

	if _, err := Elicit(context.Background(), server, "Your name?", nil); !errors.Is(err, ErrElicitationUnsupported) {
		t.Fatalf("Elicit error = %v, want ErrElicitationUnsupported", err)
	}
	// Refused before the write: nothing was put on the wire.
	expectNoFrame(t, stream)
}

// TestElicitationRefusedWithNoStream pins the other refusal: a server that is
// not serving has nowhere to send the request at all.
func TestElicitationRefusedWithNoStream(t *testing.T) {
	if _, err := Elicit(context.Background(), testServer(t), "Your name?", nil); !errors.Is(err, ErrNotServing) {
		t.Fatalf("Elicit error = %v, want ErrNotServing", err)
	}
}

// TestClientSupportsElicitationFollowsTheHandshake pins the exported accessor a
// caller uses to decide whether an elicitation is written at all. It reads the
// latest handshake only: a client that never initialized or never claimed the
// capability is false, a client that claimed it is true, and a later handshake
// that drops it is false again rather than leaving the older claim behind.
func TestClientSupportsElicitationFollowsTheHandshake(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	if server.ClientSupportsElicitation() {
		t.Fatal("a server that never initialized claims the client supports elicitation")
	}
	stream.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	resultOf(t, stream.read())
	if server.ClientSupportsElicitation() {
		t.Fatal("a client that did not advertise elicitation reads as supporting it")
	}
	stream.send(initializeWithElicitation)
	resultOf(t, stream.read())
	if !server.ClientSupportsElicitation() {
		t.Fatal("a client that advertised elicitation reads as not supporting it")
	}
	// A null capability is not an advertisement either: the spec's values are
	// objects, and presence means presence of an object.
	stream.send(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":null}}}`)
	resultOf(t, stream.read())
	if server.ClientSupportsElicitation() {
		t.Fatal("a null elicitation capability was read as an advertisement")
	}
}

// TestElicitationRejectsAnUnknownAction pins that a client cannot smuggle an
// action past the caller's switch: an answer that is none of the three spec
// actions is an error, not an empty result a caller might treat as a decline.
func TestElicitationRejectsAnUnknownAction(t *testing.T) {
	server := testServer(t)
	stream := startServerStream(t, server)
	stream.send(initializeWithElicitation)
	resultOf(t, stream.read())

	outcome := elicitWhileClientAnswers(t, stream, server, `{"action":"maybe"}`)
	if outcome.err == nil {
		t.Fatalf("Elicit accepted an unknown action: %#v", outcome.result)
	}
	if got := server.pendingRequestCount(); got != 0 {
		t.Fatalf("an unknown action left %d pending entries", got)
	}
}
