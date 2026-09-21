package dshwire

import (
	"reflect"
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

// TestProjectionNumbersTheConsoleSequence is the property every other rule rests
// on: the console's cursor, its throughSeq paging and the live tail's afterSeq
// all speak the served sequence, and the console's session format documents
// contiguous sequence numbers as an invariant. The served sequence counts
// records, not durable events: a record the console could only skip takes a
// window slot and a number it never needed (ADR 0117).
func TestProjectionNumbersTheConsoleSequence(t *testing.T) {
	events := aTurn()
	projected := project(t, events)
	if len(projected) >= len(events) {
		t.Fatalf("projected %d records for %d durable events: %v", len(projected), len(events), eventTypes(projected))
	}
	for index, record := range projected {
		if record.Seq != int64(index+1) {
			t.Fatalf("record %d (%s) cites seq %d, want %d", index, record.Type, record.Seq, index+1)
		}
		if record.Time == 0 {
			t.Fatalf("record %d (%s) lost its durable time", index, record.Type)
		}
	}
	// A turn that projects to records must still cite the time of the durable
	// event each record came from.
	source := Project(events, Identity{}).Source
	if gone := len(events) - len(source); gone == 0 {
		t.Fatal("no durable events were dropped, so this case no longer exercises the filter")
	}
}

// TestProjectionRendersTheTurnAsAConsoleTranscript pins the mapping that makes a
// conversation visible: the prompt becomes a user message, each step's streamed
// deltas settle into one assistant message, and the tool call pairs with its
// result.
func TestProjectionRendersTheTurnAsAConsoleTranscript(t *testing.T) {
	projected := project(t, aTurn())

	if start := findByType(t, projected, "step/start"); start.Data["step"] != 1 || start.Data["turn"] != DefaultTurn {
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
	if assistant.Data["turn"] != DefaultTurn || assistant.Data["step"] != 1 {
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

// TestProjectionMarksOnlySurfaceEvents holds the wire rule the client enforces:
// surfaceOp appears exactly on the four message-producing types, and every record
// served is one the console knows.
func TestProjectionMarksOnlySurfaceEvents(t *testing.T) {
	for _, projected := range project(t, aTurn()) {
		eligible := SurfaceEligibleTypes[projected.Type]
		if eligible && projected.SurfaceOp != "append" {
			t.Fatalf("%s is surface-eligible but carries surfaceOp %q", projected.Type, projected.SurfaceOp)
		}
		if !eligible && projected.SurfaceOp != "" {
			t.Fatalf("%s is not surface-eligible but carries surfaceOp %q", projected.Type, projected.SurfaceOp)
		}
		if !KnownEventTypes[projected.Type] {
			t.Fatalf("%s is outside the console's vocabulary but was served", projected.Type)
		}
	}
}

// TestProjectionServesTheConsoleItsOwnWindow pins what the operator sees: a
// turn's window holds the console's events and nothing else. The host's
// bookkeeping and the model deltas are absent, which is upstream's shape and what
// keeps a conversation from filling the console's window with records it can only
// skip (ADR 0117).
func TestProjectionServesTheConsoleItsOwnWindow(t *testing.T) {
	projected := project(t, aTurn())
	want := []string{
		"user/message", "step/start", "assistant/message", "tool/call",
		"tool/result", "step/end", "step/start", "assistant/message",
		"step/end", "turn/end",
	}
	if got := eventTypes(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("window = %v, want %v", got, want)
	}
}

// TestProjectionKeepsTheDeltasInTheMessageStream pins that dropping the delta
// records loses nothing the console reads: the settled message carries the same
// deltas, byte-exact and timed, in its compact `stream`, which is where the
// console's trajectory view reads the answer from.
func TestProjectionKeepsTheDeltasInTheMessageStream(t *testing.T) {
	message := findByType(t, project(t, aTurn()), "assistant/message")
	stream, ok := message.Data["stream"].([]any)
	if !ok || len(stream) != 2 {
		t.Fatalf("assistant/message stream = %v, want two block timelines", message.Data["stream"])
	}
	reasoning := stream[0].(map[string]any)
	if reasoning["type"] != "reasoning-chunks" || reasoning["index"] != 0 {
		t.Fatalf("first timeline = %v, want reasoning-chunks at block 0", reasoning)
	}
	if got := reasoning["texts"]; !reflect.DeepEqual(got, []any{"thinking"}) {
		t.Fatalf("reasoning texts = %v, want the reasoning delta", got)
	}
	if got := reasoning["dt"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("reasoning dt = %v, want no gaps for one delta", got)
	}
	text := stream[1].(map[string]any)
	if text["type"] != "text-chunks" || text["index"] != 1 {
		t.Fatalf("second timeline = %v, want text-chunks at block 1", text)
	}
	if got := text["texts"]; !reflect.DeepEqual(got, []any{"Let me ", "check."}) {
		t.Fatalf("text texts = %v, want both text deltas in order", got)
	}
	if got := text["dt"]; !reflect.DeepEqual(got, []any{int64(1)}) {
		t.Fatalf("text dt = %v, want one gap of 1ms", got)
	}
	if text["time0"] != int64(1700000000006) {
		t.Fatalf("text time0 = %v, want the first delta's time", text["time0"])
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
	if attempt.SurfaceOp != "" {
		t.Fatalf("assistant/attempt = %+v, want a non-surface record", attempt)
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
	if end.SurfaceOp != "" {
		t.Fatalf("turn/end = %+v, want a non-surface record", end)
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
	// Four records: the prompt, step/start, and the one settled message. The
	// deltas and the model events between them are not records (ADR 0117).
	if len(projection.Events) != 3 {
		t.Fatalf("records = %d, want 3: %v", len(projection.Events), eventTypes(projection.Events))
	}
	if assistant.Seq != 3 {
		t.Fatalf("assistant message cites seq %d, want the third record", assistant.Seq)
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
	newest := len(projection.Events)
	if window[0].Seq != int64(newest-2) || window[2].Seq != int64(newest) {
		t.Fatalf("window seqs = %d..%d, want the newest %d..%d", window[0].Seq, window[2].Seq, newest-2, newest)
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
// vocabulary filter rests on: no zenforge event name is one the console knows, so
// a host event can never be served under a name the console reads as its own
// meaning. A zenforge event whose name collides would need an explicit mapping
// rather than the pass-through arm.
func TestProjectionAvoidsTheConsoleVocabularyCollision(t *testing.T) {
	names := zenforgeEventNames(t)
	if len(names) < 40 {
		t.Fatalf("enumerated only %d zenforge event names; the list is incomplete", len(names))
	}
	for _, name := range names {
		if KnownEventTypes[name] {
			t.Fatalf("zenforge event %q is a console event type; it must be mapped, not passed through", name)
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

// TestProjectionCarriesThePromptsRequestIdentity covers the identity the console
// retires its local submission echo by: a durable user message whose
// `source.rpcId` equals the requestId the prompt RPC carried (ADR 0111).
func TestProjectionCarriesThePromptsRequestIdentity(t *testing.T) {
	withIdentity := project(t, []zenforge.Event{
		event(1, zenforge.EventRunStarted, map[string]any{"input": "hello", "promptId": "req-1"}),
	})
	prompt := findByType(t, withIdentity, "user/message")
	if got := sourceRPCID(t, prompt); got != "req-1" {
		t.Fatalf("prompt source rpcId = %q, want the identity the caller submitted", got)
	}

	// A run nobody labelled keeps the source shape it always had: no identity,
	// and a console with no echo to retire.
	without := project(t, []zenforge.Event{
		event(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
	})
	source, ok := findByType(t, without, "user/message").Data["source"].(map[string]any)
	if !ok {
		t.Fatal("user/message has no source")
	}
	if _, present := source["rpcId"]; present {
		t.Fatalf("source = %v, want no rpcId without a caller identity", source)
	}

	// A queued turn: the harness names the text "message", and the identity the
	// host passed as the steer id is the prompt's requestId.
	queued := project(t, []zenforge.Event{
		event(1, zenforge.EventRequestSteer, map[string]any{"steerId": "req-2", "message": "and this"}),
	})
	steer := findByType(t, queued, "user/message")
	if got := sourceRPCID(t, steer); got != "req-2" {
		t.Fatalf("steer source rpcId = %q, want the queued prompt's identity", got)
	}
	if text := firstText(t, steer.Data); text != "and this" {
		t.Fatalf("steer text = %q, want the queued message's text", text)
	}
}

// sourceRPCID reads the identity a projected user message carries.
func sourceRPCID(t *testing.T, projected Event) string {
	t.Helper()
	source, ok := projected.Data["source"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no source: %v", projected.Type, projected.Data)
	}
	identity, _ := source["rpcId"].(string)
	return identity
}

// The console reads a conversation's name from the `title` projection, which it
// folds out of this event, so the durable record has to reach the wire in the
// shape the fold expects -- a tagged source object rather than the string the
// durable payload carries (ADR 0119).
func TestProjectionServesTheTitleTheConsoleFolds(t *testing.T) {
	projector := New(Identity{Turn: 1})
	renamed, ok := projector.Next(zenforge.Event{
		Seq: 1, Type: zenforge.EventSessionTitle, Timestamp: 7,
		Payload: map[string]any{"title": "My Session", "source": "user"},
	})
	if !ok {
		t.Fatal("a rename was not served")
	}
	if renamed.Type != "session/title" {
		t.Fatalf("type = %q, want session/title", renamed.Type)
	}
	if renamed.Data["title"] != "My Session" {
		t.Fatalf("title = %v, want My Session", renamed.Data["title"])
	}
	source, ok := renamed.Data["source"].(map[string]any)
	if !ok || source["kind"] != "user" {
		t.Fatalf("source = %v, want {kind:user}", renamed.Data["source"])
	}
	if seqs, ok := renamed.Data["messageSeqs"].([]any); !ok || len(seqs) != 0 {
		t.Fatalf("messageSeqs = %v, want an empty list", renamed.Data["messageSeqs"])
	}

	// A title derived from the first prompt is the fallback kind, not the operator's.
	derived, ok := projector.Next(zenforge.Event{
		Seq: 2, Type: zenforge.EventSessionTitle, Timestamp: 8,
		Payload: map[string]any{"title": "first question", "source": "fallback"},
	})
	if !ok {
		t.Fatal("a derived title was not served")
	}
	derivedSource, _ := derived.Data["source"].(map[string]any)
	if derivedSource["kind"] != "fallback" {
		t.Fatalf("source = %v, want {kind:fallback}", derived.Data["source"])
	}
}
