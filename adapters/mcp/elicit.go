package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// methodElicitationCreate is the client-directed method Elicit sends. It is a
// client capability, not a server one: this package never advertises it in
// initialize's capabilities block, because the block says what the *server*
// offers, and a server that claimed elicitation there would be claiming a
// method it cannot serve.
const methodElicitationCreate = "elicitation/create"

// The three actions the spec allows a client to answer an elicitation with.
// They are exported so a caller can switch on them without retyping strings
// that a typo would silently turn into a different branch.
const (
	// ElicitationActionAccept means the user filled the form; Content holds
	// the values.
	ElicitationActionAccept = "accept"
	// ElicitationActionDecline means the user said no. It is a decision, not
	// a failure, and it carries no content.
	ElicitationActionDecline = "decline"
	// ElicitationActionCancel means the prompt was dismissed without a
	// decision, for example because the run was interrupted.
	ElicitationActionCancel = "cancel"
)

// DefaultElicitationTimeout bounds an elicitation whose caller supplied no
// deadline. Elicitation waits on a person, so the default is generous; a
// caller that knows its own budget should pass a context with a deadline and
// that budget wins. The bound exists so a client that never answers cannot
// hold a handler forever.
const DefaultElicitationTimeout = 5 * time.Minute

// ErrElicitationUnsupported means the client did not advertise the
// elicitation capability during initialize. Sending the request anyway would
// either hang until the timeout or draw a method-not-found error, so Elicit
// refuses up front and the caller can take its fallback path.
var ErrElicitationUnsupported = errors.New("mcp client did not advertise the elicitation capability")

// ElicitationResult is the client's answer to an elicitation. Action is one of
// the ElicitationAction* constants, and Content is present when the action is
// accept.
type ElicitationResult struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

// Elicit asks the client to collect input from its user and returns the
// answer. It is the convenience layer over Request: it builds the
// `elicitation/create` params, applies DefaultElicitationTimeout when ctx has
// no deadline of its own, and decodes the `{action, content}` result.
//
// It refuses before writing anything when the server has no stream or the
// client did not advertise elicitation, so a caller always learns why it has
// to fall back instead of waiting on a request that cannot be answered.
func Elicit(ctx context.Context, s *Server, message string, schema map[string]any) (ElicitationResult, error) {
	if s == nil {
		return ElicitationResult{}, errors.New("mcp server is nil, so elicitation cannot be sent")
	}
	// The stream check comes first: without one there is nowhere to send the
	// request at all, and "not serving" is the more fundamental reason.
	if !s.isServing() {
		return ElicitationResult{}, ErrNotServing
	}
	if !s.elicitationAdvertised() {
		return ElicitationResult{}, ErrElicitationUnsupported
	}
	text := strings.TrimSpace(message)
	if text == "" {
		return ElicitationResult{}, errors.New("elicitation needs a message")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultElicitationTimeout)
		defer cancel()
	}
	params := map[string]any{"message": text}
	if schema != nil {
		params["requestedSchema"] = schema
	}
	var result ElicitationResult
	if err := s.Request(ctx, methodElicitationCreate, params, &result); err != nil {
		return ElicitationResult{}, err
	}
	switch result.Action {
	case ElicitationActionAccept, ElicitationActionDecline, ElicitationActionCancel:
	default:
		// An action this package does not know is a client bug. Refusing it
		// keeps a caller from switching on a value that matches no branch and
		// silently treating it as neither accept nor decline.
		return ElicitationResult{}, fmt.Errorf("elicitation answered with an unknown action %q", result.Action)
	}
	return result, nil
}

// elicitationAdvertised reports whether the last initialize named the
// elicitation capability in the client's capabilities block. The zero value is
// false: a client that never initialized, or never said so, is not assumed to
// support a method it never claimed.
func (s *Server) elicitationAdvertised() bool {
	if s == nil {
		return false
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.clientElicitation
}

// clientAdvertises reports whether a capabilities block names a capability
// with a non-null value. Presence is what counts: the spec's capability values
// are objects that may be empty, so `"elicitation": {}` is an advertisement
// and `"elicitation": null` is not.
func clientAdvertises(capabilities map[string]any, name string) bool {
	if capabilities == nil {
		return false
	}
	value, ok := capabilities[name]
	return ok && value != nil
}
