package zenmind

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestChatJSONLWriterProjectsMappedEvents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writer := NewChatJSONLWriter(root, NewMapper())

	event := zenforge.NewEvent(zenforge.EventModelDelta, "run_chat", map[string]any{"textDelta": "hello"}).WithSeq(3)
	event.Timestamp = 42
	if err := writer.Append(ctx, event); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	records, err := ReadChatRecords(ctx, root, "run_chat")
	if err != nil {
		t.Fatalf("ReadChatRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Version != "zenmind.chat_trace.v1" || record.Type != "content.delta" || record.Source != string(zenforge.EventModelDelta) {
		t.Fatalf("unexpected record envelope: %#v", record)
	}
	if record.RunID != "run_chat" || record.Seq != 3 || record.Timestamp != 42 {
		t.Fatalf("unexpected record identifiers: %#v", record)
	}
	if record.Payload["textDelta"] != "hello" || record.Payload["runId"] != "run_chat" {
		t.Fatalf("payload not preserved: %#v", record.Payload)
	}
	if record.WrittenAt.IsZero() {
		t.Fatalf("WrittenAt was not set")
	}
}

func TestReadChatRecordsMissingRunReturnsEmpty(t *testing.T) {
	records, err := ReadChatRecords(context.Background(), t.TempDir(), "missing")
	if err != nil {
		t.Fatalf("ReadChatRecords returned error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %#v, want empty", records)
	}
}

func TestChatJSONLRejectsUnsafeRunIDs(t *testing.T) {
	root := t.TempDir()
	writer := NewChatJSONLWriter(root, NewMapper())
	for _, runID := range []string{".", "..", "nested/run", `nested\run`, filepath.Join(string(filepath.Separator), "tmp", "run")} {
		t.Run(fmt.Sprintf("%q", runID), func(t *testing.T) {
			event := zenforge.NewEvent(zenforge.EventModelDelta, runID, nil)
			if err := writer.Append(context.Background(), event); err == nil {
				t.Fatalf("Append accepted unsafe runID %q", runID)
			}
			if _, err := ReadChatRecords(context.Background(), root, runID); err == nil {
				t.Fatalf("ReadChatRecords accepted unsafe runID %q", runID)
			}
		})
	}
}
