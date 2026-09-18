package mcp

import (
	"context"
	"errors"
	"strings"
	"time"
)

// methodSamplingCreateMessage is the client-directed method Sample sends. It
// names a *client* capability: this package never advertises it in
// initialize's capabilities block, because the block says what the server
// offers, and sampling is the opposite direction -- the server asking the
// client to run a model call it cannot run itself.
const methodSamplingCreateMessage = "sampling/createMessage"

// DefaultSamplingTimeout bounds a sample whose caller supplied no deadline. A
// sample is a whole model turn on the client's side and can take minutes, so
// the default is generous; a caller that knows its own budget should pass a
// context with a deadline and that budget wins. The bound exists so a client
// that never answers cannot hold a handler forever.
const DefaultSamplingTimeout = 5 * time.Minute

// DefaultSamplingMaxTokens is the maxTokens a request carries when the caller
// named none. The field is required by the spec, so some value has to be sent;
// this one is a conservative ceiling rather than an invitation to generate
// without limit, and a caller that needs more says so.
const DefaultSamplingMaxTokens = 8192

// ErrSamplingUnsupported means the client did not advertise the sampling
// capability during initialize. Sending the request anyway would either hang
// until the timeout or draw a method-not-found error, so Sample refuses up
// front and the caller can take its fallback path.
var ErrSamplingUnsupported = errors.New("mcp client did not advertise the sampling capability")

// SamplingMessage is one message a sampling request carries. The spec allows
// only the user and assistant roles here -- there is no system message and no
// tool message -- which is why an adapter that has those maps them onto these
// two.
type SamplingMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// SamplingRequest is what the server asks its client to run. Messages is the
// conversation, SystemPrompt is the optional instruction prefix the spec puts
// outside the message list, ModelHint is an optional client-model preference,
// and MaxTokens bounds the answer (DefaultSamplingMaxTokens when unset).
type SamplingRequest struct {
	Messages     []SamplingMessage
	SystemPrompt string
	ModelHint    string
	MaxTokens    int
}

// maxTokens resolves the bound the wire request carries.
func (r SamplingRequest) maxTokens() int {
	if r.MaxTokens > 0 {
		return r.MaxTokens
	}
	return DefaultSamplingMaxTokens
}

// SamplingResult is the client's answer. Role names the speaker the client
// chose (normally assistant), Content is the answer itself, Model is the model
// the client actually used -- which may differ from the hint it was given --
// and StopReason is the client's optional explanation of why it stopped.
type SamplingResult struct {
	Role       string  `json:"role"`
	Content    Content `json:"content"`
	Model      string  `json:"model"`
	StopReason string  `json:"stopReason,omitempty"`
}

// Text returns the answer's text. It is a method rather than a field so the
// wire shape stays the spec's content block, and a caller that needs to tell a
// non-text answer apart can still switch on Content.Type.
func (r SamplingResult) Text() string {
	return r.Content.Text
}

// Sample asks the client to run a model call on this server's behalf and
// returns its answer. It is the convenience layer over Request: it builds the
// `sampling/createMessage` params, applies DefaultSamplingTimeout when ctx has
// no deadline of its own, and decodes the `{role, content, model, stopReason}`
// result.
//
// It refuses before writing anything when the server has no stream or the
// client did not advertise sampling, so a caller always learns why it has to
// fall back instead of waiting on a request that cannot be answered. The
// request never carries a tool catalog: the spec has no field for one, and
// inventing one would be a private extension no other client reads.
func (s *Server) Sample(ctx context.Context, request SamplingRequest) (SamplingResult, error) {
	if s == nil {
		return SamplingResult{}, errors.New("mcp server is nil, so a sample cannot be sent")
	}
	// The stream check comes first: without one there is nowhere to send the
	// request at all, and "not serving" is the more fundamental reason.
	if !s.isServing() {
		return SamplingResult{}, ErrNotServing
	}
	if !s.ClientSupportsSampling() {
		return SamplingResult{}, ErrSamplingUnsupported
	}
	if len(request.Messages) == 0 {
		return SamplingResult{}, errors.New("sampling needs at least one message")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultSamplingTimeout)
		defer cancel()
	}
	params := map[string]any{
		"messages":  request.Messages,
		"maxTokens": request.maxTokens(),
	}
	if prompt := strings.TrimSpace(request.SystemPrompt); prompt != "" {
		params["systemPrompt"] = prompt
	}
	if hint := strings.TrimSpace(request.ModelHint); hint != "" {
		// The spec's preference shape is a list of hints; one name is the
		// whole of what this server has to say, and the client remains free
		// to use a different model, which is why the result names the one it
		// actually used.
		params["modelPreferences"] = map[string]any{
			"hints": []map[string]any{{"name": hint}},
		}
	}
	var result SamplingResult
	if err := s.Request(ctx, methodSamplingCreateMessage, params, &result); err != nil {
		return SamplingResult{}, err
	}
	return result, nil
}

// ClientSupportsSampling reports whether the last initialize named the
// sampling capability in the client's capabilities block. The zero value is
// false: a client that never initialized, or never said so, is not assumed to
// support a method it never claimed.
//
// It is exported because a caller that has to decide before it writes anything
// -- the MCP server deciding whether a served run can be answered at all, for
// example -- should be able to refuse with its own actionable message instead
// of sending a request and translating the refusal afterwards. The answer is
// the client's own declaration, so it changes only when a new initialize
// arrives; nothing this server advertises is affected.
func (s *Server) ClientSupportsSampling() bool {
	if s == nil {
		return false
	}
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	return s.clientSampling
}
