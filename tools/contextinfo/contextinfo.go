// Package contextinfo provides the model-facing context-window
// introspection tool get_context_remaining, modeled on the codex tool
// of the same name. The agent injects the live remaining-token budget
// into tool-call metadata before dispatch; the tool reports it back to
// the model, or null when no context window is configured.
package contextinfo

import (
	"context"

	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

// Name is the tool name exposed to the model.
const Name = "get_context_remaining"

// TokensRemainingMetadataKey is the tool-call metadata key the agent
// populates with the current remaining context tokens (an int). Keeping
// this a plain metadata value lets the tool stay stateless and lets any
// other tool or middleware observe the same budget.
const TokensRemainingMetadataKey = "zenforge.tokensRemaining"

type input struct{}

type output struct {
	TokensLeft *int `json:"tokens_left"`
}

// New returns the get_context_remaining tool. Without an agent-injected
// metadata value the tool reports null, mirroring codex behavior when
// the token budget is unknown.
func New() (tool.Tool, error) {
	return tools.New(Name, "Get the remaining tokens in the current context window.", func(_ context.Context, _ input, call tool.Context) (output, error) {
		out := output{}
		if remaining, ok := tokensRemaining(call.Metadata); ok {
			out.TokensLeft = &remaining
		}
		return out, nil
	})
}

func tokensRemaining(metadata map[string]any) (int, bool) {
	value, ok := metadata[TokensRemainingMetadataKey]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}
