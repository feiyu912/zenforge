package fake

import (
	"context"

	"github.com/feiyu912/zenforge/sandbox"
)

type PromptProvider struct {
	Prompts map[string]sandbox.Prompt
	Err     error
}

func (p PromptProvider) Prompt(ctx context.Context, environmentID string) (sandbox.Prompt, error) {
	if err := ctx.Err(); err != nil {
		return sandbox.Prompt{}, err
	}
	if p.Err != nil {
		return sandbox.Prompt{}, p.Err
	}
	prompt, ok := p.Prompts[environmentID]
	if !ok {
		return sandbox.Prompt{}, sandbox.ErrEnvironmentNotFound
	}
	return prompt, nil
}
