package sandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge"
	coresandbox "github.com/feiyu912/zenforge/sandbox"
)

const (
	MetadataEnvironmentID = "sandbox.environmentId"
	MetadataPrompt        = "sandbox.prompt"
)

type Augmenter struct {
	Provider      coresandbox.EnvironmentPromptProvider
	EnvironmentID string
	Header        string
}

func (a Augmenter) AugmentTask(ctx context.Context, task zenforge.Task) (zenforge.Task, coresandbox.Prompt, error) {
	if err := ctx.Err(); err != nil {
		return task, coresandbox.Prompt{}, err
	}
	if a.Provider == nil {
		return cloneTask(task), coresandbox.Prompt{}, nil
	}
	environmentID := a.environmentID(task)
	if environmentID == "" {
		return task, coresandbox.Prompt{}, fmt.Errorf("sandbox environment id is required")
	}
	prompt, err := a.Provider.Prompt(ctx, environmentID)
	if err != nil {
		return task, coresandbox.Prompt{}, err
	}
	if strings.TrimSpace(prompt.Content) == "" {
		return cloneTask(task), prompt, nil
	}
	if prompt.EnvironmentID == "" {
		prompt.EnvironmentID = environmentID
	}
	out := cloneTask(task)
	out.Input = formatInput(a.header(), task.Input, prompt.Content)
	if out.Meta == nil {
		out.Meta = map[string]any{}
	}
	out.Meta[MetadataEnvironmentID] = prompt.EnvironmentID
	out.Meta[MetadataPrompt] = map[string]any{
		"environmentId": prompt.EnvironmentID,
		"metadata":      cloneMap(prompt.Metadata),
	}
	return out, prompt, nil
}

func (a Augmenter) environmentID(task zenforge.Task) string {
	if strings.TrimSpace(a.EnvironmentID) != "" {
		return strings.TrimSpace(a.EnvironmentID)
	}
	if task.Meta == nil {
		return ""
	}
	if value, ok := task.Meta[MetadataEnvironmentID].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func (a Augmenter) header() string {
	if strings.TrimSpace(a.Header) != "" {
		return a.Header
	}
	return "Sandbox environment"
}

func formatInput(header, input, content string) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(":\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n\nUser request:\n")
	b.WriteString(input)
	return b.String()
}

func cloneTask(task zenforge.Task) zenforge.Task {
	task.Meta = cloneMap(task.Meta)
	return task
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
