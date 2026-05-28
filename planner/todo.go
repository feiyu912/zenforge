package planner

import (
	"fmt"
	"strings"
	"time"
)

type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoDone       TodoStatus = "done"
	TodoFailed     TodoStatus = "failed"
	TodoCancelled  TodoStatus = "cancelled"
)

type Todo struct {
	ID        string         `json:"id"`
	Content   string         `json:"content"`
	Status    TodoStatus     `json:"status"`
	Notes     string         `json:"notes,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type Patch struct {
	Content *string
	Status  *TodoStatus
	Notes   *string
	Meta    map[string]any
}

func ValidStatus(status TodoStatus) bool {
	switch status {
	case TodoPending, TodoInProgress, TodoDone, TodoFailed, TodoCancelled:
		return true
	default:
		return false
	}
}

func TerminalStatus(status TodoStatus) bool {
	switch status {
	case TodoDone, TodoFailed, TodoCancelled:
		return true
	default:
		return false
	}
}

func NormalizeTodos(todos []Todo, now time.Time) ([]Todo, error) {
	if len(todos) == 0 {
		return nil, fmt.Errorf("todo list cannot be empty")
	}
	seen := map[string]struct{}{}
	out := make([]Todo, 0, len(todos))
	for i, todo := range todos {
		todo.ID = strings.TrimSpace(todo.ID)
		if todo.ID == "" {
			todo.ID = fmt.Sprintf("todo_%d", i+1)
		}
		if _, exists := seen[todo.ID]; exists {
			return nil, fmt.Errorf("duplicate todo id %q", todo.ID)
		}
		seen[todo.ID] = struct{}{}
		todo.Content = strings.TrimSpace(todo.Content)
		if todo.Content == "" {
			return nil, fmt.Errorf("todo %q content is required", todo.ID)
		}
		if todo.Status == "" {
			todo.Status = TodoPending
		}
		if !ValidStatus(todo.Status) {
			return nil, fmt.Errorf("invalid todo status %q", todo.Status)
		}
		if todo.CreatedAt.IsZero() {
			todo.CreatedAt = now
		}
		todo.UpdatedAt = now
		out = append(out, todo)
	}
	return out, nil
}

func FormatTodos(todos []Todo) string {
	var b strings.Builder
	for _, todo := range todos {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- [")
		b.WriteString(string(todo.Status))
		b.WriteString("] ")
		b.WriteString(todo.ID)
		b.WriteString(": ")
		b.WriteString(todo.Content)
		if todo.Notes != "" {
			b.WriteString(" (")
			b.WriteString(todo.Notes)
			b.WriteString(")")
		}
	}
	return b.String()
}

func cloneTodos(todos []Todo) []Todo {
	out := make([]Todo, len(todos))
	copy(out, todos)
	return out
}
