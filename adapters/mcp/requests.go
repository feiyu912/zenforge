package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Errors a server-initiated request can fail with before or while it waits.
// They are exported so a caller can tell "there is no stream to ask on" from
// "the client never answered" and fall back accordingly, instead of retrying a
// call that could never work.
var (
	// ErrNotServing means the server has no outbound stream: Serve is not
	// running, or it already returned. A request cannot be written anywhere,
	// so it fails at once rather than waiting for an answer that can never
	// come.
	ErrNotServing = errors.New("mcp server is not serving a stream")
	// ErrStreamClosed means the stream ended while a server request was in
	// flight, so the answer it was waiting for can never arrive.
	ErrStreamClosed = errors.New("mcp server stream closed before the client answered")
)

// serverRequestIDPrefix namespaces the ids this server mints for its own
// requests. The prefix is what makes the two id spaces disjoint by
// construction: a server id is always a *string* `srv-<n>`, while the ids a
// client sends are echoed back raw and are never touched. A client that
// happened to send `srv-1` for one of its own requests could not be confused
// with a server request either, because the frame routing keys on the
// request/response shape (a method, or no method) before it looks at the id.
const serverRequestIDPrefix = "srv-"

// pendingResponse is what one waiting Request receives: either the client's
// answer, or the reason no answer can arrive.
type pendingResponse struct {
	result json.RawMessage
	err    *rpcError
	failed error
}

// Request sends a server-initiated request to the client and waits for the
// response with the same id. It is the one way a handler can ask the client
// for something -- an elicitation, a sample, a root -- and it is deliberately
// separate from Notify: a notification is one-way and needs no second reader,
// while a request needs the reader to keep reading, which is why Serve no
// longer handles requests on the goroutine that reads the stream.
//
// The caller must supply a ctx with a deadline. This is enforced rather than
// defaulted here because a server request blocks a handler and holds a pending
// entry, and an unbounded wait would turn a client that never answers into a
// wedged run with no way to notice. Elicit is the convenience helper that
// supplies a documented default when the caller has none.
//
// Request returns:
//   - ErrNotServing if Serve is not running (or has returned);
//   - ctx.Err() if the caller's deadline or cancellation wins the race;
//   - ErrStreamClosed if Serve returns before the answer arrives;
//   - the JSON-RPC error object if the client answered with one;
//   - a decode error if the client's result does not fit result.
//
// A response that arrives after the deadline is dropped by id, exactly as the
// client side drops the late response of an abandoned call: the stream stays
// usable and the next request starts from a clean registry.
func (s *Server) Request(ctx context.Context, method string, params any, result any) error {
	if s == nil {
		return errors.New("mcp server is nil, so a request cannot be sent")
	}
	if ctx == nil {
		return errors.New("mcp server request needs a context")
	}
	name := strings.TrimSpace(method)
	if name == "" {
		return errors.New("mcp server request needs a method")
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("mcp server request %q needs a context deadline (Elicit applies a default when the caller has none)", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// The waiter is registered before the frame is written: the client can
	// answer the instant the write lands, and a waiter registered afterwards
	// would miss it. reserveRequest does that registration and the
	// "is anything serving?" check under one lock, so a request can never be
	// registered into a registry that Serve has already stopped draining.
	id, key, waiter, err := s.reserveRequest()
	if err != nil {
		return err
	}
	frame, err := json.Marshal(serverCall{
		JSONRPC: "2.0",
		ID:      id,
		Method:  name,
		Params:  params,
	})
	if err != nil {
		s.forgetRequest(key)
		return fmt.Errorf("mcp server request %q could not be encoded: %w", name, err)
	}
	if err := s.send(frame); err != nil {
		s.forgetRequest(key)
		return err
	}

	select {
	case response := <-waiter:
		if response.failed != nil {
			return response.failed
		}
		if response.err != nil {
			return response.err
		}
		if result == nil || len(response.result) == 0 {
			return nil
		}
		if err := json.Unmarshal(response.result, result); err != nil {
			return fmt.Errorf("mcp server request %q returned a result this caller cannot decode: %w", name, err)
		}
		return nil
	case <-ctx.Done():
		// The caller gave up. The entry is removed so a late response is
		// dropped by routeResponse instead of resolving a later request.
		s.forgetRequest(key)
		return ctx.Err()
	}
}

// reserveRequest mints the next server id and installs its waiter. Minting
// and registering happen under requestMu together with the serving check,
// which is what closes the race with a Serve that is shutting down: either
// the request is in the registry before Serve drains it, or it is refused.
func (s *Server) reserveRequest() (id string, key string, waiter chan pendingResponse, err error) {
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if !s.serving {
		return "", "", nil, ErrNotServing
	}
	s.nextRequestID++
	id = serverRequestIDPrefix + strconv.FormatUint(s.nextRequestID, 10)
	// The registry key is the id exactly as it appears on the wire, quotes
	// included. The id the client echoes back is compared with it byte for
	// byte, so no client id is ever parsed, renumbered or rewritten on either
	// side of the exchange.
	key = strconv.Quote(id)
	waiter = make(chan pendingResponse, 1)
	s.pendingRequests[key] = waiter
	return id, key, waiter, nil
}

// forgetRequest drops a waiter that is no longer interested: its caller gave
// up, or the frame could not be written. The channel is buffered and written
// at most once, so a later routeResponse or failPending for the same id is a
// no-op rather than a block.
func (s *Server) forgetRequest(key string) {
	s.requestMu.Lock()
	delete(s.pendingRequests, key)
	s.requestMu.Unlock()
}

// routeResponse delivers a response frame to the Request waiting for its id.
// It returns false when nobody is waiting, and the caller then drops the frame
// without answering it: a response is not a request, and an unsolicited error
// back at the client would desynchronize a peer that is merely late.
func (s *Server) routeResponse(envelope serverRequest) bool {
	if s == nil || len(envelope.ID) == 0 {
		return false
	}
	key := string(bytes.TrimSpace(envelope.ID))
	s.requestMu.Lock()
	waiter, ok := s.pendingRequests[key]
	if ok {
		delete(s.pendingRequests, key)
	}
	s.requestMu.Unlock()
	if !ok {
		return false
	}
	// Buffered, and written at most once because the entry was removed under
	// the lock: the reader never blocks on a waiter that is still starting up.
	waiter <- pendingResponse{result: envelope.Result, err: envelope.Error}
	return true
}

// failPending refuses every waiting request, which is what Serve does when its
// stream ends. A waiter must never be left hanging on a stream nobody will
// read again, and a handler blocked in Request must wake up so its goroutine
// can finish before Serve returns.
func (s *Server) failPending(err error) {
	if s == nil {
		return
	}
	s.requestMu.Lock()
	s.serving = false
	pending := s.pendingRequests
	s.pendingRequests = map[string]chan pendingResponse{}
	s.requestMu.Unlock()
	for _, waiter := range pending {
		select {
		case waiter <- pendingResponse{failed: err}:
		default:
			// Unreachable while a waiter is written at most once; the
			// non-blocking send keeps a future bug from wedging shutdown.
		}
	}
}

// isServing reports whether Serve currently owns an outbound stream.
func (s *Server) isServing() bool {
	if s == nil {
		return false
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	return s.serving
}

// pendingRequestCount is the number of server requests still waiting for an
// answer. It exists for the tests that pin "no pending state is left behind",
// which is otherwise invisible from outside the package.
func (s *Server) pendingRequestCount() int {
	if s == nil {
		return 0
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	return len(s.pendingRequests)
}

// recordWriteError keeps the first transport write that failed on a request
// goroutine, so Serve can still report a broken stream: the write no longer
// happens on Serve's own goroutine, so there is no return value to carry it.
func (s *Server) recordWriteError(err error) {
	if s == nil || err == nil {
		return
	}
	s.writeErrMu.Lock()
	if s.writeErr == nil {
		s.writeErr = err
	}
	s.writeErrMu.Unlock()
}

// takeWriteError returns and clears the recorded write error.
func (s *Server) takeWriteError() error {
	s.writeErrMu.Lock()
	defer s.writeErrMu.Unlock()
	err := s.writeErr
	s.writeErr = nil
	return err
}

// isResponseEnvelope reports whether a decoded frame is a response rather than
// a request or a notification. The spec makes that a property of shape, not of
// id: a request always carries a method, a response never does. An `"id":null`
// frame is not a response either -- null is no id at all -- so it falls through
// to dispatch and is answered with the method-not-found error it got before
// server requests existed.
func isResponseEnvelope(envelope serverRequest) bool {
	if envelope.Method != "" || len(envelope.ID) == 0 {
		return false
	}
	return !bytes.Equal(bytes.TrimSpace(envelope.ID), []byte("null"))
}

// serverCall is the wire form of a server-initiated request. ID is a plain
// string so the counter in it survives as a JSON string: the client echoes it
// verbatim, and routeResponse compares it byte for byte with the registry key.
type serverCall struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}
