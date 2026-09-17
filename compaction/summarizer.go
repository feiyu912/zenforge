package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

// SummaryPrefix introduces the replacement user message that shadows the
// compacted range. It follows the codex-style handoff framing: the summary
// was produced for the model that continues the run.
const SummaryPrefix = "Another language model started to solve this task and produced a summary of its progress. " +
	"You also have access to the state of the tools that were used. Build on the work that has already been done and avoid duplicating it. " +
	"Here is the summary produced by the other language model:"

// SummarizePrompt instructs the summarizer model. It follows the codex
// context-checkpoint-compaction handoff shape.
const SummarizePrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include:
- The original task and any explicit user constraints or preferences
- Current progress and key decisions made
- Important context: file paths, commands, identifiers, data values, and tool state needed to continue
- What remains to be done, as clear next steps

Be concise, structured, and focused on helping the next LLM seamlessly continue the work. Do not ask questions.`

// DefaultInputBudgetChars bounds the transcript handed to the summarizer
// (~20k tokens at the default heuristic), matching the codex compact user
// message budget.
const DefaultInputBudgetChars = 80_000

// DefaultTranscriptMessageChars bounds one message inside the transcript.
const DefaultTranscriptMessageChars = 2000

// Summary is one completed summarization.
type Summary struct {
	Text      string
	Model     string
	Usage     model.Usage
	MaxTokens int
}

// SummarizeRequest carries the shadowed range and the run identity.
type SummarizeRequest struct {
	// RunInput is the original task input; the summary must carry it.
	RunInput string
	// Messages is the shadowed range, already pruned when pruning is on.
	Messages []harness.MessageState
	// MaxSummaryTokens caps generation; 0 lets the summarizer decide.
	MaxSummaryTokens int
	// InputBudgetChars bounds the transcript; 0 uses the default.
	InputBudgetChars int
}

// Summarizer produces the compaction summary. Hosts may substitute remote,
// template, or test summarizers; ModelSummarizer is the built-in default.
type Summarizer interface {
	Summarize(ctx context.Context, req SummarizeRequest) (Summary, error)
}

// SummarizerFunc adapts a function to Summarizer.
type SummarizerFunc func(ctx context.Context, req SummarizeRequest) (Summary, error)

func (f SummarizerFunc) Summarize(ctx context.Context, req SummarizeRequest) (Summary, error) {
	return f(ctx, req)
}

// ModelSummarizer summarizes through any model.Model with a one-shot
// no-tools Generate call.
type ModelSummarizer struct {
	Model            model.Model
	Name             string
	Prompt           string
	InputBudgetChars int
	MessageChars     int
}

func (s ModelSummarizer) Summarize(ctx context.Context, req SummarizeRequest) (Summary, error) {
	if s.Model == nil {
		return Summary{}, errors.New("compaction summarizer model is required")
	}
	prompt := s.Prompt
	if prompt == "" {
		prompt = SummarizePrompt
	}
	transcript := FormatTranscript(req, s.InputBudgetChars, s.MessageChars)
	maxTokens := req.MaxSummaryTokens
	response, err := s.Model.Generate(ctx, model.Request{
		Messages: []model.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: transcript},
		},
		ToolChoice: model.ToolChoiceNone,
		Meta: map[string]any{
			"zenforge.compaction": true,
			"zenforge.maxTokens":  maxTokens,
		},
	})
	if err != nil {
		return Summary{}, fmt.Errorf("compaction summarize: %w", err)
	}
	text := strings.TrimSpace(response.Message.Content)
	if text == "" {
		return Summary{}, errors.New("compaction summarize returned an empty summary")
	}
	return Summary{
		Text:      text,
		Model:     s.Name,
		Usage:     response.Usage,
		MaxTokens: maxTokens,
	}, nil
}

// FormatTranscript renders the shadowed range for the summarizer with a
// bounded per-message size and a bounded total. When the total exceeds the
// budget, middle messages are elided while the earliest (task context) and
// most recent (current position) messages are retained.
func FormatTranscript(req SummarizeRequest, budgetChars, messageChars int) string {
	if budgetChars <= 0 {
		budgetChars = DefaultInputBudgetChars
	}
	if messageChars <= 0 {
		messageChars = DefaultTranscriptMessageChars
	}
	type entry struct {
		text string
	}
	entries := make([]entry, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.RunInput) != "" {
		entries = append(entries, entry{text: "## Original task\n" + truncateRunes(req.RunInput, messageChars)})
	}
	for _, message := range req.Messages {
		entries = append(entries, entry{text: formatTranscriptMessage(message, messageChars)})
	}
	total := 0
	for _, item := range entries {
		total += len(item.text)
	}
	if total > budgetChars && len(entries) > 2 {
		head := entries[:1]
		tail := entries[len(entries)-1:]
		middleBudget := budgetChars - len(head[0].text) - len(tail[0].text)
		var middle []entry
		used := 0
		// Keep the most recent middle entries that fit; older ones are elided.
		for i := len(entries) - 2; i >= 1; i-- {
			if used+len(entries[i].text) > middleBudget {
				middle = append([]entry{{text: fmt.Sprintf("[... %d earlier transcript messages omitted ...]", len(entries)-2-len(middle))}}, middle...)
				break
			}
			middle = append([]entry{entries[i]}, middle...)
			used += len(entries[i].text)
		}
		if len(middle) == 0 {
			middle = []entry{{text: "[... earlier transcript messages omitted ...]"}}
		}
		entries = append(append(append([]entry{}, head...), middle...), tail...)
	}
	var builder strings.Builder
	for i, item := range entries {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(item.text)
	}
	return builder.String()
}

func formatTranscriptMessage(message harness.MessageState, messageChars int) string {
	var builder strings.Builder
	builder.WriteString("## " + strings.ToUpper(message.Role))
	if message.Name != "" {
		builder.WriteString(" (" + message.Name + ")")
	}
	builder.WriteString("\n")
	if message.Content != "" {
		builder.WriteString(truncateRunes(message.Content, messageChars))
	}
	for _, call := range message.ToolCalls {
		builder.WriteString(fmt.Sprintf("\n[tool call] %s %s", call.Name, truncateRunes(string(call.Arguments), messageChars/4)))
	}
	return builder.String()
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + fmt.Sprintf("\n[... %d characters truncated ...]", len(runes)-max)
}
