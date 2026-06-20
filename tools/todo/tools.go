package todo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/feiyu912/zenforge/planner"
	"github.com/feiyu912/zenforge/tool"
)

type Config struct {
	Manager planner.Manager
}

func Tools(config Config) ([]tool.Tool, error) {
	write, err := Write(config, "todo_write")
	if err != nil {
		return nil, err
	}
	read, err := Read(config, "todo_read")
	if err != nil {
		return nil, err
	}
	update, err := Update(config, "todo_update")
	if err != nil {
		return nil, err
	}
	return []tool.Tool{write, read, update}, nil
}

func CompatibilityAliases(config Config) ([]tool.Tool, error) {
	write, err := Write(config, "plan_add_tasks")
	if err != nil {
		return nil, err
	}
	read, err := Read(config, "plan_get_tasks")
	if err != nil {
		return nil, err
	}
	update, err := Update(config, "plan_update_task")
	if err != nil {
		return nil, err
	}
	return []tool.Tool{write, read, update}, nil
}

func Write(config Config, name string) (tool.Tool, error) {
	return newTool(config, name, kindWrite, "Create or replace the current run todo list.")
}

func Read(config Config, name string) (tool.Tool, error) {
	return newTool(config, name, kindRead, "Read the current run todo list.")
}

func Update(config Config, name string) (tool.Tool, error) {
	return newTool(config, name, kindUpdate, "Update one todo status, content, notes, or metadata.")
}

type kind string

const (
	kindWrite  kind = "write"
	kindRead   kind = "read"
	kindUpdate kind = "update"
)

type todoTool struct {
	name        string
	description string
	manager     planner.Manager
	kind        kind
}

func newTool(config Config, name string, kind kind, description string) (tool.Tool, error) {
	if config.Manager == nil {
		return nil, fmt.Errorf("%w: todo manager is nil", tool.ErrInvalidTool)
	}
	if name == "" {
		name = "todo_" + string(kind)
	}
	return todoTool{name: name, description: description, manager: config.Manager, kind: kind}, nil
}

func (t todoTool) Name() string {
	return t.name
}

func (t todoTool) Description() string {
	return t.description
}

func (t todoTool) Schema() map[string]any {
	switch t.kind {
	case kindWrite:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":      map[string]any{"type": "string"},
							"content": map[string]any{"type": "string"},
							"status": map[string]any{
								"type": "string",
								"enum": []string{"pending", "in_progress", "done", "failed", "cancelled"},
							},
							"notes": map[string]any{"type": "string"},
							"meta":  map[string]any{"type": "object"},
						},
						"required":             []string{"content"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		}
	case kindUpdate:
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
				"status": map[string]any{
					"type": "string",
					"enum": []string{"pending", "in_progress", "done", "failed", "cancelled"},
				},
				"content":    map[string]any{"type": "string"},
				"setContent": map[string]any{"type": "boolean"},
				"notes":      map[string]any{"type": "string"},
				"setNotes":   map[string]any{"type": "boolean"},
				"meta":       map[string]any{"type": "object"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		}
	default:
		return map[string]any{"type": "object", "additionalProperties": false}
	}
}

func (t todoTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	runID := call.RunID
	if runID == "" {
		return tool.Result{Error: "runID is required", ExitCode: 1}, fmt.Errorf("runID is required")
	}
	switch t.kind {
	case kindWrite:
		var in writeInput
		if err := decode(input, &in); err != nil {
			return invalidArguments(err)
		}
		todos, err := t.manager.Replace(ctx, runID, in.plannerTodos())
		return result(todos, err)
	case kindRead:
		var in struct{}
		if err := decode(input, &in); err != nil {
			return invalidArguments(err)
		}
		todos, err := t.manager.List(ctx, runID)
		return result(todos, err)
	case kindUpdate:
		var in updateInput
		if err := decode(input, &in); err != nil {
			return invalidArguments(err)
		}
		patch := planner.Patch{
			Content: in.Content,
			Status:  in.Status,
			Notes:   in.Notes,
			Meta:    in.Meta,
		}
		if patch.Content == nil && in.SetContent {
			patch.Content = new(string)
		}
		if patch.Notes == nil && in.SetNotes {
			patch.Notes = new(string)
		}
		todos, err := t.manager.Update(ctx, runID, in.ID, patch)
		return result(todos, err)
	default:
		return tool.Result{Error: "unknown todo tool kind", ExitCode: 1}, fmt.Errorf("unknown todo tool kind")
	}
}

type writeInput struct {
	Todos []writeTodoInput `json:"todos"`
}

type writeTodoInput struct {
	ID      string             `json:"id,omitempty"`
	Content string             `json:"content"`
	Status  planner.TodoStatus `json:"status,omitempty"`
	Notes   string             `json:"notes,omitempty"`
	Meta    map[string]any     `json:"meta,omitempty"`
}

func (in writeInput) plannerTodos() []planner.Todo {
	todos := make([]planner.Todo, len(in.Todos))
	for i, todo := range in.Todos {
		todos[i] = planner.Todo{
			ID:      todo.ID,
			Content: todo.Content,
			Status:  todo.Status,
			Notes:   todo.Notes,
			Meta:    todo.Meta,
		}
	}
	return todos
}

type updateInput struct {
	ID         string              `json:"id"`
	Status     *planner.TodoStatus `json:"status,omitempty"`
	Content    *string             `json:"content,omitempty"`
	SetContent bool                `json:"setContent,omitempty"`
	Notes      *string             `json:"notes,omitempty"`
	SetNotes   bool                `json:"setNotes,omitempty"`
	Meta       map[string]any      `json:"meta,omitempty"`
}

func decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("arguments must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func invalidArguments(err error) (tool.Result, error) {
	return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1}, fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
}

func result(todos []planner.Todo, err error) (tool.Result, error) {
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	output := planner.FormatTodos(todos)
	return tool.Result{
		Output: output,
		Structured: map[string]any{
			"todos": todos,
			"text":  output,
		},
	}, nil
}
