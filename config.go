package zenforge

import (
	"context"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
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
	Model                 model.Model
	Instructions          string
	Tools                 []tool.Tool
	ToolInvoker           tool.Invoker
	ToolRuntime           []tool.Middleware
	ToolArgumentRedaction []string
	Approval              approval.Broker
	Todos                 planner.Manager
	SubAgentSpecs         []subagent.SubAgentSpec
	SubAgentRegistry      subagent.Registry
	SubAgentOrchestrator  subagent.Orchestrator
	SubAgentRunner        subagent.Runner
	SubAgentOptions       subagent.Options
	Workspace             workspace.Workspace
	Events                EventStore
	Checkpoints           checkpoint.Store
	Trace                 trace.Sink
	MaxSteps              int
	Mode                  AgentMode
	Planning              PlanningMode
	SubAgents             SubAgentMode
}

// Tool is re-exported for the high-level API.
type Tool = tool.Tool

// Model is re-exported for the high-level API.
type Model = model.Model

// SubAgentSpec is re-exported for configuring delegated child agents.
type SubAgentSpec = subagent.SubAgentSpec

// SubAgentOptions controls host-owned task limits and child orchestration.
type SubAgentOptions = subagent.Options
