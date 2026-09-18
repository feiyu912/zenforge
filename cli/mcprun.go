package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/adapters/mcp"
	"github.com/feiyu912/zenforge/approval"
)

// defaultMCPRunTimeout bounds one served run. A run holds the connection it
// was asked for until it finishes (the server answers requests in order), so
// an unbounded one could hold it for the life of the process.
const defaultMCPRunTimeout = 15 * time.Minute

// mcpRunToolName is the model-visible name of the run-starting tool.
const mcpRunToolName = "zenforge_run"

// newMCPRunTool builds the tool that starts a run for a remote caller.
//
// The agent is built here, before the protocol is served, for the same reason
// `zenforge run` builds it before the first model call: a configuration that
// cannot produce an agent must fail the command rather than the first
// request. The run itself is configured entirely by the operator's flags --
// workspace, tool set, sandbox, approval mode -- so the remote caller chooses
// only the task.
func newMCPRunTool(ctx context.Context, opts *options, ioStreams IO, timeout time.Duration, registry *servedRunRegistry) (mcp.ServerTool, error) {
	served := *opts
	refusals := &runApprovalRecorder{}
	if served.approve != "always" {
		// A served run has no operator at a keyboard, and the interactive
		// broker reads the same streams the protocol is spoken on: answering
		// a prompt would consume the next request. `prompt` is therefore
		// downgraded to a refusal rather than left to corrupt the stream,
		// and the refusal names what the operator has to change.
		reason := "this MCP server serves runs without an operator, so it cannot prompt for approval; start it with --approve always to allow tools that need one"
		if served.approve == "never" {
			reason = "approval disabled"
		}
		refusals.reason = reason
		served.approvalOverride = refusals
	}
	agent, err := buildAgent(ctx, &served, ioStreams)
	if err != nil {
		return mcp.ServerTool{}, err
	}
	// The command drains what buildAgent opened; the served copy's closers
	// belong to the same command, so they are adopted here or they leak.
	opts.closers = append(opts.closers, served.closers...)

	// `Serve` answers one message at a time, so this slot is idle on the stdio
	// path. `Handle` is exported, though, and a host that drives it from
	// several goroutines must not overlap two runs on one agent: the slot
	// serializes them, and a caller whose own deadline is shorter than the
	// wait hears that the server was busy instead of being queued silently.
	runSlot := make(chan struct{}, 1)

	return mcp.ServerTool{
		Name: mcpRunToolName,
		// The description states the policy, because the caller's model is
		// the one that decides whether to call this at all.
		Description: "Start a ZenForge run in the workspace this MCP server was configured with and return the run's final answer. The server operator must have enabled it with --allow-run. Only the task is chosen here: the workspace, tools, sandbox, and approval mode come from how the server was started.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The task for the run. It becomes the run's input message.",
				},
				"detach": map[string]any{
					"type":        "boolean",
					"description": "Return the run id as soon as the run has started, instead of waiting for its answer. The run keeps going on the server (bounded by the operator's --run-timeout) and its state is available through zenforge_run_status. Use it for work longer than a single call can hold.",
				},
			},
			"required": []string{"prompt"},
		},
		// Not read-only, and deliberately without a read-only hint: starting
		// a run can change files, so a conforming MCP client has to ask its
		// own operator before calling this. That is the client-side half of
		// the approval path; --allow-run is the server-side half.
		ReadOnly: false,
		Handler: func(ctx context.Context, arguments json.RawMessage) (mcp.CallResult, error) {
			prompt, detach, err := decodeMCPRunArguments(arguments)
			if err != nil {
				return mcp.CallResult{}, err
			}
			runID := zenforge.NewRunID()
			if !detach {
				select {
				case runSlot <- struct{}{}:
					defer func() { <-runSlot }()
				case <-ctx.Done():
					return mcp.CallResult{}, fmt.Errorf("another served run is in progress and this call was cancelled while waiting: %w", ctx.Err())
				}
				// The caller is waiting, so its own context bounds the run: a
				// cancelled request should stop the work it asked for — and
				// the operator's run timeout still bounds every served run.
				runCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				// A blocking run is registered too: it is still a run this
				// server owns, so it can be reported on while it runs and
				// stopped from another connection.
				if err := registry.start(runID, false, cancel); err != nil {
					return mcp.CallResult{}, err
				}
				state := runServedTask(runCtx, agent, refusals, runID, false, timeout, prompt)
				registry.finish(state)
				return mcpRunCallResult(state, refusals), nil
			}
			// A detached run outlives the call that asked for it, so it must
			// not be bound to that call's context: the server's own context,
			// with the operator's timeout, is what keeps it alive and what
			// eventually stops it.
			select {
			case runSlot <- struct{}{}:
			default:
				return mcp.CallResult{}, errors.New("another served run is in progress; ask for zenforge_run_status on it or retry when it finishes")
			}
			detachedCtx, cancel := context.WithTimeout(registry.context(), timeout)
			if err := registry.start(runID, true, cancel); err != nil {
				<-runSlot
				cancel()
				return mcp.CallResult{}, err
			}
			go func() {
				defer func() { <-runSlot }()
				defer cancel()
				registry.finish(runServedTask(detachedCtx, agent, refusals, runID, true, timeout, prompt))
			}()
			return mcp.CallResult{
				Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf(
					"run %s started and is running on the server; ask zenforge_run_status for its state (it is bounded by this server's run timeout)", runID)}},
				StructuredContent: map[string]any{
					"runId":    runID,
					"status":   servedRunRunning,
					"detached": true,
				},
			}, nil
		},
	}, nil
}

// decodeMCPRunArguments reads the run tool's arguments once, so the blocking
// and detached paths cannot disagree about what was asked for.
func decodeMCPRunArguments(arguments json.RawMessage) (string, bool, error) {
	var input struct {
		Prompt string `json:"prompt"`
		Detach bool   `json:"detach"`
	}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &input); err != nil {
			return "", false, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return "", false, errors.New("prompt is required")
	}
	return prompt, input.Detach, nil
}

// serveMCPRun runs one task and projects the outcome onto an MCP result.
//
// A tool call that the served run refused for lack of an approver is reported
// as part of the outcome, not as a failed call: the run finished, and its
// answer already reflects the refusal. A run that errored, timed out, or was
// cancelled is an `isError` result, and it still carries the run id so the
// caller can inspect what happened in the durable log.
func runServedTask(ctx context.Context, agent *zenforge.Agent, refusals *runApprovalRecorder, runID string, detached bool, timeout time.Duration, prompt string) servedRunState {
	refusals.reset()
	// A detached run names itself before it starts, so the caller can be told
	// which run it is holding; a blocking one lets the agent choose.
	result, runErr := agent.Run(ctx, zenforge.Task{RunID: runID, Input: prompt})
	if result == nil {
		result = &zenforge.Result{}
	}
	state := servedRunState{
		RunID:    result.RunID,
		Status:   servedRunCompleted,
		Output:   result.Output,
		Refused:  refusals.refused(),
		Detached: detached,
	}
	if runErr != nil {
		// How a cancellation surfaces depends on where it landed: a model call
		// reports the context error, while a checkpoint save that was already
		// in flight can return a store error that does not chain to
		// context.Canceled. The run's own context is therefore the authority
		// on why it stopped — if the bound was hit, the outcome is the bound,
		// whatever error the internals produced on the way out.
		switch {
		case errors.Is(runErr, approval.ErrRequired):
			state.Status = "awaiting-approval"
			state.Message = "the served run stopped while an approval request was open, which this server cannot answer"
		case errors.Is(runErr, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
			state.Status = "timeout"
			state.Message = fmt.Sprintf("the served run was cancelled after %s", timeout)
		case errors.Is(runErr, context.Canceled), ctx.Err() != nil:
			state.Status = "cancelled"
			state.Message = "the served run was cancelled"
		default:
			state.Status = "failed"
			state.Message = runErr.Error()
		}
		if state.RunID != "" {
			state.Message = fmt.Sprintf("run %s %s: %s", state.RunID, state.Status, state.Message)
		}
	}
	return state
}

// mcpRunCallResult projects a served run onto an MCP result.
//
// A run that refused a tool call for lack of an approver is a completed run,
// and its answer already reflects the refusal; the caller is told what was
// refused and what would change it, because the refusal is a property of how
// this server was started and the remote agent may be able to ask its operator
// to change that.
func mcpRunCallResult(state servedRunState, refusals *runApprovalRecorder) mcp.CallResult {
	text := state.Output
	if state.Status != servedRunCompleted {
		text = state.Message
	}
	if state.Status == servedRunCompleted && len(state.Refused) > 0 {
		text = strings.TrimSpace(text) + fmt.Sprintf(
			"\n\n%d tool call(s) were refused because this server cannot ask a human to approve them: %s (%s)",
			len(state.Refused), strings.Join(state.Refused, ", "), refusals.reason,
		)
	}
	structured := map[string]any{
		"runId":  state.RunID,
		"output": state.Output,
		"status": state.Status,
	}
	if len(state.Refused) > 0 {
		structured["refusedToolCalls"] = state.Refused
	}
	return mcp.CallResult{
		Content:           []mcp.Content{{Type: "text", Text: text}},
		StructuredContent: structured,
		// A failed run is a tool failure, and the structured outcome is kept
		// with it: the caller can still report which run it was and why it
		// ended. The server only synthesizes the error text when a handler
		// returns an error, so the flag is set here rather than delegated.
		IsError: state.Status != servedRunCompleted,
	}
}

// runApprovalRecorder is the broker installed for a served run. It records
// which tool calls asked for approval and refuses them: the point is not the
// refusal (there is nothing else it could do) but telling the remote caller
// which part of its task the server declined to perform.
type runApprovalRecorder struct {
	reason string

	mu           sync.Mutex
	refusedNames map[string]struct{}
}

func (b *runApprovalRecorder) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	name := strings.TrimSpace(req.ToolName)
	if name == "" {
		name = strings.TrimSpace(req.Operation)
	}
	if name != "" {
		b.mu.Lock()
		if b.refusedNames == nil {
			b.refusedNames = map[string]struct{}{}
		}
		b.refusedNames[name] = struct{}{}
		b.mu.Unlock()
	}
	return approval.AlwaysDeny(b.reason).Request(ctx, req)
}

func (b *runApprovalRecorder) reset() {
	b.mu.Lock()
	b.refusedNames = nil
	b.mu.Unlock()
}

func (b *runApprovalRecorder) refused() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.refusedNames))
	for name := range b.refusedNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
