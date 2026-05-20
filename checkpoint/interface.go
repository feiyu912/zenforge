package checkpoint

import "context"

type Store interface {
	Save(ctx context.Context, checkpoint Checkpoint) error
	Load(ctx context.Context, runID string) (*Checkpoint, error)
	Delete(ctx context.Context, runID string) error
}

type Checkpoint struct {
	RunID    string
	Input    string
	Step     int
	Messages []Message
	Todos    []Todo
	Meta     map[string]any
}

type Message struct {
	Role    string
	Content string
}

type Todo struct {
	ID      string
	Content string
	Status  string
}

