// Package workflow runs a JavaScript orchestration script that fans work out
// across child agents.
//
// The package is the engine half of the reference's workflow capability: it
// owns script parsing, hook semantics, concurrency and total-agent caps,
// cancellation, and the materialization of the script's return value. It does
// not know how a child agent is built — that is a [Runner] the caller
// supplies — and it never touches the caller's event or checkpoint stores;
// progress reaches the caller through an optional [Observer].
//
// The script contract is the reference's: the script body runs inside an
// async function, so top-level `await` works and `return <value>` is the
// result. The body sees five hooks and one value:
//
//   - agent(prompt, opts?) — run one child agent and resolve to its final
//     text, or to the object it returned when `opts.schema` was given.
//     Resolves to null when the child does not complete. `opts` accepts
//     label/phase/schema/provider/model; any other key is refused loudly.
//   - pipeline(items, ...stages) — run each item through the stages
//     independently, with no barrier between stages. Each stage is called as
//     stage(previous, item, index).
//   - parallel(thunks) — run zero-argument functions concurrently and await
//     all of them.
//   - phase(title) — name the phase the following agent() calls belong to.
//   - log(message) — narrate progress.
//   - args — the JSON input the caller passed with the request.
//
// A child that does not complete, and an ordinary error thrown inside a
// stage, become a per-item null. Everything this package refuses — a misused
// hook, an unsupported option or schema, a tripped cap, cancellation — is
// fatal: it rejects the combinator's promise instead of nulling the item.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// ErrorCode is the machine-routable taxonomy for workflow failures. The codes
// are the reference's, plus SCRIPT_ERROR and SCRIPT_TIMEOUT: the reference
// reports a script-level failure as a stop reason with a free-form message,
// while this package always names an error.
type ErrorCode string

const (
	// CodeScriptParse means the script body did not compile.
	CodeScriptParse ErrorCode = "SCRIPT_PARSE"
	// CodeScriptError means the script threw while running.
	CodeScriptError ErrorCode = "SCRIPT_ERROR"
	// CodeScriptTimeout means the script's synchronous prefix outlived
	// Limits.SyncTimeout.
	CodeScriptTimeout ErrorCode = "SCRIPT_TIMEOUT"
	// CodeInvalidArgument means a hook was called with the wrong shape.
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	// CodeUnsupportedOption means agent() received an option outside the
	// supported set.
	CodeUnsupportedOption ErrorCode = "UNSUPPORTED_OPTION"
	// CodeUnsupportedSchema means agent() received a schema outside the
	// enforced subset (see [ValidateObjectSchema]).
	CodeUnsupportedSchema ErrorCode = "UNSUPPORTED_SCHEMA"
	// CodeAgentCap means the run reached Limits.MaxTotalAgents.
	CodeAgentCap ErrorCode = "AGENT_CAP"
	// CodeItemCap means a combinator received more items than
	// Limits.MaxItemsPerCall.
	CodeItemCap ErrorCode = "ITEM_CAP"
	// CodeAgentStart means a child agent could not be started.
	CodeAgentStart ErrorCode = "AGENT_START"
	// CodeAgentResult means a started child's outcome could not be observed.
	CodeAgentResult ErrorCode = "AGENT_RESULT"
	// CodeResultUnserializable means the script's return value is not plain
	// JSON data.
	CodeResultUnserializable ErrorCode = "RESULT_UNSERIALIZABLE"
	// CodeCancelled means the run was cancelled. It is fatal so that a
	// combinator never turns cancellation into a per-item null.
	CodeCancelled ErrorCode = "CANCELLED"
)

// knownErrorCodes is the closed taxonomy, used to keep a code a hook raised
// from being replaced by a generic failure on the way out.
var knownErrorCodes = map[ErrorCode]bool{
	CodeScriptParse: true, CodeScriptError: true, CodeScriptTimeout: true,
	CodeInvalidArgument: true, CodeUnsupportedOption: true, CodeUnsupportedSchema: true,
	CodeAgentCap: true, CodeItemCap: true, CodeAgentStart: true, CodeAgentResult: true,
	CodeResultUnserializable: true, CodeCancelled: true,
}

// Error is a workflow failure.
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Fatal reports whether a combinator must re-throw this error instead of
// mapping the item to null. Every error this package raises is fatal; the
// method exists so the discipline is explicit at the call sites that decide.
func (e *Error) Fatal() bool { return true }

// ErrorCodeOf returns the workflow error code carried by err, if any.
func ErrorCodeOf(err error) (ErrorCode, bool) {
	var workflowErr *Error
	if !errors.As(err, &workflowErr) {
		return "", false
	}
	return workflowErr.Code, true
}

func newError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Phase is one declared phase of a workflow. Declarations are documentation
// and progress metadata: the engine does not require phase() titles to match
// them, and an observer may use them to label what it is told.
type Phase struct {
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Meta is the workflow's identity block.
type Meta struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	WhenToUse   string  `json:"whenToUse,omitempty"`
	Phases      []Phase `json:"phases,omitempty"`
}

// Limits bounds one run. A zero field takes its default from
// [DefaultLimits].
type Limits struct {
	// MaxConcurrentAgents is how many children may run at once. Zero means
	// the default: the smaller of 16 and one less than the machine's CPU
	// count, never below one.
	MaxConcurrentAgents int
	// MaxTotalAgents is the runaway-loop backstop for one run.
	MaxTotalAgents int
	// MaxItemsPerCall bounds one parallel() or pipeline() call.
	MaxItemsPerCall int
	// SyncTimeout bounds the script's synchronous prefix: the part that runs
	// before it first yields. A script that spins without awaiting is
	// interrupted instead of hanging the run.
	SyncTimeout time.Duration
	// CancelGrace is how long a cancelled run waits for the script to settle
	// before it is reported as cancelled anyway. A script parked on a promise
	// no hook owns never settles; without this it would hold the run forever.
	CancelGrace time.Duration
}

// DefaultLimits returns the reference defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxConcurrentAgents: 0, // resolved per machine by resolveLimits
		MaxTotalAgents:      1000,
		MaxItemsPerCall:     4096,
		SyncTimeout:         5 * time.Second,
		CancelGrace:         5 * time.Second,
	}
}

// resolveLimits fills zero fields from the defaults and rejects values the
// engine cannot honor.
func resolveLimits(limits Limits) (Limits, error) {
	defaults := DefaultLimits()
	if limits.MaxConcurrentAgents < 0 {
		return Limits{}, newError(CodeInvalidArgument, "MaxConcurrentAgents must not be negative")
	}
	if limits.MaxConcurrentAgents == 0 {
		limits.MaxConcurrentAgents = defaultConcurrency()
	}
	if limits.MaxTotalAgents < 0 {
		return Limits{}, newError(CodeInvalidArgument, "MaxTotalAgents must not be negative")
	}
	if limits.MaxTotalAgents == 0 {
		limits.MaxTotalAgents = defaults.MaxTotalAgents
	}
	if limits.MaxItemsPerCall < 0 {
		return Limits{}, newError(CodeInvalidArgument, "MaxItemsPerCall must not be negative")
	}
	if limits.MaxItemsPerCall == 0 {
		limits.MaxItemsPerCall = defaults.MaxItemsPerCall
	}
	if limits.SyncTimeout < 0 || limits.CancelGrace < 0 {
		return Limits{}, newError(CodeInvalidArgument, "SyncTimeout and CancelGrace must not be negative")
	}
	if limits.SyncTimeout == 0 {
		limits.SyncTimeout = defaults.SyncTimeout
	}
	if limits.CancelGrace == 0 {
		limits.CancelGrace = defaults.CancelGrace
	}
	return limits, nil
}

// defaultConcurrency mirrors the reference: leave room for the host, and
// never fan out beyond 16 children at once.
func defaultConcurrency() int {
	cpus := runtime.NumCPU() - 1
	if cpus < 1 {
		cpus = 1
	}
	if cpus > 16 {
		cpus = 16
	}
	return cpus
}

// ChildRequest is one agent() call.
type ChildRequest struct {
	// Prompt is the task text the script passed.
	Prompt string
	// Schema is the object schema the script asked the child to satisfy, or
	// nil when it passed none. It is always within the supported subset.
	Schema json.RawMessage
	// Provider and Model override the child's target when the script named
	// one. An empty value means "the caller's default".
	Provider string
	Model    string
	// Label is the display label, defaulted from the prompt when the script
	// passed none.
	Label string
	// Phase is the phase the call belongs to: the script's most recent
	// phase() title, or the per-call override.
	Phase string
}

// ChildResult is one child's outcome.
type ChildResult struct {
	// Output is the child's final text, used when no schema was requested.
	Output string
	// Structured is the child's structured result, used when a schema was
	// requested. A nil value, or a value that is not valid JSON, means the
	// child did not satisfy the schema and the item resolves to null.
	Structured json.RawMessage
	// StopReason is how the child ended. Anything other than "completed" is
	// a per-item null. A child that ran and failed belongs here with a nil
	// error: an error is for an outcome that could not be observed at all,
	// which is fatal because the script cannot be told the truth about it.
	StopReason string
}

// StopReasonCompleted is the child stop reason that yields a value.
const StopReasonCompleted = "completed"

// Child is one started child agent. The engine holds a concurrency slot
// between StartChild and the child's Result, and always cancels the run
// context it handed to StartChild when the run ends.
type Child interface {
	// Result waits for the child to settle. An error means the outcome could
	// not be observed, which is fatal: the script cannot be told the truth
	// about that item, so it must not be told a null either.
	Result() (ChildResult, error)
}

// Runner starts one child agent. StartChild must return promptly; the engine
// launches it on its own goroutine, so a Runner that blocks here serializes
// the run instead of fanning it out.
type Runner interface {
	StartChild(ctx context.Context, req ChildRequest) (Child, error)
}

// StartFunc adapts a plain function into a [Runner]. The error it returns is
// reported as an AGENT_RESULT failure, because a single call cannot say
// whether the child failed to start or failed to be observed.
func StartFunc(run func(ctx context.Context, req ChildRequest) (ChildResult, error)) Runner {
	return startFunc(run)
}

type startFunc func(ctx context.Context, req ChildRequest) (ChildResult, error)

func (f startFunc) StartChild(ctx context.Context, req ChildRequest) (Child, error) {
	return &funcChild{run: f, ctx: ctx, req: req}, nil
}

type funcChild struct {
	run func(ctx context.Context, req ChildRequest) (ChildResult, error)
	ctx context.Context
	req ChildRequest
}

func (c *funcChild) Result() (ChildResult, error) {
	return c.run(c.ctx, c.req)
}

// AgentInfo identifies one child to an [Observer].
type AgentInfo struct {
	// Seq is the 1-based order of the agent() call in this run.
	Seq int
	// Label is the display label: the script's own, or the prompt's first
	// line truncated to 48 characters.
	Label string
	// Phase is the phase the call belongs to, empty when the script never
	// named one.
	Phase string
}

// Observer reports progress. Every method may be called from the goroutine
// running [Engine.Run] and must not block it; a nil observer is valid.
type Observer interface {
	// WorkflowPhase reports a phase() call.
	WorkflowPhase(title string)
	// WorkflowLog reports a log() call.
	WorkflowLog(message string)
	// WorkflowAgentStart reports a child that has been started.
	WorkflowAgentStart(info AgentInfo)
	// WorkflowAgentEnd reports a child that settled. The outcome is
	// "completed", "failed", or "cancelled".
	WorkflowAgentEnd(info AgentInfo, outcome string)
}

// Observer outcomes.
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
)

// StopReason is how a run ended.
type StopReason string

// Run outcomes.
const (
	// StopCompleted means the script returned a value.
	StopCompleted StopReason = "completed"
	// StopCancelled means the run was cancelled.
	StopCancelled StopReason = "cancelled"
	// StopError means the script threw, or a fatal hook misuse killed it.
	StopError StopReason = "error"
)

// Request is one workflow run.
type Request struct {
	// Meta is the workflow identity block. Name and Description are
	// required.
	Meta Meta
	// Script is the plain-JavaScript body, without a meta statement.
	Script string
	// Args is the JSON value exposed to the script as `args`, or nil.
	Args json.RawMessage
	// Observer receives progress, or nil.
	Observer Observer
}

// Result is one run's outcome. It is populated for every stop reason, so a
// caller reporting a failure still knows how many children were started and
// (for a completed run) what the script returned.
type Result struct {
	// Value is the script's return value as JSON, null when there is none.
	Value json.RawMessage
	// StopReason is how the run ended.
	StopReason StopReason
	// Error is the failure text for a non-completed run.
	Error string
	// AgentsStarted counts agent() calls, including children that did not
	// complete.
	AgentsStarted int
}

// Engine runs workflow scripts. An Engine holds only its configuration, so
// one Engine may serve concurrent runs as long as its Runner does.
type Engine struct {
	runner Runner
	limits Limits
}

// New builds an engine with the default limits.
func New(runner Runner) (*Engine, error) {
	return NewWithLimits(runner, Limits{})
}

// NewWithLimits builds an engine, filling zero limit fields from
// [DefaultLimits] and rejecting the rest.
func NewWithLimits(runner Runner, limits Limits) (*Engine, error) {
	if runner == nil {
		return nil, newError(CodeInvalidArgument, "workflow engine requires a runner")
	}
	resolved, err := resolveLimits(limits)
	if err != nil {
		return nil, err
	}
	return &Engine{runner: runner, limits: resolved}, nil
}

// Limits returns the engine's resolved limits.
func (e *Engine) Limits() Limits { return e.limits }

// Run executes one script to settlement.
//
// The returned error is nil exactly when the script completed: a run that was
// cancelled, that threw, or that died of a hook misuse returns a *[Error]
// alongside the populated [Result], so a caller never has to choose between
// the reason and the run's bookkeeping.
func (e *Engine) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Meta.Name == "" {
		return Result{}, newError(CodeInvalidArgument, "workflow meta.name is required")
	}
	if req.Meta.Description == "" {
		return Result{}, newError(CodeInvalidArgument, "workflow meta.description is required")
	}
	if len(req.Args) > 0 && !json.Valid(req.Args) {
		return Result{}, newError(CodeInvalidArgument, "workflow args must be valid JSON")
	}
	if err := ctx.Err(); err != nil {
		return Result{StopReason: StopCancelled, Error: err.Error()}, newError(CodeCancelled, "workflow run cancelled: %v", err)
	}
	execution, err := newExecution(e, req, ctx)
	if err != nil {
		return Result{}, err
	}
	return execution.run()
}
