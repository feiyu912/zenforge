// Package mcpsampling adapts MCP sampling to the ZenForge model interface.
//
// A served run normally calls a provider configured on the host that runs it.
// When the operator opts in (the MCP server's --sampling), the run instead
// asks the connected MCP client to run each model call, so the client's own
// model, credentials, and operator controls are what answer the run.
//
// Two properties of the protocol shape this adapter, and both are deliberate
// rather than oversights:
//
//   - sampling/createMessage is not a stream. The client answers once, so a
//     single answer is mapped onto the delta-and-done stream the harness
//     expects; the run renders the whole answer at once rather than token by
//     token.
//   - the request has no field for a tool catalog. Tool definitions are
//     therefore dropped: a sampled run reasons in text and cannot call the
//     server's tools, because the client's model was never told they exist.
//     A tool choice of "required" is refused outright, since silently
//     answering text where a tool call was demanded would be a lie about what
//     happened.
package mcpsampling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/feiyu912/zenforge/adapters/mcp"
	"github.com/feiyu912/zenforge/model"
)

// Model answers model calls by delegating them to an MCP client through
// [mcp.Server.Sample]. It implements [model.Model] so a run can be served by
// the client's model without any other part of the harness knowing.
type Model struct {
	// mu guards server. The server is bound per served run rather than at
	// construction -- the MCP server builds its run agent before any
	// connection exists -- so a bind can race the model calls of the run it
	// was made for the moment a second run is admitted. The run slot already
	// serializes runs, and the lock keeps that an invariant the race detector
	// can see rather than a property of the caller.
	mu     sync.Mutex
	server *mcp.Server
}

// New builds a model adapter over one MCP server. The server may be nil when
// it is not known yet; [Model.BindServer] supplies it before the first call.
func New(server *mcp.Server) *Model {
	return &Model{server: server}
}

// BindServer fixes the protocol layer this model asks. It exists because the
// order of construction is forced: the MCP server builds the agent that will
// serve runs, and only later, when a tools/call arrives, does it have the
// connection a sampling request is written on. The run handler binds the
// server it reads off its context before starting the run, which is the same
// fix as the approval recorder's.
func (m *Model) BindServer(server *mcp.Server) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.server = server
	m.mu.Unlock()
}

// boundServer returns the server currently bound to this model.
func (m *Model) boundServer() *mcp.Server {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.server
}

// Generate runs one model call and returns the client's complete answer. It is
// the non-streaming form of [Model.Stream], mirroring how the provider
// adapters build Generate on top of Stream.
func (m *Model) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	events, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	response := &model.Response{}
	var content strings.Builder
	for event := range events {
		if event.Error != nil {
			return nil, event.Error
		}
		if event.Delta != "" {
			content.WriteString(event.Delta)
		}
		if event.Message != nil {
			response.Message = *event.Message
		}
		if event.Usage.TotalTokens != 0 || event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			response.Usage = event.Usage
		}
	}
	if response.Message.Role == "" {
		response.Message.Role = "assistant"
	}
	if response.Message.Content == "" {
		response.Message.Content = content.String()
	}
	return response, nil
}

// Stream runs one model call through the client and maps its single answer
// onto the event stream the harness expects. The call is made before the
// channel is returned, so a client that refuses, times out, or is not
// connected surfaces as a model error from Stream rather than as a silent
// empty answer; a caller that prefers the error-channel form can read the
// error off the returned error instead.
//
// A request that demands a tool call cannot be honored -- sampling carries no
// tool catalog -- so it fails loudly. A request that merely offers tools
// succeeds with a text answer, which is what a client that was never told
// about them can actually produce.
func (m *Model) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	if model.HasOutputSchema(req) {
		// The client's model is asked for plain text; there is no field on
		// sampling/createMessage to constrain its answer, and pretending the
		// schema was applied would hand the caller a response that violates
		// it. The repository's rule is to fail rather than ignore a schema.
		return nil, fmt.Errorf("mcpsampling: %w", model.ErrUnsupportedOutputSchema)
	}
	if req.ToolChoice == model.ToolChoiceRequired {
		return nil, errors.New("mcpsampling: the MCP sampling protocol cannot require a tool call, so a required tool choice cannot be delegated")
	}
	server := m.boundServer()
	if server == nil {
		return nil, errors.New("mcpsampling: no MCP server is bound, so the client cannot be asked to run a model call")
	}
	request, err := samplingRequest(req)
	if err != nil {
		return nil, err
	}
	result, err := server.Sample(ctx, request)
	if err != nil {
		// The sentinel errors -- ErrNotServing, ErrSamplingUnsupported, a
		// deadline -- stay wrapped so a caller can still ask why, while the
		// model call fails as a model error rather than an empty answer a
		// run might treat as a finished turn.
		return nil, fmt.Errorf("mcpsampling: sampling request failed: %w", err)
	}
	message := model.Message{Role: "assistant", Content: result.Text()}

	// The channel is buffered for exactly the events written below and closed
	// before it is returned: the answer already exists, so there is no
	// producer goroutine to leak and no ordering left to get wrong.
	events := make(chan model.Event, 2)
	if message.Content != "" {
		events <- model.Event{Type: model.EventDelta, Delta: message.Content}
	}
	done := model.Event{Type: model.EventDone, Message: &message}
	if result.Model != "" {
		// The model that actually answered is reported on the done event so a
		// caller can record it; the harness reads no usage from sampling,
		// which the protocol does not report.
		done.Meta = map[string]any{"model": result.Model}
	}
	events <- done
	close(events)
	return events, nil
}

// samplingRequest maps a harness model request onto the protocol's request
// shape. System messages become the spec's single systemPrompt -- the
// protocol has no system role -- and every other message becomes a
// user/assistant message, since those are the only two roles sampling allows.
// Tool calls and images cannot be expressed in a sampling message and are
// dropped; under sampling the tools cannot run anyway, so the loss is the
// protocol's, not a silent truncation of an answer the model gave.
func samplingRequest(req model.Request) (mcp.SamplingRequest, error) {
	request := mcp.SamplingRequest{}
	system := make([]string, 0, 1)
	for _, message := range req.Messages {
		if message.Role == "system" {
			if text := strings.TrimSpace(message.Content); text != "" {
				system = append(system, text)
			}
			continue
		}
		role := "user"
		if message.Role == "assistant" {
			role = "assistant"
		}
		request.Messages = append(request.Messages, mcp.SamplingMessage{
			Role:    role,
			Content: mcp.Content{Type: "text", Text: message.Content},
		})
	}
	if len(request.Messages) == 0 {
		return mcp.SamplingRequest{}, errors.New("mcpsampling: the request has no message the sampling protocol can carry")
	}
	request.SystemPrompt = strings.Join(system, "\n\n")
	return request, nil
}
