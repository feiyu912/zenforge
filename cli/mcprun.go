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
		// a prompt locally would consume the next request. `prompt` is
		// therefore answered by the MCP client (it advertises elicitation)
		// or downgraded to a refusal, and the refusal names what the
		// operator has to change.
		reason := "this MCP server serves runs without an operator, so it cannot prompt for approval; start it with --approve always to allow tools that need one"
		if served.approve == "never" {
			// `never` refuses without asking anyone: the operator has
			// already answered the mechanism itself, so eliciting would ask
			// the client to overrule the server's own configuration.
			reason = "approval disabled"
		} else {
			refusals.elicit = true
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
			// The server is read off the handler's context here, while the
			// request that carries it is still the run's parent: a detached
			// run deliberately outlives that request and switches to the
			// server's own context, so the reference has to be taken now or
			// the detached run could never ask the client for approval. A
			// handler called without a server (the tests that invoke it
			// directly) gets nil, which is the fallback refusal.
			server := mcp.ServerFrom(ctx)
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
				state := runServedTask(runCtx, agent, refusals, server, runID, false, timeout, prompt)
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
				registry.finish(runServedTask(detachedCtx, agent, refusals, server, runID, true, timeout, prompt))
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
func runServedTask(ctx context.Context, agent *zenforge.Agent, refusals *runApprovalRecorder, server *mcp.Server, runID string, detached bool, timeout time.Duration, prompt string) servedRunState {
	refusals.beginRun(server)
	// A detached run names itself before it starts, so the caller can be told
	// which run it is holding; a blocking one lets the agent choose.
	task := zenforge.Task{RunID: runID, Input: prompt}
	// A client that attached a progress token gets one notification per run
	// event, counted as the events arrive, with the event type as the message.
	// The reporter is a no-op when the caller asked for nothing -- and for a
	// detached run the context is the server's, not the request's, so its
	// no-op is also what keeps a finished call from writing progress for a
	// token nobody is waiting on any more. This hangs off Agent.Run's own
	// event loop: there is one pass over the stream, and it is the one that
	// produces the result, so progress can never change what the run returns.
	report := mcp.ProgressFrom(ctx)
	count := 0
	task.OnEvent = func(event zenforge.Event) {
		count++
		report(float64(count), string(event.Type))
	}
	result, runErr := agent.Run(ctx, task)
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
// refused and why, because a refusal is a property of how this server was
// started or of the client's own answer, and the remote agent may be able to
// change the first and knows the second.
func mcpRunCallResult(state servedRunState, refusals *runApprovalRecorder) mcp.CallResult {
	text := state.Output
	if state.Status != servedRunCompleted {
		text = state.Message
	}
	if state.Status == servedRunCompleted && len(state.Refused) > 0 {
		text = strings.TrimSpace(text) + refusals.summary(state.Refused)
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

// The explanations a denied tool call carries. The first is the fallback that
// existed before elicitation: it names what the operator has to change, because
// the refusal is a property of how this server was started. The next two are
// the client's own answers, and they are kept apart because "the operator said
// no" and "the client would not answer" call for different follow-ups -- one is
// a decision to respect, the other is a capability the caller may be able to
// fix. The last is an answer this server cannot read, which is denied for the
// same reason a missing answer is: consent has to be unambiguous.
const (
	servedRunOperatorDeclinedReason = "the MCP client's operator declined the approval request"
	servedRunClientDeclinedReason   = "the MCP client declined the approval request"
	servedRunUnreadableAnswerReason = "the MCP client answered the approval request with a value that is not a boolean, so the tool call was denied"
)

// runApprovalRecorder is the broker installed for a served run. It records
// which tool calls asked for approval and resolves them: a call is approved
// only when the MCP client says so, and is otherwise denied -- because the
// client declined, or because the server could not ask it. The point is not the
// denial (without an answer there is nothing else it could do) but telling the
// remote caller which part of its task was not performed and why.
type runApprovalRecorder struct {
	// reason is the refusal a denied tool call carries when the client cannot
	// be asked. It names what the operator has to change, and it is also the
	// reason for --approve never, which refuses without asking because the
	// operator has already answered the mechanism itself.
	reason string
	// elicit is true only for --approve prompt. `always` installs no recorder
	// at all, and `never` refuses without asking, so this flag is the one
	// place that decides whether the client is consulted.
	elicit bool
	// elicitTimeout bounds one question put to the client. The zero value
	// means mcp.DefaultElicitationTimeout; it is a field rather than a
	// constant in the call so the bound is a property of the recorder and a
	// test can pin the timeout's fallback without waiting out the default.
	elicitTimeout time.Duration

	mu sync.Mutex
	// server is the protocol layer the current run may elicit through. It is
	// fixed at the start of a run because a detached run's own context is the
	// server's, not the request's, and the request is the only carrier of the
	// server reference. A nil server means no protocol layer is behind the
	// run, which is exactly the pre-elicitation behaviour.
	server *mcp.Server
	// denials maps a refused tool name to the explanation the refusal carries.
	// It is a map rather than a list so two runs cannot accumulate each
	// other's refusals after a reset.
	denials map[string]string
}

func (b *runApprovalRecorder) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	name := approvalRequestName(req)
	if server := b.elicitationServer(); b.elicit && server != nil && server.ClientSupportsElicitation() {
		decision, denial, err := b.askClient(ctx, server, req, name)
		if err == nil {
			if denial != "" {
				b.record(name, denial)
			}
			return decision, nil
		}
		// A client that cannot answer -- it never advertised elicitation, let
		// the stream end, ran out of time, or answered with something this
		// server cannot read -- leaves the run exactly where it stood before
		// elicitation existed: the refusal below. Falling back rather than
		// failing the run is deliberate, because what could not proceed is the
		// tool call, not the run, and a client that cannot answer must not be
		// worse off than it was.
	}
	if name != "" {
		b.record(name, b.reason)
	}
	return approval.AlwaysDeny(b.reason).Request(ctx, req)
}

// askClient puts one approval question to the MCP client and turns its answer
// into a broker decision. A non-nil error means the client could not be asked
// or its answer could not be read, and the caller falls back to the refusal;
// denial is the explanation to record when the decision is a denial, and is
// empty for an approval.
func (b *runApprovalRecorder) askClient(ctx context.Context, server *mcp.Server, req approval.Request, name string) (approval.Decision, string, error) {
	// The wait is bounded by the default elicitation timeout even when the
	// run's own budget is larger: a person who is not at the keyboard must
	// not hold a served run -- detached or not -- for its whole allowance.
	// A shorter parent deadline still wins, because WithTimeout keeps the
	// earlier of the two.
	timeout := b.elicitTimeout
	if timeout <= 0 {
		timeout = mcp.DefaultElicitationTimeout
	}
	askCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := mcp.Elicit(askCtx, server, approvalElicitationMessage(req, name), approvalElicitationSchema())
	if err != nil {
		return approval.Decision{}, "", err
	}
	switch result.Action {
	case mcp.ElicitationActionAccept:
		approved, ok := result.Content["approve"].(bool)
		if !ok {
			// An answer that is not a boolean is not a decision this server
			// can read, and it is denied rather than treated as consent: a
			// mistyped or structured answer that happened to be truthy must
			// never be the reason a tool call ran.
			return denyApproval(ctx, req, servedRunUnreadableAnswerReason)
		}
		if !approved {
			return denyApproval(ctx, req, servedRunOperatorDeclinedReason)
		}
		return approveApproval(ctx, req)
	case mcp.ElicitationActionDecline, mcp.ElicitationActionCancel:
		// Both are the client's decision, not a failure to reach it: the run
		// is told the client declined, never that the server could not ask.
		return denyApproval(ctx, req, servedRunClientDeclinedReason)
	default:
		// Elicit already rejects an action outside the three it documents, so
		// this is a bug rather than an answer. It takes the fallback path like
		// any other unreadable answer instead of guessing a decision.
		return approval.Decision{}, "", fmt.Errorf("elicitation answered with an action this server cannot act on: %q", result.Action)
	}
}

// approveApproval builds the one-call approval an accepted elicitation grants.
// The scope is deliberately once: the client answered this call, not every call
// this run will make, and a grant that outlived the question would be a wider
// permission than the client gave.
func approveApproval(ctx context.Context, req approval.Request) (approval.Decision, string, error) {
	if err := ctx.Err(); err != nil {
		return approval.Decision{}, "", err
	}
	if err := req.Validate(); err != nil {
		return approval.Decision{}, "", err
	}
	return approval.Decision{
		RequestID: req.ID,
		Action:    approval.DecisionApprove,
		Scope:     approval.ScopeOnce,
		DecidedAt: time.Now().UTC(),
	}, "", nil
}

// denyApproval builds a denial with an explanation. It goes through the same
// AlwaysDeny broker a fallback refusal uses, so an invalid request still fails
// the call in exactly the way it did before, rather than being answered with a
// decision the agent would have to validate twice.
func denyApproval(ctx context.Context, req approval.Request, reason string) (approval.Decision, string, error) {
	decision, err := approval.AlwaysDeny(reason).Request(ctx, req)
	if err != nil {
		return approval.Decision{}, "", err
	}
	return decision, reason, nil
}

// approvalRequestName is the name a refusal is recorded under. It prefers the
// tool name and falls back to the operation, which is all some requests carry.
func approvalRequestName(req approval.Request) string {
	name := strings.TrimSpace(req.ToolName)
	if name == "" {
		name = strings.TrimSpace(req.Operation)
	}
	return name
}

// approvalElicitationMessage is what the client's operator reads. It names the
// tool so the decision is about something specific, and repeats the reason the
// harness gave when there is one -- an escalation's reason is the part that
// makes the difference between a routine command and a widened sandbox.
func approvalElicitationMessage(req approval.Request, name string) string {
	if name == "" {
		name = "a tool"
	} else {
		name = fmt.Sprintf("the %s tool", name)
	}
	message := fmt.Sprintf("The ZenForge run is asking to call %s, which needs approval.", name)
	if reason := strings.TrimSpace(req.Description); reason != "" {
		message += " The harness gave this reason: " + reason
	}
	return message + " Approve this call?"
}

// approvalElicitationSchema is the form the client fills in: one boolean, and
// it is required. A schema the client can answer without the field would let an
// accept arrive with no decision in it, which this server would then have to
// treat as a denial -- asking for the field makes that a client bug rather than
// a silent refusal.
func approvalElicitationSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"approve": map[string]any{
				"type":        "boolean",
				"description": "true to let this one tool call run, false to deny it",
			},
		},
		"required": []string{"approve"},
	}
}

// beginRun readies the recorder for one run: it fixes the server the run may
// elicit through and drops the previous run's denials. The server is captured
// here rather than looked up per request because the run's own context is not
// the request's for a detached run, and only the handler ever saw the request.
func (b *runApprovalRecorder) beginRun(server *mcp.Server) {
	b.mu.Lock()
	b.server = server
	b.denials = nil
	b.mu.Unlock()
}

// elicitationServer returns the server fixed for the current run.
func (b *runApprovalRecorder) elicitationServer() *mcp.Server {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.server
}

// record notes that a tool call was denied and why. A request with no name is
// not recorded: the refusal text names tools, and an unnamed entry would read
// as a tool the caller cannot identify.
func (b *runApprovalRecorder) record(name, reason string) {
	if name == "" {
		return
	}
	b.mu.Lock()
	if b.denials == nil {
		b.denials = map[string]string{}
	}
	b.denials[name] = reason
	b.mu.Unlock()
}

// refused lists the denied tool names in a stable order.
func (b *runApprovalRecorder) refused() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.denials))
	for name := range b.denials {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// summary renders the sentence appended to a completed run's answer. The
// all-refusal case keeps the exact wording callers already saw, so a client
// that cannot answer is told the same thing it was told before elicitation
// existed; any other case names what the client actually answered, because
// "the server cannot ask" would be false once the client has answered.
func (b *runApprovalRecorder) summary(names []string) string {
	if len(names) == 0 {
		return ""
	}
	b.mu.Lock()
	reasons := make(map[string]string, len(b.denials))
	for name, reason := range b.denials {
		reasons[name] = reason
	}
	b.mu.Unlock()

	fallbackOnly := true
	for _, name := range names {
		if reasons[name] != b.reason {
			fallbackOnly = false
			break
		}
	}
	if fallbackOnly {
		return fmt.Sprintf(
			"\n\n%d tool call(s) were refused because this server cannot ask a human to approve them: %s (%s)",
			len(names), strings.Join(names, ", "), b.reason,
		)
	}
	details := make([]string, 0, len(names))
	for _, name := range names {
		reason := reasons[name]
		if reason == "" {
			reason = b.reason
		}
		details = append(details, fmt.Sprintf("%s (%s)", name, reason))
	}
	return fmt.Sprintf("\n\n%d tool call(s) were refused: %s", len(names), strings.Join(details, ", "))
}
