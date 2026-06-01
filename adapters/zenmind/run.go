package zenmind

import (
	"context"
	"fmt"

	"github.com/feiyu912/zenforge"
	memoryadapter "github.com/feiyu912/zenforge/adapters/memory"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/trace"
)

type CatalogAgent struct {
	Name         string         `json:"name"`
	Instructions string         `json:"instructions,omitempty"`
	Model        ModelRef       `json:"model,omitempty"`
	ToolNames    []string       `json:"toolNames,omitempty"`
	MaxSteps     int            `json:"maxSteps,omitempty"`
	Planning     string         `json:"planning,omitempty"`
	SubAgents    string         `json:"subAgents,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type ModelRef struct {
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
}

type Session struct {
	RunID          string         `json:"runId,omitempty"`
	Input          string         `json:"input"`
	UserID         string         `json:"userId,omitempty"`
	ConversationID string         `json:"conversationId,omitempty"`
	TeamID         string         `json:"teamId,omitempty"`
	Memory         []MemoryEntry  `json:"memory,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type MemoryEntry struct {
	ID    string         `json:"id,omitempty"`
	Text  string         `json:"text"`
	Score float64        `json:"score,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

type Runtime struct {
	Model                zenforge.Model
	Tools                []tool.Tool
	ToolInvoker          tool.Invoker
	ToolRuntime          []tool.Middleware
	Approval             approval.Broker
	Todos                planner.Manager
	SubAgentSpecs        []subagent.SubAgentSpec
	SubAgentRegistry     subagent.Registry
	SubAgentOrchestrator subagent.Orchestrator
	SubAgentRunner       subagent.Runner
	Events               zenforge.EventStore
	Checkpoints          checkpoint.Store
	Trace                trace.Sink
}

type RunConfig struct {
	Config zenforge.Config
	Task   zenforge.Task
}

func BuildRun(ctx context.Context, agent CatalogAgent, session Session, runtime Runtime) (RunConfig, error) {
	if err := ctx.Err(); err != nil {
		return RunConfig{}, err
	}
	if session.Input == "" {
		return RunConfig{}, fmt.Errorf("zenmind session input is required")
	}
	task := zenforge.Task{
		RunID: session.RunID,
		Input: session.Input,
		Meta:  taskMeta(agent, session),
	}
	if len(session.Memory) > 0 {
		augmented, _, err := memoryadapter.Augmenter{
			Store:      memoryadapter.NewStaticStore(memoryEntries(session.Memory)...),
			MaxEntries: len(session.Memory),
		}.AugmentTask(ctx, task)
		if err != nil {
			return RunConfig{}, err
		}
		task = augmented
	}
	return RunConfig{
		Config: zenforge.Config{
			Model:                runtime.Model,
			Instructions:         agent.Instructions,
			Tools:                filterTools(runtime.Tools, agent.ToolNames),
			ToolInvoker:          runtime.ToolInvoker,
			ToolRuntime:          runtime.ToolRuntime,
			Approval:             runtime.Approval,
			Todos:                runtime.Todos,
			SubAgentSpecs:        runtime.SubAgentSpecs,
			SubAgentRegistry:     runtime.SubAgentRegistry,
			SubAgentOrchestrator: runtime.SubAgentOrchestrator,
			SubAgentRunner:       runtime.SubAgentRunner,
			Events:               runtime.Events,
			Checkpoints:          runtime.Checkpoints,
			Trace:                runtime.Trace,
			MaxSteps:             agent.MaxSteps,
			Planning:             planningMode(agent.Planning),
			SubAgents:            subAgentMode(agent.SubAgents),
		},
		Task: task,
	}, nil
}

func taskMeta(agent CatalogAgent, session Session) map[string]any {
	meta := cloneMap(session.Metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["zenmind"] = map[string]any{
		"agent": map[string]any{
			"name":     agent.Name,
			"model":    agent.Model,
			"metadata": cloneMap(agent.Metadata),
		},
		"session": map[string]any{
			"userId":         session.UserID,
			"conversationId": session.ConversationID,
			"teamId":         session.TeamID,
		},
	}
	return meta
}

func filterTools(tools []tool.Tool, names []string) []tool.Tool {
	if len(names) == 0 {
		return append([]tool.Tool(nil), tools...)
	}
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	out := make([]tool.Tool, 0, len(tools))
	for _, current := range tools {
		if current != nil && allowed[current.Name()] {
			out = append(out, current)
		}
	}
	return out
}

func planningMode(value string) zenforge.PlanningMode {
	switch value {
	case "enabled", "true":
		return zenforge.PlanningEnabled
	case "disabled", "false":
		return zenforge.PlanningDisabled
	case "plan_execute", "plan-execute", "":
		return zenforge.PlanningPlanExecute
	default:
		return zenforge.PlanningDisabled
	}
}

func subAgentMode(value string) zenforge.SubAgentMode {
	switch value {
	case "enabled", "true":
		return zenforge.SubAgentsEnabled
	default:
		return zenforge.SubAgentsDisabled
	}
}

func memoryEntries(entries []MemoryEntry) []memoryadapter.Entry {
	out := make([]memoryadapter.Entry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, memoryadapter.Entry{
			ID:    entry.ID,
			Text:  entry.Text,
			Score: entry.Score,
			Meta:  cloneMap(entry.Meta),
		})
	}
	return out
}
