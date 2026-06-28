package harness

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	RunStateVersion          = "zenforge.run_state.v1"
	ModelAttemptHistoryLimit = 64
)

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

// ValidateRunState validates the versioned fields used to dispatch a resumed run.
// Empty version and mode values are accepted for legacy checkpoints.
func ValidateRunState(state RunState) error {
	if state.Version != "" && state.Version != RunStateVersion {
		return fmt.Errorf("unsupported run state version %q", state.Version)
	}
	switch state.Phase {
	case RunPhaseCreated, RunPhaseModel, RunPhaseTool, RunPhaseApproval,
		RunPhaseSubtask, RunPhaseFinalizing, RunPhaseCompleted, RunPhaseFailed,
		RunPhaseCancelled:
	default:
		return fmt.Errorf("unsupported run phase %q", state.Phase)
	}
	switch state.Mode {
	case "", "react", "oneshot", "plan_execute":
	default:
		return fmt.Errorf("unsupported run mode %q", state.Mode)
	}
	attemptIDs := make(map[string]struct{}, len(state.Model.Attempts)+1)
	for i := range state.Model.Attempts {
		if err := validateModelAttempt(state.Model.Attempts[i], true, attemptIDs); err != nil {
			return fmt.Errorf("invalid model attempt history at index %d: %w", i, err)
		}
		if state.Model.Attempts[i].LogicalStep > state.Step {
			return fmt.Errorf("model attempt history at index %d is ahead of run step %d", i, state.Step)
		}
	}
	if state.Model.Active != nil {
		if err := validateModelAttempt(*state.Model.Active, false, attemptIDs); err != nil {
			return fmt.Errorf("invalid active model attempt: %w", err)
		}
		if state.Model.Active.LogicalStep != state.Step {
			return fmt.Errorf("active model attempt step %d does not match run step %d", state.Model.Active.LogicalStep, state.Step)
		}
	}
	if len(state.Model.Attempts) > ModelAttemptHistoryLimit {
		return fmt.Errorf("model attempt history exceeds limit %d", ModelAttemptHistoryLimit)
	}
	attemptsByID := make(map[string]ModelAttempt, len(state.Model.Attempts)+1)
	for _, attempt := range state.Model.Attempts {
		attemptsByID[attempt.ID] = attempt
	}
	if state.Model.Active != nil {
		attemptsByID[state.Model.Active.ID] = *state.Model.Active
	}
	activeAttemptID := ""
	if state.Model.Active != nil {
		activeAttemptID = state.Model.Active.ID
	}
	for _, attempt := range attemptsByID {
		if attempt.ReplacesID != "" {
			replaced, ok := attemptsByID[attempt.ReplacesID]
			if !ok {
				return fmt.Errorf("model attempt %q replaces missing attempt %q", attempt.ID, attempt.ReplacesID)
			}
			if replaced.ReplacementID != attempt.ID {
				return fmt.Errorf("model attempt %q has inconsistent replaces link", attempt.ID)
			}
		}
		if attempt.ReplacementID != "" {
			replacement, ok := attemptsByID[attempt.ReplacementID]
			if !ok {
				return fmt.Errorf("model attempt %q references missing replacement %q", attempt.ID, attempt.ReplacementID)
			}
			if replacement.ReplacesID != attempt.ID {
				return fmt.Errorf("model attempt %q has inconsistent replacement link", attempt.ID)
			}
		}
		if attempt.Status == ModelAttemptSuperseded && attempt.ID != activeAttemptID && attempt.ReplacementID == "" {
			return fmt.Errorf("historical superseded model attempt %q has no replacement", attempt.ID)
		}
	}
	return nil
}

func validateModelAttempt(attempt ModelAttempt, historical bool, ids map[string]struct{}) error {
	if attempt.ID == "" {
		return fmt.Errorf("id is required")
	}
	if _, exists := ids[attempt.ID]; exists {
		return fmt.Errorf("duplicate id %q", attempt.ID)
	}
	ids[attempt.ID] = struct{}{}
	if attempt.LogicalStep < 0 {
		return fmt.Errorf("logical step must be non-negative")
	}
	if attempt.StartedAt.IsZero() {
		return fmt.Errorf("startedAt is required")
	}
	switch attempt.Status {
	case ModelAttemptStarted, ModelAttemptStreaming:
		if historical {
			return fmt.Errorf("history contains nonterminal status %q", attempt.Status)
		}
		if attempt.CompletedAt != nil {
			return fmt.Errorf("status %q cannot have completedAt", attempt.Status)
		}
	case ModelAttemptInterrupted:
		if historical {
			return fmt.Errorf("history contains nonterminal status %q", attempt.Status)
		}
		if attempt.CompletedAt == nil {
			return fmt.Errorf("status %q requires completedAt", attempt.Status)
		}
	case ModelAttemptCommitted:
		if !historical {
			return fmt.Errorf("active attempt cannot have status %q", attempt.Status)
		}
		if attempt.CompletedAt == nil {
			return fmt.Errorf("status %q requires completedAt", attempt.Status)
		}
	case ModelAttemptSuperseded:
		if attempt.CompletedAt == nil {
			return fmt.Errorf("status %q requires completedAt", attempt.Status)
		}
	default:
		return fmt.Errorf("unsupported status %q", attempt.Status)
	}
	if attempt.CompletedAt != nil && attempt.CompletedAt.Before(attempt.StartedAt) {
		return fmt.Errorf("completedAt precedes startedAt")
	}
	return nil
}

type RunState struct {
	Version     string          `json:"version"`
	RunID       string          `json:"runId"`
	ParentRunID string          `json:"parentRunId,omitempty"`
	TaskID      string          `json:"taskId,omitempty"`
	Input       string          `json:"input"`
	Mode        string          `json:"mode,omitempty"`
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
	RunID         string         `json:"runId,omitempty"`
	SubtaskID     string         `json:"subtaskId,omitempty"`
	EnvironmentID string         `json:"environmentId,omitempty"`
	WorkingDir    string         `json:"workingDir,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}

type ModelState struct {
	Provider string         `json:"provider,omitempty"`
	Name     string         `json:"name,omitempty"`
	Request  map[string]any `json:"request,omitempty"`
	Active   *ModelAttempt  `json:"active,omitempty"`
	Attempts []ModelAttempt `json:"attempts,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

type ModelAttemptStatus string

const (
	ModelAttemptStarted     ModelAttemptStatus = "started"
	ModelAttemptStreaming   ModelAttemptStatus = "streaming"
	ModelAttemptCommitted   ModelAttemptStatus = "committed"
	ModelAttemptInterrupted ModelAttemptStatus = "interrupted"
	ModelAttemptSuperseded  ModelAttemptStatus = "superseded"
)

// ModelAttempt is durable observation state, not committed conversation state.
type ModelAttempt struct {
	ID             string             `json:"id"`
	LogicalStep    int                `json:"logicalStep"`
	Status         ModelAttemptStatus `json:"status"`
	TextDraft      string             `json:"textDraft,omitempty"`
	ToolCallsDraft []ToolCallSpec     `json:"toolCallsDraft,omitempty"`
	ChunkSeq       int64              `json:"chunkSeq,omitempty"`
	ObservedUsage  UsageState         `json:"observedUsage,omitempty"`
	StartedAt      time.Time          `json:"startedAt"`
	CompletedAt    *time.Time         `json:"completedAt,omitempty"`
	ReplacesID     string             `json:"replacesId,omitempty"`
	ReplacementID  string             `json:"replacementId,omitempty"`
}

// AppendAttempt retains a bounded, internally linked attempt history.
func (s *ModelState) AppendAttempt(attempt ModelAttempt) {
	s.Attempts = append(s.Attempts, attempt)
	if len(s.Attempts) <= ModelAttemptHistoryLimit {
		return
	}
	s.Attempts = append([]ModelAttempt(nil), s.Attempts[len(s.Attempts)-ModelAttemptHistoryLimit:]...)
	s.Attempts[0].ReplacesID = ""
}
