package harness

import (
	"encoding/json"
	"time"
)

const RunStateVersion = "zenforge.run_state.v1"

type RunPhase string

const (
	RunPhaseCreated    RunPhase = "created"
	RunPhaseModel      RunPhase = "model"
	RunPhaseTool       RunPhase = "tool"
	RunPhaseApproval   RunPhase = "approval"
	RunPhaseSubtask    RunPhase = "subtask"
	RunPhaseFinalizing RunPhase = "finalizing"
	RunPhaseCompleted  RunPhase = "completed"
	RunPhaseFailed     RunPhase = "failed"
	RunPhaseCancelled  RunPhase = "cancelled"
)

type RunState struct {
	Version     string          `json:"version"`
	RunID       string          `json:"runId"`
	ParentRunID string          `json:"parentRunId,omitempty"`
	TaskID      string          `json:"taskId,omitempty"`
	Input       string          `json:"input"`
	Phase       RunPhase        `json:"phase"`
	Step        int             `json:"step"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	Messages    []MessageState  `json:"messages,omitempty"`
	Todos       []TodoState     `json:"todos,omitempty"`
	Tool        ToolState       `json:"tool,omitempty"`
	Approval    ApprovalState   `json:"approval,omitempty"`
	Subtasks    []SubtaskState  `json:"subtasks,omitempty"`
	Control     RunControlState `json:"control"`
	Usage       UsageState      `json:"usage,omitempty"`
	Workspace   WorkspaceState  `json:"workspace,omitempty"`
	Sandbox     SandboxState    `json:"sandbox,omitempty"`
	Model       ModelState      `json:"model,omitempty"`
	Meta        map[string]any  `json:"meta,omitempty"`
}

type MessageState struct {
	ID         string         `json:"id,omitempty"`
	Role       string         `json:"role"`
	Name       string         `json:"name,omitempty"`
	Content    string         `json:"content,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	ToolCalls  []ToolCallSpec `json:"toolCalls,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type ToolCallSpec struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type ToolState struct {
	Pending []ToolCallState  `json:"pending,omitempty"`
	Active  *ToolCallState   `json:"active,omitempty"`
	Last    *ToolResultState `json:"last,omitempty"`
}

type ToolCallState struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Status    ToolCallStatus  `json:"status"`
	StartedAt *time.Time      `json:"startedAt,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
}

type ToolCallStatus string

const (
	ToolCallPending ToolCallStatus = "pending"
	ToolCallRunning ToolCallStatus = "running"
	ToolCallDone    ToolCallStatus = "done"
	ToolCallFailed  ToolCallStatus = "failed"
	ToolCallSkipped ToolCallStatus = "skipped"
)

type ToolResultState struct {
	ToolCallID string         `json:"toolCallId"`
	Output     string         `json:"output,omitempty"`
	Structured map[string]any `json:"structured,omitempty"`
	Error      string         `json:"error,omitempty"`
	ExitCode   int            `json:"exitCode"`
}

type TodoState struct {
	ID      string     `json:"id"`
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoDone       TodoStatus = "done"
	TodoFailed     TodoStatus = "failed"
	TodoCancelled  TodoStatus = "cancelled"
)

type ApprovalState struct {
	Waiting  *ApprovalRequestState   `json:"waiting,omitempty"`
	Resolved []ApprovalDecisionState `json:"resolved,omitempty"`
	Grants   []ApprovalGrantState    `json:"grants,omitempty"`
}

type ApprovalRequestState struct {
	ID          string         `json:"id"`
	RunID       string         `json:"runId,omitempty"`
	ToolCallID  string         `json:"toolCallId,omitempty"`
	ToolName    string         `json:"toolName,omitempty"`
	Operation   string         `json:"operation,omitempty"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Risk        string         `json:"risk,omitempty"`
	Options     []string       `json:"options,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
}

type ApprovalDecisionState struct {
	RequestID string         `json:"requestId"`
	Action    string         `json:"action"`
	Reason    string         `json:"reason,omitempty"`
	Scope     string         `json:"scope,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	DecidedAt time.Time      `json:"decidedAt"`
}

type ApprovalGrantState struct {
	RequestID   string    `json:"requestId"`
	Action      string    `json:"action"`
	Scope       string    `json:"scope"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	RuleKey     string    `json:"ruleKey,omitempty"`
	GrantedAt   time.Time `json:"grantedAt"`
}

type SubtaskState struct {
	ID        string         `json:"id"`
	ParentID  string         `json:"parentId,omitempty"`
	AgentName string         `json:"agentName"`
	Input     string         `json:"input"`
	Status    SubtaskStatus  `json:"status"`
	RunID     string         `json:"runId,omitempty"`
	Output    string         `json:"output,omitempty"`
	Error     string         `json:"error,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type SubtaskStatus string

const (
	SubtaskPending   SubtaskStatus = "pending"
	SubtaskRunning   SubtaskStatus = "running"
	SubtaskCompleted SubtaskStatus = "completed"
	SubtaskFailed    SubtaskStatus = "failed"
	SubtaskCancelled SubtaskStatus = "cancelled"
)

type RunControlState struct {
	Status      RunStatus    `json:"status"`
	Interrupt   bool         `json:"interrupt,omitempty"`
	Steers      []SteerState `json:"steers,omitempty"`
	AwaitingIDs []string     `json:"awaitingIds,omitempty"`
}

type RunStatus string

const (
	RunStatusIdle           RunStatus = "IDLE"
	RunStatusRunning        RunStatus = "RUNNING"
	RunStatusModelStreaming RunStatus = "MODEL_STREAMING"
	RunStatusToolExecuting  RunStatus = "TOOL_EXECUTING"
	RunStatusWaitingSubmit  RunStatus = "WAITING_SUBMIT"
	RunStatusCompleted      RunStatus = "COMPLETED"
	RunStatusCancelled      RunStatus = "CANCELLED"
	RunStatusFailed         RunStatus = "FAILED"
)

type SteerState struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type UsageState struct {
	InputTokens  int            `json:"inputTokens,omitempty"`
	OutputTokens int            `json:"outputTokens,omitempty"`
	TotalTokens  int            `json:"totalTokens,omitempty"`
	CostUSD      float64        `json:"costUsd,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

type WorkspaceState struct {
	Root       string         `json:"root,omitempty"`
	SandboxID  string         `json:"sandboxId,omitempty"`
	DirtyPaths []string       `json:"dirtyPaths,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type SandboxState struct {
	SessionID     string         `json:"sessionId,omitempty"`
	EnvironmentID string         `json:"environmentId,omitempty"`
	WorkingDir    string         `json:"workingDir,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}

type ModelState struct {
	Provider string         `json:"provider,omitempty"`
	Name     string         `json:"name,omitempty"`
	Request  map[string]any `json:"request,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}
