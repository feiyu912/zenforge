package zenmind

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestChatJSONLWriterMatchesPlatformGolden(t *testing.T) {
	// Source: agent-platform commit 1893edb51b8dc691ae974cea2719a835e0e21de4,
	// internal/chat/types.go:139 and internal/chat/events_writer.go:12.
	root := t.TempDir()
	writer := NewChatJSONLWriter(root)
	event := StreamEvent{Seq: 7, Type: "content.delta", RunID: "run-1", Timestamp: 42, Payload: map[string]any{"contentId": "run-1_c_1", "delta": "hello"}}
	if err := writer.Append(context.Background(), "chat-1", event); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "chat-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/platform/chat_event_line.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("event line differs byte-for-byte\ngot:  %s\nwant: %s", got, want)
	}
	if bytes.Count(got, []byte(`"liveSeq"`)) != 1 || bytes.Contains(got, []byte(`"event":{"seq"`)) {
		t.Fatalf("nested event repeats replay cursor: %s", got)
	}
}

func TestChatJSONLWriterRemovesNestedReplayCursors(t *testing.T) {
	root := t.TempDir()
	event := StreamEvent{Seq: 9, Type: "content.delta", RunID: "run-1", Timestamp: 43, Payload: map[string]any{"contentId": "content-1", "delta": "x", "seq": int64(8), "liveSeq": int64(9)}}
	if err := NewChatJSONLWriter(root).Append(context.Background(), "chat-1", event); err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "chat-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte(`"liveSeq"`)) != 1 || bytes.Count(data, []byte(`"seq"`)) != 0 {
		t.Fatalf("replay cursor leaked into nested event: %s", data)
	}
}

func TestChatJSONLWriterFlatPathAppendAndRead(t *testing.T) {
	root := t.TempDir()
	writer := NewChatJSONLWriter(root)
	for seq, eventType := range []string{"run.start", "run.complete"} {
		event := StreamEvent{Seq: int64(seq + 1), Type: eventType, RunID: "run-a", Timestamp: int64(100 + seq), Payload: map[string]any{"runId": "run-a"}}
		if err := writer.Append(context.Background(), "chat-a", event); err != nil {
			t.Fatalf("Append %d: %v", seq, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "chat-a.jsonl")); err != nil {
		t.Fatalf("flat chat path missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "run-a", "chat.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("legacy run path exists, err=%v", err)
	}
	lines, err := ReadEventLines(context.Background(), root, "chat-a")
	if err != nil {
		t.Fatalf("ReadEventLines: %v", err)
	}
	if len(lines) != 2 || lines[0].LiveSeq != 1 || lines[1].LiveSeq != 2 || lines[0].UpdatedAt != 100 || lines[1].UpdatedAt != 101 {
		t.Fatalf("unexpected lines: %#v", lines)
	}
	nested := lines[0].Event
	if _, ok := nested["seq"]; ok {
		t.Fatalf("nested event contains seq: %#v", nested)
	}
}

func TestReadEventLinesRejectsInvalidUpdatedAtAndLiveSeq(t *testing.T) {
	for name, line := range map[string]string{
		"zero-live-seq":       `{"chatId":"chat-1","runId":"run-1","updatedAt":42,"event":{"timestamp":42,"type":"run.start"},"_type":"event"}` + "\n",
		"timestamp-mismatch":  `{"chatId":"chat-1","runId":"run-1","updatedAt":41,"liveSeq":1,"event":{"timestamp":42,"type":"run.start"},"_type":"event"}` + "\n",
		"negative-updated-at": `{"chatId":"chat-1","runId":"run-1","updatedAt":-1,"liveSeq":1,"event":{"timestamp":-1,"type":"run.start"},"_type":"event"}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "chat-1.jsonl"), []byte(line), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadEventLines(context.Background(), root, "chat-1"); err == nil {
				t.Fatal("ReadEventLines accepted invalid cursor or timestamp")
			}
		})
	}
}

func TestReadEventLinesJSONStream(t *testing.T) {
	first := `{"chatId":"chat-1","runId":"run-1","updatedAt":41,"liveSeq":1,"event":{"timestamp":41,"type":"run.start"},"_type":"event"}`
	second := `{"chatId":"chat-1","runId":"run-1","updatedAt":42,"liveSeq":2,"event":{"timestamp":42,"type":"run.complete"},"_type":"event"}`
	tests := []struct {
		name      string
		contents  string
		wantCount int
		wantError bool
	}{
		{name: "valid-multiple-lines", contents: first + "\n" + second + "\n", wantCount: 2},
		{name: "truncated-last-line", contents: first + "\n" + second[:len(second)-1], wantError: true},
		{name: "trailing-garbage", contents: first + "\n" + second + "\nnot-json\n", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "chat-1.jsonl"), []byte(test.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			lines, err := ReadEventLines(context.Background(), root, "chat-1")
			if test.wantError {
				if err == nil {
					t.Fatalf("ReadEventLines returned %d lines without reporting malformed trailing data", len(lines))
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadEventLines: %v", err)
			}
			if len(lines) != test.wantCount {
				t.Fatalf("line count = %d, want %d", len(lines), test.wantCount)
			}
		})
	}
}

func TestChatJSONLWriterRejectsUnsafeOrMismatchedIdentity(t *testing.T) {
	root := t.TempDir()
	writer := NewChatJSONLWriter(root)
	unsafe := []string{"", ".", "..", " nested", "nested/run", `nested\run`, filepath.Join(string(filepath.Separator), "tmp", "id"), "line\nbreak"}
	for _, id := range unsafe {
		t.Run(fmt.Sprintf("chat-%q", id), func(t *testing.T) {
			event := StreamEvent{Seq: 1, Type: "run.start", RunID: "run-1", Payload: map[string]any{"runId": "run-1"}}
			if err := writer.Append(context.Background(), id, event); err == nil {
				t.Fatalf("Append accepted unsafe chatID %q", id)
			}
		})
		t.Run(fmt.Sprintf("run-%q", id), func(t *testing.T) {
			event := StreamEvent{Seq: 1, Type: "run.start", RunID: id}
			if err := writer.Append(context.Background(), "chat-1", event); err == nil {
				t.Fatalf("Append accepted unsafe runID %q", id)
			}
		})
	}
	for name, event := range map[string]StreamEvent{
		"chat": {Seq: 1, Type: "run.start", RunID: "run-1", Payload: map[string]any{"chatId": "chat-2"}},
		"run":  {Seq: 1, Type: "run.start", RunID: "run-1", Payload: map[string]any{"runId": "run-2"}},
	} {
		t.Run(name+"-mismatch", func(t *testing.T) {
			if err := writer.Append(context.Background(), "chat-1", event); err == nil {
				t.Fatal("Append accepted mismatched identity")
			}
		})
	}
}

func TestChatJSONLWriterMultipleInstancesAppendAtomically(t *testing.T) {
	root := t.TempDir()
	writers := []*ChatJSONLWriter{NewChatJSONLWriter(root), NewChatJSONLWriter(root), NewChatJSONLWriter(root), NewChatJSONLWriter(root)}
	const count = 80
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := StreamEvent{Seq: int64(i + 1), Type: "content.delta", RunID: "run-concurrent", Timestamp: int64(i), Payload: map[string]any{"contentId": "content-1", "delta": fmt.Sprint(i)}}
			errs <- writers[i%len(writers)].Append(context.Background(), "chat-concurrent", event)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Append: %v", err)
		}
	}
	lines, err := ReadEventLines(context.Background(), root, "chat-concurrent")
	if err != nil {
		t.Fatalf("ReadEventLines: %v", err)
	}
	if len(lines) != count {
		t.Fatalf("line count = %d, want %d", len(lines), count)
	}
	seen := make(map[int64]bool, count)
	for _, line := range lines {
		seen[line.LiveSeq] = true
	}
	if len(seen) != count {
		t.Fatalf("unique liveSeq count = %d, want %d", len(seen), count)
	}
}

func TestReadEventLinesMissingChatReturnsEmpty(t *testing.T) {
	lines, err := ReadEventLines(context.Background(), t.TempDir(), "missing")
	if err != nil {
		t.Fatalf("ReadEventLines: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %#v, want empty", lines)
	}
}
