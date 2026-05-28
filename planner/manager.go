package planner

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Manager interface {
	List(ctx context.Context, runID string) ([]Todo, error)
	Replace(ctx context.Context, runID string, todos []Todo) ([]Todo, error)
	Update(ctx context.Context, runID string, id string, patch Patch) ([]Todo, error)
}

type EventType string

const EventTodoUpdated EventType = "todo.updated"

type Event struct {
	Type  EventType
	RunID string
	Todos []Todo
}

type EventSink func(ctx context.Context, event Event)

type MemoryManager struct {
	mu    sync.RWMutex
	todos map[string][]Todo
	sink  EventSink
	now   func() time.Time
}

type MemoryConfig struct {
	Sink EventSink
	Now  func() time.Time
}

func NewMemoryManager(config MemoryConfig) *MemoryManager {
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryManager{
		todos: map[string][]Todo{},
		sink:  config.Sink,
		now:   now,
	}
}

func (m *MemoryManager) List(ctx context.Context, runID string) ([]Todo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneTodos(m.todos[runID]), nil
}

func (m *MemoryManager) Replace(ctx context.Context, runID string, todos []Todo) ([]Todo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}
	normalized, err := NormalizeTodos(todos, m.now())
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.todos[runID] = cloneTodos(normalized)
	m.mu.Unlock()
	m.emit(ctx, runID, normalized)
	return cloneTodos(normalized), nil
}

func (m *MemoryManager) Update(ctx context.Context, runID string, id string, patch Patch) ([]Todo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}
	if id == "" {
		return nil, fmt.Errorf("todo id is required")
	}
	if patch.Status != nil && !ValidStatus(*patch.Status) {
		return nil, fmt.Errorf("invalid todo status %q", *patch.Status)
	}
	m.mu.Lock()
	todos := cloneTodos(m.todos[runID])
	found := false
	now := m.now()
	for i := range todos {
		if todos[i].ID != id {
			continue
		}
		found = true
		if patch.Content != nil {
			todos[i].Content = *patch.Content
		}
		if patch.Status != nil {
			todos[i].Status = *patch.Status
		}
		if patch.Notes != nil {
			todos[i].Notes = *patch.Notes
		}
		if patch.Meta != nil {
			todos[i].Meta = patch.Meta
		}
		todos[i].UpdatedAt = now
		break
	}
	if !found {
		return nil, fmt.Errorf("todo %q not found", id)
	}
	m.todos[runID] = cloneTodos(todos)
	out := cloneTodos(todos)
	m.mu.Unlock()
	m.emit(ctx, runID, out)
	return out, nil
}

func (m *MemoryManager) emit(ctx context.Context, runID string, todos []Todo) {
	if m.sink == nil {
		return
	}
	m.sink(ctx, Event{Type: EventTodoUpdated, RunID: runID, Todos: cloneTodos(todos)})
}
