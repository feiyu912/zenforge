package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/feiyu912/zenforge/subagent"
	"github.com/feiyu912/zenforge/tool"
)

const (
	Name  = "task"
	Alias = "agent_invoke"
)

type Config struct {
	MaxTasks int
	Alias    bool
}

func New(config Config) (tool.Tool, error) {
	name := Name
	if config.Alias {
		name = Alias
	}
	maxTasks := config.MaxTasks
	if maxTasks <= 0 {
		maxTasks = 8
	}
	return taskTool{name: name, maxTasks: maxTasks}, nil
}

func Must(config Config) tool.Tool {
	tool, err := New(config)
	if err != nil {
		panic(err)
	}
	return tool
}

func Tools(config Config) ([]tool.Tool, error) {
	primary, err := New(config)
	if err != nil {
		return nil, err
	}
	alias, err := New(Config{MaxTasks: config.MaxTasks, Alias: true})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{primary, alias}, nil
}

type taskTool struct {
	name     string
	maxTasks int
}

func (t taskTool) Name() string {
	return t.name
}

func (t taskTool) Description() string {
	return "Delegate one or more subtasks to configured sub-agents."
}

func (t taskTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": t.maxTasks,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agent": map[string]any{"type": "string"},
						"name":  map[string]any{"type": "string"},
						"input": map[string]any{"type": "string"},
						"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
					"required":             []string{"agent", "input"},
					"additionalProperties": true,
				},
			},
			"options": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"parallel": map[string]any{
						"type":        "boolean",
						"description": "Run independent child tasks concurrently.",
					},
					"failFast": map[string]any{
						"type":        "boolean",
						"description": "Cancel remaining child tasks after the first failure.",
					},
					"maxTasks": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     t.maxTasks,
						"description": "Maximum number of child tasks allowed for this request.",
					},
				},
				"additionalProperties": false,
			},
		},
		"required":             []string{"tasks"},
		"additionalProperties": false,
	}
}

func (t taskTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	_ = ctx
	_ = call
	req, err := Decode(input)
	if err != nil {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, err
	}
	if len(req.Tasks) > t.maxTasks {
		err := fmt.Errorf("too many subtasks: %d > %d", len(req.Tasks), t.maxTasks)
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	err = fmt.Errorf("task tool requires harness subagent runtime")
	return tool.Result{Error: err.Error(), ExitCode: 1}, err
}

func Decode(raw json.RawMessage) (subagent.Request, error) {
	var in struct {
		Tasks   []subagent.TaskSpec `json:"tasks"`
		Options taskOptions         `json:"options,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 {
		decoder = json.NewDecoder(bytes.NewReader([]byte(`{}`)))
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&in); err != nil {
		return subagent.Request{}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return subagent.Request{}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	req := subagent.Request{Tasks: in.Tasks, Options: in.Options.toSubAgentOptions()}
	if err := validateModelRequest(req); err != nil {
		return subagent.Request{}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
	}
	return req, nil
}

func validateModelRequest(req subagent.Request) error {
	if len(req.Tasks) == 0 {
		return fmt.Errorf("at least one subtask is required")
	}
	if req.Options.MaxTasks < 0 {
		return fmt.Errorf("maxTasks must be greater than zero")
	}
	if req.Options.MaxTasks > 0 && len(req.Tasks) > req.Options.MaxTasks {
		return fmt.Errorf("too many subtasks: %d > %d", len(req.Tasks), req.Options.MaxTasks)
	}
	for _, task := range req.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type taskOptions struct {
	MaxTasks int  `json:"maxTasks,omitempty"`
	Parallel bool `json:"parallel,omitempty"`
	FailFast bool `json:"failFast,omitempty"`
}

func (o taskOptions) toSubAgentOptions() subagent.Options {
	return subagent.Options{
		MaxTasks: o.MaxTasks,
		Parallel: o.Parallel,
		FailFast: o.FailFast,
	}
}

func IsTaskTool(name string) bool {
	return name == Name || name == Alias
}
