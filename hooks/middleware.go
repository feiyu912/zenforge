package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/tool"
)

// ErrBlocked reports a tool call refused by a hook.
var ErrBlocked = fmt.Errorf("blocked by a hook")

// Middleware runs PreToolUse hooks before a call and PostToolUse hooks
// after it. A PreToolUse block returns ErrBlocked without invoking the
// tool, and a hook that rewrites the arguments replaces them for the call.
// PostToolUse context is appended to the result's metadata so the runtime
// can surface it, and a PostToolUse block turns a successful call into a
// refusal, which is how a hook says "the result is not acceptable".
//
// Note the deliberate asymmetry: a hook failure is reported in the result
// metadata and does not block, unless that hook set failClosed. Failing
// closed by default would turn a broken hook script into a dead agent.
func Middleware(engine *Engine, sessionID string) tool.Middleware {
	return func(next tool.Invoker) tool.Invoker {
		return tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if engine == nil {
				return next.Invoke(ctx, call)
			}
			arguments := map[string]any{}
			if len(call.Arguments) > 0 {
				// A malformed argument payload is the tool's problem, not the
				// hook's: hand the raw text over as-is.
				_ = json.Unmarshal(call.Arguments, &arguments)
			}
			pre, err := engine.Run(ctx, Request{
				Event:     EventPreToolUse,
				SessionID: sessionID,
				RunID:     call.RunID,
				ToolName:  call.Name,
				ToolInput: arguments,
			})
			if err != nil {
				return tool.Result{}, err
			}
			if pre.Blocked {
				return blockedResult(call, pre), fmt.Errorf("%w: %s", ErrBlocked, pre.BlockMessage())
			}
			if len(pre.UpdatedInput) > 0 {
				if encoded, err := json.Marshal(pre.UpdatedInput); err == nil {
					call.Arguments = encoded
				}
			}
			result, callErr := next.Invoke(ctx, call)

			post, err := engine.Run(ctx, Request{
				Event:      EventPostToolUse,
				SessionID:  sessionID,
				RunID:      call.RunID,
				ToolName:   call.Name,
				ToolInput:  arguments,
				ToolOutput: result.Output,
				ToolError:  errorText(callErr, result),
			})
			if err != nil {
				return result, err
			}
			result = annotate(result, pre, post)
			if post.Blocked {
				return result, fmt.Errorf("%w: %s", ErrBlocked, post.BlockMessage())
			}
			return result, callErr
		})
	}
}

// blockedResult is the structured outcome of a refusal.
func blockedResult(call tool.Call, outcome Outcome) tool.Result {
	return tool.Result{
		Error:    fmt.Sprintf("%s: %s", ErrBlocked, outcome.BlockMessage()),
		ExitCode: 1,
		Structured: map[string]any{
			"blocked": true,
			"reason":  outcome.BlockMessage(),
		},
		Metadata: map[string]any{
			"code":         "HOOK_BLOCKED",
			"hookEvent":    string(outcome.Event),
			"hookFeedback": outcome.Summary(),
			"tool":         call.Name,
		},
	}
}

// annotate folds hook feedback into the tool result.
func annotate(result tool.Result, outcomes ...Outcome) tool.Result {
	var feedback, additionalContext, systemMessage string
	blocked := false
	for _, outcome := range outcomes {
		if summary := strings.TrimSpace(outcome.Summary()); summary != "" {
			if feedback != "" {
				feedback += "\n"
			}
			feedback += summary
		}
		if outcome.AdditionalContext != "" {
			if additionalContext != "" {
				additionalContext += "\n"
			}
			additionalContext += outcome.AdditionalContext
		}
		if outcome.SystemMessage != "" {
			systemMessage = outcome.SystemMessage
		}
		blocked = blocked || outcome.Blocked
	}
	if feedback == "" && additionalContext == "" && systemMessage == "" {
		return result
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	if feedback != "" {
		result.Metadata["hookFeedback"] = feedback
	}
	if additionalContext != "" {
		result.Metadata["hookContext"] = additionalContext
	}
	if systemMessage != "" {
		result.Metadata["hookSystemMessage"] = systemMessage
	}
	result.Metadata["hookBlocked"] = blocked
	return result
}

// errorText is the tool's error, preferring the explicit error and falling
// back to a structured result's own error field.
func errorText(err error, result tool.Result) string {
	if err != nil {
		return err.Error()
	}
	return result.Error
}
