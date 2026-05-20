package zenforge

import (
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/trace"
	"github.com/feiyu912/zenforge/workspace"
)

type PlanningMode string

const (
	PlanningDisabled PlanningMode = "disabled"
	PlanningEnabled  PlanningMode = "enabled"
)

type SubAgentMode string

const (
	SubAgentsDisabled SubAgentMode = "disabled"
	SubAgentsEnabled  SubAgentMode = "enabled"
)

// Config describes the default high-level ZenForge agent.
type Config struct {
	Model        model.Model
	Instructions string
	Tools        []tool.Tool
	Workspace    workspace.Workspace
	Checkpoints  checkpoint.Store
	Trace        trace.Sink
	Planning     PlanningMode
	SubAgents    SubAgentMode
}

// Tool is re-exported for the high-level API.
type Tool = tool.Tool

// Model is re-exported for the high-level API.
type Model = model.Model

