package zenforge

import (
	"context"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/compaction"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/hooks"
	"github.com/feiyu912/zenforge/instructions"
	"github.com/feiyu912/zenforge/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/modelretry"
	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/review"
	"github.com/feiyu912/zenforge/skill"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	"github.com/feiyu912/zenforge/trace"
	"github.com/feiyu912/zenforge/workspace"
)

type PlanningMode string

const (
	PlanningDisabled    PlanningMode = "disabled"
	PlanningEnabled     PlanningMode = "enabled"
	PlanningPlanExecute PlanningMode = "plan_execute"
)

// AgentMode selects a platform-compatible execution preset.
type AgentMode string

const (
	ModeReact       AgentMode = "react"
	ModeOneshot     AgentMode = "oneshot"
	ModePlanExecute AgentMode = "plan_execute"
)

type SubAgentMode string

const (
	SubAgentsDisabled SubAgentMode = "disabled"
	SubAgentsEnabled  SubAgentMode = "enabled"
)

// EventStore is implemented by eventlog stores without forcing the root
// package to import the eventlog package and create an import cycle.
type EventStore interface {
	Append(ctx context.Context, event Event) error
	Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]Event, error)
	LatestSeq(ctx context.Context, runID string) (int64, error)
}

// Config describes the default high-level ZenForge agent.
type Config struct {
	Model        model.Model
	Instructions string
	Skills       *skill.Bundle
	// Hooks runs user-configured commands at lifecycle points: SessionStart
	// and UserPromptSubmit once at run start (their context is frozen into
	// durable Meta), and Stop before the run finishes, where a refusal sends
	// the agent back to work with the hook's reason as its next
	// instruction. PreToolUse/PostToolUse are wired as tool middleware by
	// the caller. Optional.
	Hooks *hooks.Engine
	// Memory injects durable cross-run learnings into the system prompt and
	// records new ones when a run finishes. The summary is frozen into
	// durable Meta at run start, so a resume replays what the run saw.
	// Optional; without it no memory work happens.
	Memory memory.Provider
	// Review runs an independent reviewer over the finished run. In report
	// mode its verdict is recorded; in enforce mode a request-changes
	// verdict sends the agent back to work with the findings as its next
	// instruction, sharing the Stop hook's bounded refusals. Optional.
	Review                *review.Guardian
	Tools                 []tool.Tool
	ToolInvoker           tool.Invoker
	ToolRuntime           []tool.Middleware
	ToolArgumentRedaction []string
	Approval              approval.Broker
	ApprovalGrants        approval.GrantStore
	ApprovalNamespace     approval.Namespace
	ApprovalGrantTTL      time.Duration
	Todos                 planner.Manager
	SubAgentSpecs         []subagent.SubAgentSpec
	SubAgentRegistry      subagent.Registry
	SubAgentOrchestrator  subagent.Orchestrator
	SubAgentRunner        subagent.Runner
	SubAgentOptions       subagent.Options
	Workspace             workspace.Workspace
	// TurnDiffs receives per-mutation content captures from the
	// workspace Write/Edit tools; the agent drains it at each turn
	// boundary and emits turn.diff events with unified diffs (codex
	// TurnDiff parity). Optional.
	TurnDiffs         *workspacetools.TurnDiffStore
	Events            EventStore
	Checkpoints       checkpoint.Store
	RunController     *harness.RunController
	Trace             trace.Sink
	Compaction        *compaction.Config
	Retry             *modelretry.Config
	StreamIdleTimeout time.Duration
	InstructionFiles  *instructions.Config
	// SessionTitle sets an explicit run title; when empty the first
	// words of the task input become a deterministic fallback title.
	// Titles are log-only metadata (session.title event + run-state
	// meta), never part of the model surface.
	SessionTitle string
	// PersonaPrefix and PersonaSuffix wrap the system prompt around
	// first-party guidance (DSH persona slots). Both support strict
	// {{variable}} references resolved from PromptVariables plus the
	// built-in `workspace` and `platform` values; an unknown or
	// malformed reference fails the run at its next model boundary.
	PersonaPrefix string
	PersonaSuffix string
	// PromptVariables are host-supplied interpolation values for
	// PersonaPrefix and PersonaSuffix. Host values override the
	// built-in `workspace` and `platform`.
	PromptVariables map[string]string
	// OutputSchema asks the provider to constrain the final response to
	// a JSON Schema (codex exec --output-schema). Adapters that cannot
	// enforce one fail the request with
	// model.ErrUnsupportedOutputSchema.
	OutputSchema map[string]any
	// OutputSchemaName labels the schema; empty selects
	// model.DefaultOutputSchemaName.
	OutputSchemaName string
	// OutputSchemaStrict requests strict provider validation. Nil means
	// strict, matching the reference default.
	OutputSchemaStrict *bool
	WorkingDir         string
	EnvironmentContext bool
	MaxSteps           int
	Mode               AgentMode
	// PlanMode starts the run in the read-only planning phase: mutating
	// tools are refused until exit_plan_mode is approved, after which the
	// run continues in Mode. The phase lives in run state, so a resumed
	// run keeps whatever phase it reached.
	PlanMode  bool
	Planning  PlanningMode
	SubAgents SubAgentMode
}

// Tool is re-exported for the high-level API.
type Tool = tool.Tool

// Model is re-exported for the high-level API.
type Model = model.Model

// SubAgentSpec is re-exported for configuring delegated child agents.
type SubAgentSpec = subagent.SubAgentSpec

// SubAgentOptions controls host-owned task limits and child orchestration.
type SubAgentOptions = subagent.Options
