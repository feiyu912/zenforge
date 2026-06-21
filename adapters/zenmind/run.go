package zenmind

import (
	"context"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge"
	memoryadapter "github.com/feiyu912/zenforge/adapters/memory"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/trace"
	"github.com/feiyu912/zenforge/workspace"
)

// CatalogAgent is the transport form of agent-platform's catalog.AgentDefinition.
// Deprecated aliases remain for callers using the original ZenMind adapter DTO.
type CatalogAgent struct {
	Key           string         `json:"key,omitempty"`
	AgentKey      string         `json:"agentKey,omitempty"`
	Name          string         `json:"name,omitempty"`
	Instructions  string         `json:"instructions,omitempty"`
	ModelKey      string         `json:"modelKey,omitempty"`
	Mode          string         `json:"mode,omitempty"`
	Tools         []string       `json:"tools,omitempty"`
	Skills        []string       `json:"skills,omitempty"`
	ContextTags   []string       `json:"contextTags,omitempty"`
	Budget        map[string]any `json:"budget,omitempty"`
	StageSettings map[string]any `json:"stageSettings,omitempty"`
	ToolOverrides map[string]any `json:"toolOverrides,omitempty"`
	Runtime       map[string]any `json:"runtime,omitempty"`
	Workspace     Workspace      `json:"workspace,omitempty"`
	HostAccess    HostAccess     `json:"hostAccess,omitempty"`
	ReactMaxSteps int            `json:"reactMaxSteps,omitempty"`

	Model     ModelRef       `json:"model,omitempty"`
	ToolNames []string       `json:"toolNames,omitempty"`
	MaxSteps  int            `json:"maxSteps,omitempty"`
	Planning  string         `json:"planning,omitempty"`
	SubAgents string         `json:"subAgents,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Workspace struct {
	Root string `json:"root,omitempty"`
}

type HostAccess struct {
	ReadRoots  []string `json:"readRoots,omitempty"`
	WriteRoots []string `json:"writeRoots,omitempty"`
}

type ModelRef struct {
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Session combines agent-platform's api.QueryRequest and contracts.QuerySession
// fields used at the ZenForge execution boundary.
type Session struct {
	RequestID       string           `json:"requestId,omitempty"`
	ChatID          string           `json:"chatId,omitempty"`
	RunID           string           `json:"runId,omitempty"`
	AgentKey        string           `json:"agentKey,omitempty"`
	ModelKey        string           `json:"modelKey,omitempty"`
	Mode            string           `json:"mode,omitempty"`
	PlanningMode    bool             `json:"planningMode,omitempty"`
	AccessLevel     string           `json:"accessLevel,omitempty"`
	HistoryMessages []map[string]any `json:"historyMessages,omitempty"`
	WorkspaceRoot   string           `json:"workspaceRoot,omitempty"`
	Message         string           `json:"message,omitempty"`

	Input          string         `json:"input,omitempty"`
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

type ModelResolver interface {
	ResolveModel(context.Context, string) (model.Model, error)
}

type ModelResolverFunc func(context.Context, string) (model.Model, error)

func (f ModelResolverFunc) ResolveModel(ctx context.Context, key string) (model.Model, error) {
	return f(ctx, key)
}

type Runtime struct {
	Model                zenforge.Model
	ModelResolver        ModelResolver
	Tools                []tool.Tool
	ToolInvoker          tool.Invoker
	ToolRuntime          []tool.Middleware
	Approval             approval.Broker
	Todos                planner.Manager
	SubAgentSpecs        []subagent.SubAgentSpec
	SubAgentRegistry     subagent.Registry
	SubAgentOrchestrator subagent.Orchestrator
	SubAgentRunner       subagent.Runner
	Workspace            workspace.Workspace
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
	input := firstNonBlank(session.Message, session.Input)
	if input == "" {
		return RunConfig{}, fmt.Errorf("zenmind session input is required")
	}

	modeValue := firstNonBlank(session.Mode, agent.Mode)
	mode, err := agentMode(modeValue)
	if err != nil {
		return RunConfig{}, err
	}
	modelKey := firstNonBlank(session.ModelKey, agent.ModelKey)
	resolvedModel, err := resolveModel(ctx, modelKey, runtime)
	if err != nil {
		return RunConfig{}, err
	}

	task := zenforge.Task{
		RunID: session.RunID,
		Input: input,
		Meta:  taskMeta(agent, session, modelKey, modeValue),
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
			Model:                resolvedModel,
			Instructions:         agent.Instructions,
			Tools:                filterTools(runtime.Tools, preferredStrings(agent.Tools, agent.ToolNames)),
			ToolInvoker:          runtime.ToolInvoker,
			ToolRuntime:          runtime.ToolRuntime,
			Approval:             runtime.Approval,
			Todos:                runtime.Todos,
			SubAgentSpecs:        runtime.SubAgentSpecs,
			SubAgentRegistry:     runtime.SubAgentRegistry,
			SubAgentOrchestrator: runtime.SubAgentOrchestrator,
			SubAgentRunner:       runtime.SubAgentRunner,
			Workspace:            runtime.Workspace,
			Events:               runtime.Events,
			Checkpoints:          runtime.Checkpoints,
			Trace:                runtime.Trace,
			MaxSteps:             maxSteps(agent),
			Mode:                 mode,
			Planning:             effectivePlanning(agent, session, mode),
			SubAgents:            subAgentMode(agent.SubAgents),
		},
		Task: task,
	}, nil
}

func resolveModel(ctx context.Context, key string, runtime Runtime) (model.Model, error) {
	if key == "" {
		return runtime.Model, nil
	}
	if runtime.ModelResolver == nil {
		return nil, fmt.Errorf("zenmind model %q requires a ModelResolver", key)
	}
	resolved, err := runtime.ModelResolver.ResolveModel(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("resolve zenmind model %q: %w", key, err)
	}
	if resolved == nil {
		return nil, fmt.Errorf("unknown zenmind model %q", key)
	}
	return resolved, nil
}

func taskMeta(agent CatalogAgent, session Session, modelKey, mode string) map[string]any {
	meta := cloneMap(session.Metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	agentKey := firstNonBlank(agent.Key, agent.AgentKey, agent.Name)
	chatID := firstNonBlank(session.ChatID, session.ConversationID)
	workspaceRoot := firstNonBlank(session.WorkspaceRoot, agent.Workspace.Root)
	meta["zenmind"] = map[string]any{
		"agent": map[string]any{
			"key":           agentKey,
			"name":          agent.Name,
			"modelKey":      modelKey,
			"model":         agent.Model,
			"mode":          mode,
			"skills":        cloneStrings(agent.Skills),
			"contextTags":   cloneStrings(agent.ContextTags),
			"budget":        cloneMap(agent.Budget),
			"stageSettings": cloneMap(agent.StageSettings),
			"toolOverrides": cloneMap(agent.ToolOverrides),
			"runtime":       cloneMap(agent.Runtime),
			"workspace":     agent.Workspace,
			"hostAccess":    agent.HostAccess,
			"metadata":      cloneMap(agent.Metadata),
		},
		"session": map[string]any{
			"requestId":       session.RequestID,
			"chatId":          chatID,
			"runId":           session.RunID,
			"agentKey":        firstNonBlank(session.AgentKey, agentKey),
			"mode":            mode,
			"planningMode":    session.PlanningMode,
			"accessLevel":     session.AccessLevel,
			"historyMessages": cloneMaps(session.HistoryMessages),
			"workspaceRoot":   workspaceRoot,
			"userId":          session.UserID,
			"conversationId":  session.ConversationID,
			"teamId":          session.TeamID,
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

func agentMode(value string) (zenforge.AgentMode, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "REACT":
		return zenforge.ModeReact, nil
	case "ONESHOT":
		return zenforge.ModeOneshot, nil
	case "PLAN_EXECUTE", "PLAN-EXECUTE":
		return zenforge.ModePlanExecute, nil
	default:
		return "", fmt.Errorf("unknown zenmind agent mode %q", value)
	}
}

func effectivePlanning(agent CatalogAgent, session Session, mode zenforge.AgentMode) zenforge.PlanningMode {
	if mode == zenforge.ModePlanExecute {
		return zenforge.PlanningPlanExecute
	}
	if session.PlanningMode {
		return zenforge.PlanningEnabled
	}
	if agent.Planning != "" {
		return planningMode(agent.Planning)
	}
	if mode != "" {
		return zenforge.PlanningDisabled
	}
	return planningMode("")
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

func maxSteps(agent CatalogAgent) int {
	if value, ok := numberAsInt(agent.Budget["maxSteps"]); ok && value > 0 {
		return value
	}
	if agent.ReactMaxSteps > 0 {
		return agent.ReactMaxSteps
	}
	return agent.MaxSteps
}

func numberAsInt(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), int64(int(value)) == value
	case uint:
		return int(value), uint(int(value)) == value
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		return int(value), uint32(int(value)) == value
	case uint64:
		return int(value), uint64(int(value)) == value
	case float64:
		return int(value), value == float64(int(value))
	default:
		return 0, false
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func preferredStrings(primary, alias []string) []string {
	if primary != nil {
		return primary
	}
	return alias
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func cloneMaps(values []map[string]any) []map[string]any {
	if values == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, cloneMap(value))
	}
	return out
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
