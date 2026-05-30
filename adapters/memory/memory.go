package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge"
)

type Store interface {
	Search(ctx context.Context, query Query) ([]Entry, error)
}

type Query struct {
	Text  string
	RunID string
	Limit int
	Meta  map[string]any
}

type Entry struct {
	ID    string         `json:"id,omitempty"`
	Text  string         `json:"text"`
	Score float64        `json:"score,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

type Augmenter struct {
	Store      Store
	MaxEntries int
	Header     string
}

func (a Augmenter) AugmentTask(ctx context.Context, task zenforge.Task) (zenforge.Task, []Entry, error) {
	if err := ctx.Err(); err != nil {
		return task, nil, err
	}
	if a.Store == nil {
		return cloneTask(task), nil, nil
	}
	limit := a.MaxEntries
	if limit <= 0 {
		limit = 5
	}
	entries, err := a.Store.Search(ctx, Query{
		Text:  task.Input,
		RunID: task.RunID,
		Limit: limit,
		Meta:  cloneMap(task.Meta),
	})
	if err != nil {
		return task, nil, err
	}
	if len(entries) == 0 {
		return cloneTask(task), nil, nil
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	out := cloneTask(task)
	out.Input = formatInput(a.header(), task.Input, entries)
	if out.Meta == nil {
		out.Meta = map[string]any{}
	}
	out.Meta["memory"] = map[string]any{
		"entries": cloneEntries(entries),
	}
	return out, cloneEntries(entries), nil
}

func (a Augmenter) header() string {
	if strings.TrimSpace(a.Header) != "" {
		return a.Header
	}
	return "Relevant memory"
}

type StaticStore struct {
	Entries []Entry
}

func NewStaticStore(entries ...Entry) *StaticStore {
	return &StaticStore{Entries: cloneEntries(entries)}
}

func (s *StaticStore) Search(ctx context.Context, query Query) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = len(s.Entries)
	}
	entries := cloneEntries(s.Entries)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func formatInput(header, input string, entries []Entry) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString(":\n")
	for _, entry := range entries {
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		if entry.ID != "" {
			_, _ = fmt.Fprintf(&b, "- [%s] %s\n", entry.ID, text)
		} else {
			_, _ = fmt.Fprintf(&b, "- %s\n", text)
		}
	}
	b.WriteString("\nUser request:\n")
	b.WriteString(input)
	return b.String()
}

func cloneTask(task zenforge.Task) zenforge.Task {
	task.Meta = cloneMap(task.Meta)
	return task
}

func cloneEntries(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		entry.Meta = cloneMap(entry.Meta)
		out = append(out, entry)
	}
	return out
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
