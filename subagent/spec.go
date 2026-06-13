package subagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/tool"
)

const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

type SubAgentSpec struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Instructions string         `json:"instructions,omitempty"`
	Model        model.Model    `json:"-"`
	Tools        []tool.Tool    `json:"-"`
	MaxSteps     int            `json:"maxSteps,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type TaskSpec struct {
	ID        string         `json:"id,omitempty"`
	AgentName string         `json:"agentName,omitempty"`
	Agent     string         `json:"agent,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     string         `json:"input"`
	Files     []string       `json:"files,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Options struct {
	MaxTasks       int  `json:"maxTasks,omitempty"`
	MaxDepth       int  `json:"maxDepth,omitempty"`
	AllowNested    bool `json:"allowNested,omitempty"`
	Parallel       bool `json:"parallel,omitempty"`
	FailFast       bool `json:"failFast,omitempty"`
	InheritContext bool `json:"inheritContext,omitempty"`
}

type Request struct {
	RunID        string         `json:"runId"`
	ParentStep   int            `json:"parentStep,omitempty"`
	ParentTaskID string         `json:"parentTaskId,omitempty"`
	ToolCallID   string         `json:"toolCallId,omitempty"`
	Depth        int            `json:"depth,omitempty"`
	Tasks        []TaskSpec     `json:"tasks"`
	Options      Options        `json:"options,omitempty"`
	Context      map[string]any `json:"-"`
}

type Result struct {
	Tasks []TaskResult `json:"tasks"`
}

type TaskResult struct {
	ID        string         `json:"id"`
	AgentName string         `json:"agentName"`
	Name      string         `json:"name,omitempty"`
	Status    string         `json:"status"`
	Output    string         `json:"output,omitempty"`
	Error     string         `json:"error,omitempty"`
	RunID     string         `json:"runId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Events    []Event        `json:"-"`
}

type Event struct {
	Seq       int64          `json:"seq,omitempty"`
	Type      string         `json:"type"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

func (s SubAgentSpec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("subagent name is required")
	}
	return nil
}

func (t TaskSpec) NormalizedAgentName() string {
	if strings.TrimSpace(t.AgentName) != "" {
		return strings.TrimSpace(t.AgentName)
	}
	return strings.TrimSpace(t.Agent)
}

func (t TaskSpec) Validate() error {
	if t.NormalizedAgentName() == "" {
		return fmt.Errorf("task agent is required")
	}
	if strings.TrimSpace(t.Input) == "" {
		return fmt.Errorf("task input is required")
	}
	return nil
}

func (r Request) Validate() error {
	maxTasks := r.Options.MaxTasks
	if maxTasks <= 0 {
		maxTasks = 8
	}
	if len(r.Tasks) == 0 {
		return fmt.Errorf("at least one subtask is required")
	}
	if len(r.Tasks) > maxTasks {
		return fmt.Errorf("too many subtasks: %d > %d", len(r.Tasks), maxTasks)
	}
	if r.Depth > 0 && !r.Options.AllowNested {
		return fmt.Errorf("nested_subagent_not_allowed")
	}
	maxDepth := r.Options.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if r.Depth >= maxDepth {
		return fmt.Errorf("subagent_max_depth_exceeded: %d >= %d", r.Depth, maxDepth)
	}
	for _, task := range r.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r Result) ToolResultJSON() (string, map[string]any, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", nil, err
	}
	var structured map[string]any
	if err := json.Unmarshal(data, &structured); err != nil {
		return string(data), nil, err
	}
	return string(data), structured, nil
}
