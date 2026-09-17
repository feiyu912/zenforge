package tool

import (
	"context"
	"encoding/json"
	"time"
)

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Call(ctx context.Context, input json.RawMessage, call Context) (Result, error)
}

// TimeoutDeclarer is implemented by tools that declare a cooperative
// per-call timeout budget, mirroring the DSH tool definition field
// `timeoutMs`. The budget is runtime metadata and is never sent to the
// model; schemas expose only name, description, and parameters. A tool
// that declares a budget promises to forward the call context to a
// cooperative implementation, so it can reach quiescence when the
// deadline fires.
type TimeoutDeclarer interface {
	TimeoutBudget() time.Duration
}

// DeferredTool marks a tool whose full definition stays out of the model
// surface until a tool search activates it, mirroring codex's
// `defer_loading` flag. Deferral is opt-in: every tool that does not
// implement this interface keeps its eager, always-visible schema.
type DeferredTool interface {
	DeferredLoading() bool
}

// IsDeferred reports whether a tool's definition is withheld until a
// search activates it.
func IsDeferred(t Tool) bool {
	deferred, ok := t.(DeferredTool)
	return ok && deferred != nil && deferred.DeferredLoading()
}

// TimeoutBudgetOf reports the declared cooperative budget for a tool;
// zero means the tool declares none.
func TimeoutBudgetOf(t Tool) time.Duration {
	declarer, ok := t.(TimeoutDeclarer)
	if !ok || declarer == nil {
		return 0
	}
	budget := declarer.TimeoutBudget()
	if budget < 0 {
		return 0
	}
	return budget
}

type Definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type Call struct {
	ID                   string          `json:"id"`
	RunID                string          `json:"runId"`
	Name                 string          `json:"name"`
	Arguments            json.RawMessage `json:"arguments"`
	Metadata             map[string]any  `json:"metadata,omitempty"`
	RedactedArgumentKeys []string        `json:"-"`
}

type Context struct {
	RunID      string
	Step       int
	ToolCallID string
	Deadline   time.Time
	Metadata   map[string]any
	Meta       map[string]any
}

type Result struct {
	Output     string `json:"output,omitempty"`
	Structured map[string]any
	Error      string         `json:"error,omitempty"`
	ExitCode   int            `json:"exitCode"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type Registry interface {
	Register(tool Tool) error
	Lookup(name string) (Tool, bool)
	Definitions() []Definition
}

type Invoker interface {
	Invoke(ctx context.Context, call Call) (Result, error)
}

type InvokerFunc func(ctx context.Context, call Call) (Result, error)

func (f InvokerFunc) Invoke(ctx context.Context, call Call) (Result, error) {
	return f(ctx, call)
}

type Middleware func(Invoker) Invoker
