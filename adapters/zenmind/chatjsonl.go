package zenmind

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
)

type ChatRecord struct {
	Version   string         `json:"version"`
	RunID     string         `json:"runId"`
	Seq       int64          `json:"seq"`
	Type      string         `json:"type"`
	Source    string         `json:"source"`
	Timestamp int64          `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
	WrittenAt time.Time      `json:"writtenAt"`
}

type ChatJSONLWriter struct {
	root   string
	mapper Mapper
	mu     sync.Mutex
}

func NewChatJSONLWriter(root string, mapper Mapper) *ChatJSONLWriter {
	if mapper.Types == nil {
		mapper = NewMapper()
	}
	return &ChatJSONLWriter{root: root, mapper: mapper}
}

func (w *ChatJSONLWriter) Append(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w == nil || w.root == "" {
		return fmt.Errorf("zenmind chat jsonl root is required")
	}
	runID := event.RunID()
	if runID == "" {
		return fmt.Errorf("event runId is required")
	}
	mapped := w.mapper.Map(event)
	record := ChatRecord{
		Version:   "zenmind.chat_trace.v1",
		RunID:     runID,
		Seq:       event.Seq,
		Type:      mapped.Type,
		Source:    mapped.Source,
		Timestamp: event.Timestamp,
		Payload:   cloneMap(mapped.Payload),
		WrittenAt: time.Now().UTC(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	runDir := filepath.Join(w.root, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(runDir, "chat.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func ReadChatRecords(ctx context.Context, root, runID string) ([]ChatRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == "" {
		return nil, fmt.Errorf("zenmind chat jsonl root is required")
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}
	file, err := os.Open(filepath.Join(root, runID, "chat.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var out []ChatRecord
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record ChatRecord
		if err := decoder.Decode(&record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}
