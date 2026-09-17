package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/model"
)

// ID derives a memory's identity from what it says, not from which run said
// it: the same learning discovered twice is one memory. Scope and project
// are part of the identity because the same sentence can be true for one
// project and false for another.
func ID(scope Scope, project, text string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + project + "\x00" + strings.ToLower(singleLine(text))))
	return "mem_" + hex.EncodeToString(sum[:])[:16]
}

// DefaultDistillMaxTokens bounds the distillation call.
const DefaultDistillMaxTokens = 1200

// DefaultDistillMaxEntries caps how many memories one run may produce, so a
// single talkative run cannot flood the store.
const DefaultDistillMaxEntries = 5

// DistillPrompt asks a model for durable, reusable learnings from one run.
// It is adapted from the reference's stage-one memory prompt: extract only
// what will still be true and useful in a later session, cite the source,
// and prefer saying nothing over guessing.
const DistillPrompt = `You extract durable memories from one finished coding session.

Write down only what will still be true and useful in a later session:
project conventions you had to discover, commands that work or do not work
here, pitfalls and their symptoms, user preferences, and facts about the
environment. Do not record what the user asked for, what you did step by
step, or anything that only mattered during the session. Do not guess: if
something was not clearly established, leave it out. One or two sentences
per memory, self-contained, no pronouns pointing at the session.

Return JSON only, shaped exactly like this:
{"memories":[{"text":"...","tags":["..."],"citations":["..."]}]}

Citations are file paths, command lines, or event names that support the
memory. Return {"memories":[]} when the session established nothing durable.`

// ModelDistiller distils a run with one model call.
type ModelDistiller struct {
	// Model performs the call. Required.
	Model model.Model
	// Prompt overrides DistillPrompt.
	Prompt string
	// MaxEntries caps the memories kept from one run.
	MaxEntries int
	// MaxTokens bounds the call.
	MaxTokens int
	// Label names the distiller in stored entries; empty uses "model".
	Label string
}

// Name returns the distiller's label.
func (d ModelDistiller) Name() string {
	if strings.TrimSpace(d.Label) != "" {
		return d.Label
	}
	return "model"
}

// Distill runs the extraction call.
func (d ModelDistiller) Distill(ctx context.Context, summary RunSummary) ([]Entry, error) {
	if d.Model == nil {
		return nil, fmt.Errorf("memory distiller model is required")
	}
	prompt := d.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = DistillPrompt
	}
	maxTokens := d.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultDistillMaxTokens
	}
	maxEntries := d.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultDistillMaxEntries
	}
	response, err := d.Model.Generate(ctx, model.Request{
		Messages: []model.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: FormatSummary(summary)},
		},
		ToolChoice: model.ToolChoiceNone,
		Meta: map[string]any{
			"zenforge.memory":    true,
			"zenforge.maxTokens": maxTokens,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("memory distill: %w", err)
	}
	parsed, err := parseMemories(response.Message.Content)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(parsed))
	for _, item := range parsed {
		text := singleLine(item.Text)
		if text == "" {
			continue
		}
		entries = append(entries, Entry{
			Text:      text,
			Tags:      cleanList(item.Tags),
			Citations: cleanList(item.Citations),
			Source:    d.Name(),
		})
		if len(entries) >= maxEntries {
			break
		}
	}
	return entries, nil
}

// wireMemories is the model's JSON answer.
type wireMemories struct {
	Memories []struct {
		Text      string   `json:"text"`
		Tags      []string `json:"tags,omitempty"`
		Citations []string `json:"citations,omitempty"`
	} `json:"memories"`
}

// parseMemories reads the model's JSON, tolerating a fenced code block,
// which models produce even when asked not to.
func parseMemories(output string) ([]struct {
	Text      string   `json:"text"`
	Tags      []string `json:"tags,omitempty"`
	Citations []string `json:"citations,omitempty"`
}, error) {
	text := strings.TrimSpace(output)
	if strings.HasPrefix(text, "```") {
		if index := strings.Index(text, "\n"); index >= 0 {
			text = text[index+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("memory distill returned no JSON object: %s", truncateForError(text))
	}
	var wire wireMemories
	decoder := json.NewDecoder(strings.NewReader(text[start : end+1]))
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("memory distill returned invalid JSON: %w", err)
	}
	return wire.Memories, nil
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := singleLine(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func truncateForError(text string) string {
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}

// FormatSummary renders a run for the distiller with bounded sections, so a
// huge run cannot blow the call's context. The task and the result are kept
// verbatim up to their limits because they carry the intent; the command,
// failure, and file lists are capped by count.
func FormatSummary(summary RunSummary) string {
	var builder strings.Builder
	builder.WriteString("## Task\n" + clip(summary.Task, 2000) + "\n")
	builder.WriteString("\n## Result\n" + clip(summary.Output, 4000) + "\n")
	if len(summary.Commands) > 0 {
		builder.WriteString("\n## Commands run\n")
		for _, command := range lastN(summary.Commands, 40) {
			builder.WriteString("- " + clip(singleLine(command), 300) + "\n")
		}
	}
	if len(summary.Failures) > 0 {
		builder.WriteString("\n## Failures observed\n")
		for _, failure := range lastN(summary.Failures, 20) {
			builder.WriteString("- " + clip(singleLine(failure), 300) + "\n")
		}
	}
	if len(summary.Files) > 0 {
		builder.WriteString("\n## Files touched\n")
		for _, file := range lastN(summary.Files, 40) {
			builder.WriteString("- " + clip(singleLine(file), 300) + "\n")
		}
	}
	if strings.TrimSpace(summary.Events) != "" {
		builder.WriteString("\n## Event trail\n" + clip(summary.Events, 4000) + "\n")
	}
	return builder.String()
}

func clip(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n[truncated]"
}

func lastN(values []string, n int) []string {
	if n <= 0 || len(values) <= n {
		return values
	}
	return values[len(values)-n:]
}

// Manager ties a store and a distiller into the two operations the agent
// needs: a summary to inject at run start, and a place to record a finished
// run.
type Manager struct {
	// Store persists entries. Required.
	Store Store
	// Distiller produces entries from a run. Optional: without one, the
	// manager only injects what is already stored.
	Distiller Distiller
	// Scope is the scope new entries get.
	Scope Scope
	// Project is the project new entries and summary reads use.
	Project string
	// Timeout bounds a distillation call and a store write. Zero uses
	// DefaultRecordTimeout.
	Timeout time.Duration
}

// DefaultRecordTimeout bounds run-end memory work.
const DefaultRecordTimeout = 30 * time.Second

// Summary returns the summary to inject into a run's instructions.
func (m *Manager) Summary(ctx context.Context) (string, error) {
	if m == nil || m.Store == nil {
		return "", nil
	}
	return m.Store.Summary(ctx, m.Project)
}

// Record distils a finished run and appends the result. It returns how many
// entries were added; zero is a normal outcome and never an error.
func (m *Manager) Record(ctx context.Context, summary RunSummary) (int, error) {
	if m == nil || m.Store == nil || m.Distiller == nil {
		return 0, nil
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = DefaultRecordTimeout
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	entries, err := m.Distiller.Distill(recordCtx, summary)
	if err != nil {
		return 0, err
	}
	scope := m.Scope
	if scope == "" {
		scope = ScopeUser
	}
	for index := range entries {
		entries[index].Scope = scope
		if scope == ScopeProject {
			entries[index].Project = m.Project
		}
		entries[index].RunID = summary.RunID
		if entries[index].Source == "" {
			entries[index].Source = m.Distiller.Name()
		}
		if entries[index].CreatedAt.IsZero() {
			entries[index].CreatedAt = time.Now().UTC()
		}
		entries[index].ID = ID(entries[index].Scope, entries[index].Project, entries[index].Text)
	}
	return m.Store.Append(recordCtx, entries...)
}
