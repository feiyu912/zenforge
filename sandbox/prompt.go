package sandbox

import "context"

type EnvironmentPromptProvider interface {
	Prompt(ctx context.Context, environmentID string) (Prompt, error)
}

type Prompt struct {
	EnvironmentID string         `json:"environmentId"`
	Content       string         `json:"content"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type PromptProviderFunc func(context.Context, string) (Prompt, error)

func (f PromptProviderFunc) Prompt(ctx context.Context, environmentID string) (Prompt, error) {
	return f(ctx, environmentID)
}
