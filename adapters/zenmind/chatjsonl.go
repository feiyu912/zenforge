package zenmind

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/feiyu912/zenforge"
)

// EventLine is the platform event-only JSONL envelope. This adapter does not
// implement Chat Storage V3.1 query, react, plan-execute, system, or submit
// lines and must not be used as a complete chat store.
type EventLine struct {
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	LiveSeq   int64          `json:"liveSeq,omitempty"`
	Event     map[string]any `json:"event"`
	Type      string         `json:"_type"`
}

// ChatJSONLWriter appends platform EventLine records to root/chatId.jsonl.
type ChatJSONLWriter struct {
	root string
}

var eventLineLocks sync.Map

func NewChatJSONLWriter(root string) *ChatJSONLWriter {
	return &ChatJSONLWriter{root: root}
}

// Append writes one already-projected platform event. chatID is explicit
// because a run ID is not a chat identity and cannot safely select the file.
func (w *ChatJSONLWriter) Append(ctx context.Context, chatID string, event StreamEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w == nil || strings.TrimSpace(w.root) == "" {
		return fmt.Errorf("zenmind platform event-line root is required")
	}
	if err := validatePathID("chatID", chatID); err != nil {
		return err
	}
	if err := validatePathID("runID", event.RunID); err != nil {
		return err
	}
	if event.Seq <= 0 {
		return fmt.Errorf("platform event liveSeq must be positive")
	}
	if event.Timestamp < 0 {
		return fmt.Errorf("platform event timestamp must not be negative")
	}
	if strings.TrimSpace(event.Type) == "" {
		return fmt.Errorf("platform event type is required")
	}
	if err := validateEventIdentity(chatID, event); err != nil {
		return err
	}

	payload, err := nestedEventMap(event)
	if err != nil {
		return fmt.Errorf("marshal platform event: %w", err)
	}
	line, err := json.Marshal(EventLine{
		ChatID: chatID, RunID: event.RunID, UpdatedAt: event.Timestamp,
		LiveSeq: event.Seq, Event: payload, Type: "event",
	})
	if err != nil {
		return fmt.Errorf("marshal platform event line: %w", err)
	}
	line = append(line, '\n')

	path := filepath.Join(w.root, chatID+".jsonl")
	lockValue, _ := eventLineLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(w.root, 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing event-line symlink %q", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return err
	}
	return file.Sync()
}

func ReadEventLines(ctx context.Context, root, chatID string) ([]EventLine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("zenmind platform event-line root is required")
	}
	if err := validatePathID("chatID", chatID); err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(root, chatID+".jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var lines []EventLine
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var line EventLine
		if err := decoder.Decode(&line); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if line.Type != "event" || line.ChatID != chatID {
			return nil, fmt.Errorf("invalid platform event line identity for chatID %q", chatID)
		}
		if err := validatePathID("runID", line.RunID); err != nil {
			return nil, err
		}
		if line.LiveSeq <= 0 {
			return nil, fmt.Errorf("platform event line liveSeq must be positive")
		}
		if line.UpdatedAt < 0 {
			return nil, fmt.Errorf("platform event line updatedAt must not be negative")
		}
		if _, ok := line.Event["seq"]; ok {
			return nil, fmt.Errorf("nested platform event repeats seq")
		}
		if _, ok := line.Event["liveSeq"]; ok {
			return nil, fmt.Errorf("nested platform event repeats liveSeq")
		}
		if err := validateIdentityFields(chatID, line.RunID, line.Event); err != nil {
			return nil, err
		}
		timestamp, err := eventTimestamp(line.Event)
		if err != nil {
			return nil, err
		}
		if timestamp != line.UpdatedAt {
			return nil, fmt.Errorf("nested platform event timestamp does not match updatedAt")
		}
		if eventType, ok := line.Event["type"].(string); !ok || strings.TrimSpace(eventType) == "" {
			return nil, fmt.Errorf("nested platform event type is required")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func nestedEventMap(event StreamEvent) (map[string]any, error) {
	event.Payload = cloneMap(event.Payload)
	delete(event.Payload, "seq")
	delete(event.Payload, "liveSeq")
	data, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	delete(payload, "seq")
	delete(payload, "liveSeq")
	return payload, nil
}

func eventTimestamp(event map[string]any) (int64, error) {
	value, ok := event["timestamp"]
	if !ok {
		return 0, fmt.Errorf("nested platform event timestamp is required")
	}
	switch value := value.(type) {
	case json.Number:
		timestamp, err := value.Int64()
		if err != nil {
			return 0, fmt.Errorf("invalid nested platform event timestamp: %w", err)
		}
		return timestamp, nil
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("nested platform event timestamp must be an integer")
		}
		return int64(value), nil
	case int64:
		return value, nil
	default:
		return 0, fmt.Errorf("nested platform event timestamp must be an integer")
	}
}

func validateEventIdentity(chatID string, event StreamEvent) error {
	return validateIdentityFields(chatID, event.RunID, event.Payload)
}

func validateIdentityFields(chatID, runID string, fields map[string]any) error {
	for key, expected := range map[string]string{"chatId": chatID, "runId": runID} {
		value, ok := fields[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok || text != expected {
			return fmt.Errorf("platform event %s does not match envelope identity", key)
		}
	}
	return nil
}

func validatePathID(name, id string) error {
	if id == "" || strings.TrimSpace(id) != id || id == "." || id == ".." || filepath.IsAbs(id) || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("invalid %s %q", name, id)
	}
	for _, r := range id {
		if unicode.IsControl(r) || r == 0 {
			return fmt.Errorf("invalid %s %q", name, id)
		}
	}
	return nil
}

// LegacyChatJSONLWriter preserves the pre-platform trace API and layout.
// Deprecated: use ChatJSONLWriter, which requires an explicit chatID.
type LegacyChatJSONLWriter struct {
	root   string
	mapper Mapper
	mu     sync.Mutex
}

// NewLegacyChatJSONLWriter constructs the deprecated root/runId/chat.jsonl writer.
func NewLegacyChatJSONLWriter(root string, mapper Mapper) *LegacyChatJSONLWriter {
	if mapper.Types == nil {
		mapper = NewMapper()
	}
	return &LegacyChatJSONLWriter{root: root, mapper: mapper}
}

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

func (w *LegacyChatJSONLWriter) Append(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w == nil || w.root == "" {
		return fmt.Errorf("zenmind legacy chat jsonl root is required")
	}
	runID := event.RunID()
	if err := validatePathID("runID", runID); err != nil {
		return err
	}
	mapped := w.mapper.Map(event)
	record := ChatRecord{Version: "zenmind.chat_trace.v1", RunID: runID, Seq: event.Seq, Type: mapped.Type, Source: mapped.Source, Timestamp: event.Timestamp, Payload: cloneMap(mapped.Payload), WrittenAt: time.Now().UTC()}
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

// ReadChatRecords reads the deprecated root/runId/chat.jsonl trace format.
// Deprecated: use ReadEventLines.
func ReadChatRecords(ctx context.Context, root, runID string) ([]ChatRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == "" {
		return nil, fmt.Errorf("zenmind legacy chat jsonl root is required")
	}
	if err := validatePathID("runID", runID); err != nil {
		return nil, err
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
	var records []ChatRecord
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record ChatRecord
		if err := decoder.Decode(&record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}
