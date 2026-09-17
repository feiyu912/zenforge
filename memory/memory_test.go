package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/model"
)

func TestFileStoreAppendsDedupesAndSummarizes(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	first := Entry{Scope: ScopeUser, Text: "the repo runs tests with make check", Source: "test", CreatedAt: time.Unix(1, 0).UTC()}
	first.ID = ID(ScopeUser, "", first.Text)
	added, err := store.Append(ctx, first)
	if err != nil || added != 1 {
		t.Fatalf("Append = %d, %v", added, err)
	}
	// The same learning discovered again is the same memory.
	duplicate := first
	duplicate.RunID = "run_2"
	added, err = store.Append(ctx, duplicate)
	if err != nil || added != 0 {
		t.Fatalf("a duplicate was appended: %d, %v", added, err)
	}
	// An empty learning is not a memory.
	added, err = store.Append(ctx, Entry{ID: "mem_empty", Text: "   "})
	if err != nil || added != 0 {
		t.Fatalf("an empty entry was appended: %d, %v", added, err)
	}
	summary, err := store.Summary(ctx, "")
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	if !strings.Contains(summary, "the repo runs tests with make check") {
		t.Fatalf("summary = %q", summary)
	}
	entries, err := store.List(ctx, ScopeUser, "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %#v, %v", entries, err)
	}
	if entries[0].Text != first.Text {
		t.Fatalf("round trip lost the text: %#v", entries[0])
	}
}

func TestScopeVisibility(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	user := Entry{ID: "mem_user", Scope: ScopeUser, Text: "user-level fact"}
	project := Entry{ID: "mem_project", Scope: ScopeProject, Project: "/repo/a", Text: "project fact for a"}
	other := Entry{ID: "mem_other", Scope: ScopeProject, Project: "/repo/b", Text: "project fact for b"}
	if _, err := store.Append(ctx, user, project, other); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	entries, err := store.List(ctx, ScopeProject, "/repo/a")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	texts := make([]string, 0, len(entries))
	for _, entry := range entries {
		texts = append(texts, entry.Text)
	}
	// A project sees its own memories and every user memory, never another
	// project's.
	if len(entries) != 2 || !strings.Contains(strings.Join(texts, "|"), "user-level fact") || strings.Contains(strings.Join(texts, "|"), "for b") {
		t.Fatalf("entries = %#v", texts)
	}
	// A project memory with no project name cannot be attributed, so it is
	// shown nowhere.
	orphan := Entry{ID: "mem_orphan", Scope: ScopeProject, Text: "no project"}
	if _, err := store.Append(ctx, orphan); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	entries, err = store.List(ctx, ScopeProject, "/repo/a")
	if err != nil || len(entries) != 2 {
		t.Fatalf("orphan entry became visible: %#v, %v", entries, err)
	}
}

func TestSummaryHonoursItsByteBudget(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	store.SummaryBytes = 300
	store.MaxEntries = 50
	for index := 0; index < 40; index++ {
		entry := Entry{
			ID:        ID(ScopeUser, "", strings.Repeat("x", 20)+string(rune('a'+index%26))),
			Scope:     ScopeUser,
			Text:      strings.Repeat("long memory text ", 3) + string(rune('a'+index%26)),
			CreatedAt: time.Unix(int64(index), 0).UTC(),
		}
		if _, err := store.Append(ctx, entry); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}
	summary, err := store.Summary(ctx, "")
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	// The summary is a hard-capped injection target: growing without bound
	// would silently eat the model's context.
	if len(summary) > store.SummaryBytes {
		t.Fatalf("summary is %d bytes, budget is %d", len(summary), store.SummaryBytes)
	}
	if !strings.HasPrefix(summary, headerLine) {
		t.Fatalf("summary = %q", summary)
	}
}

func TestSummaryOnEmptyAndBrokenFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// No files at all is a normal state.
	store := NewFileStore(root)
	if summary, err := store.Summary(ctx, ""); err != nil || summary != "" {
		t.Fatalf("Summary = %q, %v", summary, err)
	}
	if entries, err := store.List(ctx, ScopeUser, ""); err != nil || len(entries) != 0 {
		t.Fatalf("List = %#v, %v", entries, err)
	}
	// An entry with no scope is invisible rather than guessed at: the block
	// format always writes one, so a missing scope means the file was
	// hand-edited into a state the store cannot attribute.
	if err := os.WriteFile(filepath.Join(root, rawFileName), []byte("## mem_unscoped\n- text: guess me\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if entries, err := store.List(ctx, ScopeUser, ""); err != nil || len(entries) != 0 {
		t.Fatalf("an unscoped entry became visible: %#v, %v", entries, err)
	}
	// A malformed block is skipped, not fatal: one bad line must not hide
	// every memory.
	if err := os.WriteFile(filepath.Join(root, rawFileName), []byte("garbage\n## mem_ok\n- scope: user\n- text: keep me\n## mem_bad\n- scope: user\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	entries, err := store.List(ctx, ScopeUser, "")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "keep me" {
		t.Fatalf("entries = %#v", entries)
	}
	// A store with no root refuses to write rather than silently dropping
	// memories.
	if _, err := (&FileStore{}).Append(ctx, Entry{ID: "mem_x", Text: "x"}); err == nil {
		t.Fatal("a store without a root accepted a write")
	}
}

func TestModelDistillerParsesModelOutput(t *testing.T) {
	fake := &scriptedMemoryModel{response: "```json\n{\"memories\":[{\"text\":\"use make check\\nbefore committing\",\"tags\":[\"build\"],\"citations\":[\"Makefile\"]},{\"text\":\"   \"}]}\n```"}
	distiller := ModelDistiller{Model: fake, Label: "test"}
	entries, err := distiller.Distill(context.Background(), RunSummary{Task: "fix the build", Output: "done"})
	if err != nil {
		t.Fatalf("Distill returned error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	// Newlines are flattened: a memory is one injectable line.
	if entries[0].Text != "use make check before committing" {
		t.Fatalf("text = %q", entries[0].Text)
	}
	if entries[0].Source != "test" || len(entries[0].Tags) != 1 || len(entries[0].Citations) != 1 {
		t.Fatalf("entry = %#v", entries[0])
	}
	// The prompt and the run were both sent.
	if len(fake.requests) != 1 || len(fake.requests[0].Messages) != 2 {
		t.Fatalf("requests = %#v", fake.requests)
	}
	if !strings.Contains(fake.requests[0].Messages[1].Content, "fix the build") {
		t.Fatalf("the run was not sent: %q", fake.requests[0].Messages[1].Content)
	}
	if fake.requests[0].ToolChoice != model.ToolChoiceNone {
		t.Fatalf("tool choice = %q", fake.requests[0].ToolChoice)
	}
}

func TestModelDistillerRejectsUnusableOutput(t *testing.T) {
	cases := []struct {
		name     string
		response string
	}{
		{"no json", "I could not think of anything durable."},
		{"broken json", `{"memories":[{"text":`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			distiller := ModelDistiller{Model: &scriptedMemoryModel{response: testCase.response}}
			if _, err := distiller.Distill(context.Background(), RunSummary{}); err == nil {
				t.Fatal("unusable output was accepted")
			}
		})
	}
	// A model error is reported, not swallowed.
	if _, err := (ModelDistiller{Model: &scriptedMemoryModel{err: errors.New("boom")}}).Distill(context.Background(), RunSummary{}); err == nil {
		t.Fatal("a model error was ignored")
	}
	// Without a model the distiller refuses instead of pretending.
	if _, err := (ModelDistiller{}).Distill(context.Background(), RunSummary{}); err == nil {
		t.Fatal("a distiller without a model ran")
	}
}

func TestManagerRecordsScopedEntries(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager := &Manager{
		Store:     store,
		Distiller: &scriptedDistiller{entries: []Entry{{Text: "always run gofmt"}, {Text: "   "}}},
		Scope:     ScopeProject,
		Project:   "/repo/a",
	}
	added, err := manager.Record(ctx, RunSummary{RunID: "run_1"})
	if err != nil || added != 1 {
		t.Fatalf("Record = %d, %v", added, err)
	}
	entries, err := store.List(ctx, ScopeProject, "/repo/a")
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %#v, %v", entries, err)
	}
	entry := entries[0]
	if entry.Scope != ScopeProject || entry.Project != "/repo/a" || entry.RunID != "run_1" || entry.ID == "" {
		t.Fatalf("entry = %#v", entry)
	}
	// Recording the same run twice does not duplicate the memory.
	added, err = manager.Record(ctx, RunSummary{RunID: "run_1"})
	if err != nil || added != 0 {
		t.Fatalf("Record = %d, %v", added, err)
	}
	// A manager with no distiller only injects.
	readOnly := &Manager{Store: store}
	if added, err := readOnly.Record(ctx, RunSummary{}); err != nil || added != 0 {
		t.Fatalf("Record = %d, %v", added, err)
	}
	if summary, err := readOnly.Summary(ctx); err != nil || !strings.Contains(summary, "always run gofmt") {
		t.Fatalf("Summary = %q, %v", summary, err)
	}
	var nilManager *Manager
	if added, err := nilManager.Record(ctx, RunSummary{}); err != nil || added != 0 {
		t.Fatalf("a nil manager recorded: %d, %v", added, err)
	}
	if summary, err := nilManager.Summary(ctx); err != nil || summary != "" {
		t.Fatalf("a nil manager summarized: %q, %v", summary, err)
	}
}

func TestManagerRecordFailureIsReportedNotSwallowed(t *testing.T) {
	manager := &Manager{
		Store:     NewFileStore(t.TempDir()),
		Distiller: &scriptedDistiller{err: errors.New("distiller down")},
	}
	if _, err := manager.Record(context.Background(), RunSummary{}); err == nil {
		t.Fatal("a distiller failure was swallowed")
	}
}

func TestRunDigestCollectsTheShapeOfARun(t *testing.T) {
	digest := NewRunDigest("fix the build")
	digest.Observe("tool.call", map[string]any{"toolName": "shell", "arguments": `{"command":"make check","description":"d"}`})
	digest.Observe("tool.call", map[string]any{"arguments": map[string]any{"cmd": "go test ./..."}})
	digest.Observe("tool.call", map[string]any{"arguments": `{"path":"/tmp/x"}`})
	digest.Observe("tool.error", map[string]any{"error": "make: command not found"})
	digest.Observe("workspace.changed", map[string]any{"path": "/repo/a/main.go"})
	digest.Observe("workspace.changed", map[string]any{"path": "/repo/a/main.go"})
	digest.Observe("run.done", map[string]any{"output": "build fixed"})
	digest.Observe("some.future.event", map[string]any{"x": 1})
	summary := digest.Summary("run_9", "/repo/a")
	if summary.Task != "fix the build" || summary.Output != "build fixed" || summary.RunID != "run_9" || summary.Project != "/repo/a" {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.Commands) != 2 || summary.Commands[0] != "make check" || summary.Commands[1] != "go test ./..." {
		t.Fatalf("commands = %#v", summary.Commands)
	}
	if len(summary.Failures) != 1 || !strings.Contains(summary.Failures[0], "command not found") {
		t.Fatalf("failures = %#v", summary.Failures)
	}
	if len(summary.Files) != 1 {
		t.Fatalf("files = %#v (deduplicated)", summary.Files)
	}
	// An unknown event is counted but not interpreted, so a new event type
	// cannot break memory.
	if !strings.Contains(summary.Events, "some.future.event") {
		t.Fatalf("events = %q", summary.Events)
	}
	var nilDigest *RunDigest
	nilDigest.Observe("run.done", nil)
	if summary := nilDigest.Summary("run_1", ""); summary.RunID != "run_1" {
		t.Fatalf("nil digest summary = %#v", summary)
	}
}

func TestFormatSummaryBoundsTheInput(t *testing.T) {
	summary := RunSummary{
		Task:   strings.Repeat("t", 5000),
		Output: strings.Repeat("o", 9000),
	}
	for index := 0; index < 100; index++ {
		summary.Commands = append(summary.Commands, strings.Repeat("c", 100))
		summary.Failures = append(summary.Failures, "failure")
		summary.Files = append(summary.Files, "/file")
	}
	rendered := FormatSummary(summary)
	if len(rendered) > 20000 {
		t.Fatalf("rendered digest is %d bytes", len(rendered))
	}
	if strings.Count(rendered, "- "+strings.Repeat("c", 100)) != 40 {
		t.Fatalf("commands were not capped: %d", strings.Count(rendered, "- "+strings.Repeat("c", 100)))
	}
	if !strings.Contains(rendered, "[truncated]") {
		t.Fatalf("truncation was not marked: %q", rendered[:200])
	}
}

func TestParseScopeAndID(t *testing.T) {
	if scope, err := ParseScope("Project"); err != nil || scope != ScopeProject {
		t.Fatalf("ParseScope = %q, %v", scope, err)
	}
	if scope, err := ParseScope(""); err != nil || scope != ScopeUser {
		t.Fatalf("ParseScope = %q, %v", scope, err)
	}
	if _, err := ParseScope("galaxy"); err == nil {
		t.Fatal("an unknown scope was accepted")
	}
	// The ID is derived from the text, so wording differences in
	// whitespace do not create two memories.
	a := ID(ScopeUser, "", "Use  make check\nbefore committing")
	b := ID(ScopeUser, "", "use make check before committing")
	if a != b {
		t.Fatalf("IDs differ: %s vs %s", a, b)
	}
	if a == ID(ScopeProject, "/repo", "use make check before committing") {
		t.Fatal("scope is not part of the identity")
	}
}

// scriptedMemoryModel answers Generate with a canned response.
type scriptedMemoryModel struct {
	response string
	err      error
	requests []model.Request
}

func (m *scriptedMemoryModel) Generate(_ context.Context, req model.Request) (*model.Response, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	return &model.Response{Message: model.Message{Role: "assistant", Content: m.response}}, nil
}

func (m *scriptedMemoryModel) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	return nil, errors.New("Stream is not used")
}

// scriptedDistiller returns canned entries.
type scriptedDistiller struct {
	entries []Entry
	err     error
	calls   int
}

func (d *scriptedDistiller) Name() string { return "scripted" }

func (d *scriptedDistiller) Distill(context.Context, RunSummary) ([]Entry, error) {
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	return d.entries, nil
}
