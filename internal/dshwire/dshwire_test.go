package dshwire

import (
	"testing"

	"github.com/feiyu912/zenforge"
)

func event(seq int64, eventType zenforge.EventType, data map[string]any) zenforge.Event {
	return zenforge.Event{Seq: seq, Type: eventType, Timestamp: 1700000000000 + seq, Payload: zenforge.EventData(data)}
}

// aTurn is one ordinary turn: the prompt, one step that streams reasoning and
// text, a tool call and its result, a second step that answers, and the close.
func aTurn() []zenforge.Event {
	return []zenforge.Event{
		event(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
		event(2, zenforge.EventCheckpointCreated, map[string]any{"checkpointSeq": int64(1)}),
		event(3, zenforge.EventStepStarted, map[string]any{"step": 1}),
		event(4, zenforge.EventModelStarted, map[string]any{"step": 1, "attemptId": "attempt-1"}),
		event(5, zenforge.EventModelReasoning, map[string]any{"step": 1, "textDelta": "thinking"}),
		event(6, zenforge.EventModelDelta, map[string]any{"step": 1, "textDelta": "Let me "}),
		event(7, zenforge.EventModelDelta, map[string]any{"step": 1, "textDelta": "check."}),
		event(8, zenforge.EventModelUsage, map[string]any{"step": 1, "usage": map[string]any{
			"promptTokens": 11, "completionTokens": 7, "totalTokens": 18,
		}}),
		event(9, zenforge.EventModelDone, map[string]any{"step": 1, "toolCallCount": 1}),
		event(10, zenforge.EventToolCall, map[string]any{
			"toolCallId": "call-1", "toolName": "read_file", "arguments": `{"path":"a.go"}`,
		}),
		event(11, zenforge.EventToolResult, map[string]any{
			"toolCallId": "call-1", "toolName": "read_file", "output": "package a", "exitCode": 0,
		}),
		event(12, zenforge.EventStepDone, map[string]any{"step": 1}),
		event(13, zenforge.EventStepStarted, map[string]any{"step": 2}),
		event(14, zenforge.EventModelStarted, map[string]any{"step": 2, "attemptId": "attempt-2"}),
		event(15, zenforge.EventModelDelta, map[string]any{"step": 2, "textDelta": "It is fine."}),
		event(16, zenforge.EventModelDone, map[string]any{"step": 2, "toolCallCount": 0}),
		event(17, zenforge.EventStepDone, map[string]any{"step": 2}),
		event(18, zenforge.EventRunDone, map[string]any{"output": "It is fine."}),
	}
}

func project(t *testing.T, events []zenforge.Event) []Event {
	t.Helper()
	return Project(events, Identity{Provider: "openai", Model: "qwen-plus"}).Events
}

func findByType(t *testing.T, events []Event, eventType string) Event {
	t.Helper()
	for _, projected := range events {
		if projected.Type == eventType {
			return projected
		}
	}
	t.Fatalf("no projected %q in %v", eventType, eventTypes(events))
	return Event{}
}

func eventTypes(events []Event) []string {
	names := make([]string, 0, len(events))
	for _, projected := range events {
		names = append(names, projected.Type)
	}
	return names
}

// TestProjectionKeepsOneRecordPerEventAndTheDurableSeq is the property every
// other rule rests on: the console's cursor, its throughSeq paging and the live
// tail's afterSeq all speak the log's own sequence, and the console's session
// format documents contiguous sequence numbers as an invariant.
func TestProjectionKeepsOneRecordPerEventAndTheDurableSeq(t *testing.T) {
	events := aTurn()
	projected := project(t, events)
	if len(projected) != len(events) {
		t.Fatalf("projected %d records for %d durable events: %v", len(projected), len(events), eventTypes(projected))
	}
	for index, wire := range projected {
		if wire.Seq != events[index].Seq {
			t.Fatalf("record %d has seq %d, want the durable seq %d", index, wire.Seq, events[index].Seq)
		}
		if wire.Time != events[index].Timestamp {
			t.Fatalf("record %d has time %d, want the durable time %d", index, wire.Time, events[index].Timestamp)
		}
	}
}

// TestProjectionRendersTheTurnAsAConsoleTranscript pins the mapping that makes a
// conversation visible: the prompt becomes a user message, each step's streamed
// deltas settle into one assistant message, and the tool call pairs with its
// result.
func TestProjectionRendersTheTurnAsAConsoleTranscript(t *testing.T) {
	projected := project(t, aTurn())

	if start := findByType(t, projected, "step/start"); start.Data["step"] != 1 || start.Data["turn"] != Turn {
		t.Fatalf("step/start data = %v, want turn 1 step 1", start.Data)
	}

	prompt := findByType(t, projected, "user/message")
	message, ok := prompt.Data["id"].(string)
	if !ok || message == "" {
		t.Fatalf("user message has no stable id: %v", prompt.Data)
	}
	if prompt.Data["role"] != "user" {
		t.Fatalf("user message role = %v", prompt.Data["role"])
	}
	if got := prompt.Data["surfaceOp"]; got != nil {
		t.Fatalf("surfaceOp leaked into the payload: %v", got)
	}
	text := firstText(t, prompt.Data)
	if text != "hello" {
		t.Fatalf("user message text = %q, want hello", text)
	}

	assistant := findByType(t, projected, "assistant/message")
	if assistant.Data["turn"] != Turn || assistant.Data["step"] != 1 {
		t.Fatalf("assistant message data = %v, want turn 1 step 1", assistant.Data)
	}
	assistantMessage, ok := assistant.Data["message"].(map[string]any)
	if !ok {
		t.Fatalf("assistant/message carries no message: %v", assistant.Data)
	}
	source, ok := assistantMessage["source"].(map[string]any)
	if !ok || source["kind"] != "model" || source["provider"] != "openai" || source["model"] != "qwen-plus" {
		t.Fatalf("assistant provenance = %v, want the injected identity", assistantMessage["source"])
	}
	if assistantMessage["role"] != "assistant" {
		t.Fatalf("assistant role = %v", assistantMessage["role"])
	}
	content, ok := assistantMessage["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("assistant content = %v, want a reasoning block then a text block", assistantMessage["content"])
	}
	reasoning := content[0].(map[string]any)
	answer := content[1].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["text"] != "thinking" {
		t.Fatalf("first block = %v, want the reasoning delta", reasoning)
	}
	if answer["type"] != "text" || answer["text"] != "Let me check." {
		t.Fatalf("second block = %v, want the merged text deltas", answer)
	}
	usage, ok := assistant.Data["usage"].(map[string]any)
	if !ok || usage["inputTokens"] != 11 || usage["outputTokens"] != 7 || usage["totalTokens"] != 18 {
		t.Fatalf("usage = %v, want the console's token names", assistant.Data["usage"])
	}

	call := findByType(t, projected, "tool/call")
	if call.Data["callId"] != "call-1" || call.Data["name"] != "read_file" || call.Data["arguments"] != `{"path":"a.go"}` {
		t.Fatalf("tool/call data = %v", call.Data)
	}
	result := findByType(t, projected, "tool/result")
	resultMessage, ok := result.Data["message"].(map[string]any)
	if !ok {
		t.Fatalf("tool/result carries no message: %v", result.Data)
	}
	resultSource, ok := resultMessage["source"].(map[string]any)
	if !ok || resultSource["kind"] != "tool" || resultSource["callId"] != "call-1" {
		t.Fatalf("tool/result source = %v, want the call correlation", resultMessage["source"])
	}
	block := resultMessage["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool-result" || block["toolCallId"] != "call-1" {
		t.Fatalf("tool/result block = %v", block)
	}
	if got := firstText(t, map[string]any{"content": block["content"]}); got != "package a" {
		t.Fatalf("tool result text = %q, want the tool output", got)
	}
	if result.Data["step"] != 1 {
		t.Fatalf("tool/result step = %v, want the step its call opened", result.Data["step"])
	}
}

// TestProjectionMarksOnlySurfaceEventsAndOnlyUnknownNames holds the two wire
// rules the client enforces: surfaceOp appears exactly on the four
// message-producing types, and an unknown name carries the ignorable marker.
func TestProjectionMarksOnlySurfaceEventsAndOnlyUnknownNames(t *testing.T) {
	for _, projected := range project(t, aTurn()) {
		eligible := SurfaceEligibleTypes[projected.Type]
		if eligible && projected.SurfaceOp != "append" {
			t.Fatalf("%s is surface-eligible but carries surfaceOp %q", projected.Type, projected.SurfaceOp)
		}
		if !eligible && projected.SurfaceOp != "" {
			t.Fatalf("%s is not surface-eligible but carries surfaceOp %q", projected.Type, projected.SurfaceOp)
		}
		known := KnownEventTypes[projected.Type]
		if projected.Ignorable && known {
			t.Fatalf("%s is a console event but was marked ignorable", projected.Type)
		}
		if !known && !projected.Ignorable {
			t.Fatalf("%s is outside the console's vocabulary but carries no ignorable marker", projected.Type)
		}
		if projected.Ignorable && projected.SurfaceOp != "" {
			t.Fatalf("%s is ignorable and still carries surfaceOp", projected.Type)
		}
	}
}

// TestProjectionKeepsEveryDurableEventReadable pins that the projection is a
// projection and not a filter: an event with no console meaning keeps its own
// name and payload, marked as deliberately skipped, so the wire still carries
// the whole log.
func TestProjectionKeepsEveryDurableEventReadable(t *testing.T) {
	projected := project(t, aTurn())
	checkpoint := findByType(t, projected, string(zenforge.EventCheckpointCreated))
	if !checkpoint.Ignorable {
		t.Fatal("a passthrough event is not marked ignorable")
	}
	if checkpoint.Data["checkpointSeq"] == nil {
		t.Fatalf("passthrough payload was dropped: %v", checkpoint.Data)
	}
	delta := findByType(t, projected, string(zenforge.EventModelDelta))
	if delta.Data["textDelta"] == "" {
		t.Fatalf("the durable delta lost its text: %v", delta.Data)
	}
}

// TestProjectionSettlesAnEmptyStepAsAnAttempt covers a step that produced no
// model-visible content: it is log-only history, not an empty chat bubble.
func TestProjectionSettlesAnEmptyStepAsAnAttempt(t *testing.T) {
	projected := project(t, []zenforge.Event{
		event(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
		event(2, zenforge.EventStepStarted, map[string]any{"step": 1}),
		event(3, zenforge.EventModelStarted, map[string]any{"step": 1}),
		event(4, zenforge.EventModelDone, map[string]any{"step": 1}),
	})
	attempt := findByType(t, projected, "assistant/attempt")
	if attempt.SurfaceOp != "" || attempt.Ignorable {
		t.Fatalf("assistant/attempt = %+v, want a known, non-surface record", attempt)
	}
	if _, ok := attempt.Data["stream"].([]any); !ok {
		t.Fatalf("assistant/attempt has no stream: %v", attempt.Data)
	}
	for _, wire := range projected {
		if wire.Type == "assistant/message" {
			t.Fatal("an empty step produced an assistant message")
		}
	}
}

// TestProjectionMarksAFailedRunAsATurnError pins the three ways a run can end,
// because the console renders the reason rather than the raw event.
func TestProjectionMarksAFailedRunAsATurnError(t *testing.T) {
	projected := project(t, []zenforge.Event{
		event(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
		event(2, zenforge.EventRunError, map[string]any{"error": "dial tcp: refused"}),
	})
	end := findByType(t, projected, "turn/end")
	reason := end.Data["reason"].(map[string]any)
	if reason["kind"] != "error" {
		t.Fatalf("turn/end reason = %v, want an error reason", reason)
	}
	failure := reason["error"].(map[string]any)
	if failure["message"] != "dial tcp: refused" || failure["code"] != "UNKNOWN" {
		t.Fatalf("turn/end failure = %v, want the host's message and a neutral code", failure)
	}
	if end.SurfaceOp != "" || end.Ignorable {
		t.Fatalf("turn/end = %+v, want a known, non-surface record", end)
	}

	cancelled := project(t, []zenforge.Event{event(1, zenforge.EventRunCancelled, map[string]any{"error": "cancelled"})})
	if kind := cancelled[0].Data["reason"].(map[string]any)["kind"]; kind != "aborted" {
		t.Fatalf("cancelled reason = %v, want aborted", kind)
	}
	done := project(t, []zenforge.Event{event(1, zenforge.EventRunDone, map[string]any{})})
	if kind := done[0].Data["reason"].(map[string]any)["kind"]; kind != "completed" {
		t.Fatalf("done reason = %v, want completed", kind)
	}
}

// TestProjectionContinuesAfterASnapshot is the live-tail property: the follower
// snapshots the log, then projects each later event through the same projector.
// The assistant message must carry the deltas from both halves.
func TestProjectionContinuesAfterASnapshot(t *testing.T) {
	events := []zenforge.Event{
		event(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
		event(2, zenforge.EventStepStarted, map[string]any{"step": 1}),
		event(3, zenforge.EventModelStarted, map[string]any{"step": 1}),
		event(4, zenforge.EventModelDelta, map[string]any{"step": 1, "textDelta": "Hello"}),
	}
	projection := Project(events, Identity{})
	projection.Append(event(5, zenforge.EventModelDelta, map[string]any{"step": 1, "textDelta": " there"}))
	projection.Append(event(6, zenforge.EventModelDone, map[string]any{"step": 1}))
	assistant := findByType(t, projection.Events, "assistant/message")
	if got := firstText(t, assistant.Data); got != "Hello there" {
		t.Fatalf("assistant text = %q, want the deltas from both halves", got)
	}
	if len(projection.Events) != 6 {
		t.Fatalf("events = %d, want 6", len(projection.Events))
	}
}

// TestProjectionWindowsTheNewestRecords covers the snapshot window: the page and
// the follow snapshot serve the newest records and say whether anything older
// was left out.
func TestProjectionWindowsTheNewestRecords(t *testing.T) {
	projection := Project(aTurn(), Identity{})
	window, hasMore := projection.Window(3)
	if !hasMore {
		t.Fatal("hasMore = false for a window that cut the log")
	}
	if len(window) != 3 {
		t.Fatalf("window = %d records, want 3", len(window))
	}
	if window[0].Seq != 16 || window[2].Seq != 18 {
		t.Fatalf("window seqs = %d..%d, want the newest 16..18", window[0].Seq, window[2].Seq)
	}
	all, hasMore := projection.Window(0)
	if hasMore || len(all) != len(projection.Events) {
		t.Fatalf("an unbounded window returned %d records (hasMore %v)", len(all), hasMore)
	}
}

func firstText(t *testing.T, data map[string]any) string {
	t.Helper()
	if message, ok := data["message"].(map[string]any); ok {
		data = message
	}
	content, ok := data["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content blocks in %v", data)
	}
	block := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}

// TestProjectionAvoidsTheConsoleVocabularyCollision asserts the assumption the
// pass-through arm rests on: no zenforge event name is one the console knows, so
// marking every passthrough record ignorable can never make the console skip an
// event it is supposed to read.
func TestProjectionAvoidsTheConsoleVocabularyCollision(t *testing.T) {
	names := zenforgeEventNames(t)
	if len(names) < 40 {
		t.Fatalf("enumerated only %d zenforge event names; the list is incomplete", len(names))
	}
	for _, name := range names {
		if KnownEventTypes[name] {
			t.Fatalf("zenforge event %q is a console event type; the passthrough arm must not mark it ignorable", name)
		}
		if SurfaceEligibleTypes[name] {
			t.Fatalf("zenforge event %q is surface-eligible; it needs an explicit mapping", name)
		}
	}
}

// zenforgeEventNames reads the host's event vocabulary out of the package's own
// source rather than a hand-kept copy, so a new event type cannot slip past the
// collision check.
func zenforgeEventNames(t *testing.T) []string {
	t.Helper()
	source := readFile(t, hostEventsPath)
	names := map[string]bool{}
	for _, name := range eventNamePattern.FindAllStringSubmatch(source, -1) {
		names[name[1]] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	return ordered
}
