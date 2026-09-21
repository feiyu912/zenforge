package dshstream

import (
	"reflect"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
)

// deltaEvent is one durable model.delta as the harness writes it.
func deltaEvent(seq int64, text string, attemptID string, step int) zenforge.Event {
	return zenforge.Event{
		Seq:       seq,
		Type:      zenforge.EventModelDelta,
		Timestamp: 1000 + seq,
		Payload:   map[string]any{"textDelta": text, "attemptId": attemptID, "step": step},
	}
}

// framesOf casts one tracker step's frames.
func framesOf(t *testing.T, frames []any) []assistantStreamValue {
	t.Helper()
	values := make([]assistantStreamValue, 0, len(frames))
	for _, frame := range frames {
		value, ok := frame.(assistantStreamValue)
		if !ok {
			t.Fatalf("frame %T is not an assistant-stream item", frame)
		}
		if value.Type != "assistant-stream" {
			t.Fatalf("item type = %q, want assistant-stream", value.Type)
		}
		values = append(values, value)
	}
	return values
}

func TestAssistantTrackerStreamsDeltasAndSettlesThem(t *testing.T) {
	tracker := newAssistantTracker("run-1", 7)
	tracker.startTurn("run-1", 2)

	start := framesOf(t, tracker.onEvent(deltaEvent(1, "hello", "attempt-1", 3)))
	if len(start) != 2 {
		t.Fatalf("frames = %d, want a start and a chunk", len(start))
	}
	opening, ok := start[0].Frame.(assistantStartFrame)
	if !ok {
		t.Fatalf("first frame %T is not a start", start[0].Frame)
	}
	if opening.AttemptID != "attempt-1" || opening.Step != 3 || opening.Turn != 2 {
		t.Fatalf("start = %+v, want attempt-1 step 3 of turn 2", opening)
	}
	// The attempt cites the sequence the console already holds, so its settlement
	// is never mistaken for one it has already applied.
	if opening.StartedAfterSeq != 7 {
		t.Fatalf("startedAfterSeq = %d, want the stream's cursor 7", opening.StartedAfterSeq)
	}
	chunk, ok := start[1].Frame.(assistantChunkFrame)
	if !ok {
		t.Fatalf("second frame %T is not a chunk", start[1].Frame)
	}
	if chunk.Index != 0 || chunk.AttemptID != "attempt-1" {
		t.Fatalf("chunk = %+v, want index 0 of attempt-1", chunk)
	}
	delta, ok := chunk.Chunk.(assistantDeltaChunk)
	if !ok || delta.Type != "text-delta" || delta.Text != "hello" {
		t.Fatalf("chunk payload = %+v, want a text delta of hello", chunk.Chunk)
	}
	// The frame's clock is the durable event's own timestamp: a rendered delta is
	// as old as its log entry, not as old as this read.
	if chunk.Time != 1001 {
		t.Fatalf("chunk time = %d, want the event's 1001", chunk.Time)
	}

	// A second delta continues the same attempt: no new start, next index.
	more := framesOf(t, tracker.onEvent(deltaEvent(2, " there", "attempt-1", 3)))
	if len(more) != 1 {
		t.Fatalf("frames = %d, want one chunk", len(more))
	}
	second, _ := more[0].Frame.(assistantChunkFrame)
	if second.Index != 1 {
		t.Fatalf("chunk index = %d, want 1", second.Index)
	}

	// A settlement for another step must not close this attempt: the client binds
	// them by turn and step, and so does the tracker.
	if frames := tracker.onRecord(dshwire.Event{Type: "assistant/message", Seq: 40, Data: map[string]any{"step": 9}}); len(frames) != 0 {
		t.Fatalf("a settlement for another step closed the attempt: %+v", frames)
	}

	// The step's settlement is released with the sequence it has, and the attempt
	// is closed exactly once.
	end := framesOf(t, tracker.onRecord(dshwire.Event{Type: "assistant/message", Seq: 41, Data: map[string]any{"step": 3}}))
	if len(end) != 1 {
		t.Fatalf("frames = %d, want one end", len(end))
	}
	closing, ok := end[0].Frame.(assistantEndFrame)
	if !ok {
		t.Fatalf("frame %T is not an end", end[0].Frame)
	}
	if closing.Outcome.Kind != "committed" || closing.Outcome.EventType != "assistant/message" || closing.Outcome.Seq != 41 {
		t.Fatalf("outcome = %+v, want a committed settlement of 41", closing.Outcome)
	}
	if closing.Index != 2 {
		t.Fatalf("end index = %d, want the two chunks it settles", closing.Index)
	}
	if frames := tracker.onRecord(dshwire.Event{Type: "assistant/message", Seq: 42, Data: map[string]any{"step": 3}}); len(frames) != 0 {
		t.Fatalf("the closed attempt settled twice: %+v", frames)
	}
}

// A turn that ends with an attempt open -- cancelled mid-answer -- must close it,
// or the console renders text that will never settle and the next turn's start
// frame makes the client rebaseline its whole window.
func TestAssistantTrackerAbandonsAnAttemptTheTurnEndsOn(t *testing.T) {
	tracker := newAssistantTracker("run-1", 0)
	tracker.startTurn("run-1", 1)
	framesOf(t, tracker.onEvent(deltaEvent(1, "half an ans", "attempt-1", 1)))

	frames := framesOf(t, tracker.close())
	if len(frames) != 1 {
		t.Fatalf("frames = %d, want one end", len(frames))
	}
	closing, _ := frames[0].Frame.(assistantEndFrame)
	if closing.Outcome.Kind != "abandoned" {
		t.Fatalf("outcome = %+v, want an abandonment", closing.Outcome)
	}
	if closing.Outcome.EventType != "" || closing.Outcome.Seq != 0 {
		t.Fatalf("abandoned outcome carries a settlement: %+v", closing.Outcome)
	}
	if closing.Index != 1 {
		t.Fatalf("end index = %d, want the one chunk it abandons", closing.Index)
	}
	if frames := framesOf(t, tracker.close()); len(frames) != 0 {
		t.Fatalf("closing twice sent %d frames", len(frames))
	}

	// The next turn opens a fresh attempt, and its start frame is one the client
	// accepts: nothing is open, and the revision moved on.
	tracker.startTurn("run-2", 2)
	next := framesOf(t, tracker.onEvent(deltaEvent(1, "again", "attempt-2", 1)))
	opening, _ := next[0].Frame.(assistantStartFrame)
	if opening.Turn != 2 || opening.AttemptID != "attempt-2" {
		t.Fatalf("next start = %+v, want attempt-2 of turn 2", opening)
	}
	if opening.Revision != closing.Revision+1 {
		t.Fatalf("revision = %d, want %d", opening.Revision, closing.Revision+1)
	}
}

// A new attempt replaces the open one: the harness retries a step under a new
// attempt id, and the console must drop the failed attempt's text rather than
// render it into the replacement's answer.
func TestAssistantTrackerReplacesAnAttemptOnRestart(t *testing.T) {
	tracker := newAssistantTracker("run-1", 0)
	tracker.startTurn("run-1", 1)
	framesOf(t, tracker.onEvent(deltaEvent(1, "doomed", "attempt-1", 1)))

	restarted := framesOf(t, tracker.onEvent(zenforge.Event{
		Seq: 2, Type: zenforge.EventModelRestarted, Timestamp: 1002,
		Payload: map[string]any{"attemptId": "attempt-2", "step": 1},
	}))
	if len(restarted) != 1 {
		t.Fatalf("frames = %d, want the abandonment", len(restarted))
	}
	closing, _ := restarted[0].Frame.(assistantEndFrame)
	if closing.Outcome.Kind != "abandoned" || closing.AttemptID != "attempt-1" {
		t.Fatalf("end = %+v, want attempt-1 abandoned", closing)
	}

	// A reasoning delta carries no attempt id of its own: it belongs to the
	// attempt the restarted event opened.
	reasoning := framesOf(t, tracker.onEvent(zenforge.Event{
		Seq: 3, Type: zenforge.EventModelReasoning, Timestamp: 1003,
		Payload: map[string]any{"textDelta": "thinking", "step": 1},
	}))
	if len(reasoning) != 2 {
		t.Fatalf("frames = %d, want a start and a chunk", len(reasoning))
	}
	opening, _ := reasoning[0].Frame.(assistantStartFrame)
	if opening.AttemptID != "attempt-2" {
		t.Fatalf("start = %+v, want the restarted attempt-2", opening)
	}
	chunk, _ := reasoning[1].Frame.(assistantChunkFrame)
	delta, _ := chunk.Chunk.(assistantDeltaChunk)
	if delta.Type != "reasoning-delta" || delta.Index != assistantReasoningBlock {
		t.Fatalf("chunk = %+v, want a reasoning delta in its own block", delta)
	}
}
// Every frame of a generation must cite exactly one more revision than the last:
// the client's session wire throws a carrier failure otherwise, tears the stream
// down and reopens it, so a wrong counter is a restart loop rather than a
// visible error. The operator's browser reproduced exactly that.
func TestAssistantTrackerNumbersEveryFrameConsecutively(t *testing.T) {
	tracker := newAssistantTracker("run-1", 0)
	tracker.startTurn("run-1", 1)

	revisions := []int{}
	visit := func(frames []any) {
		for _, frame := range framesOf(t, frames) {
			switch value := frame.Frame.(type) {
			case assistantStartFrame:
				revisions = append(revisions, value.Revision)
			case assistantChunkFrame:
				revisions = append(revisions, value.Revision)
			case assistantEndFrame:
				revisions = append(revisions, value.Revision)
			}
		}
	}
	for index, text := range []string{"al", "pha"} {
		visit(tracker.onEvent(deltaEvent(int64(index+1), text, "attempt-1", 1)))
	}
	visit(tracker.onRecord(dshwire.Event{Type: "assistant/message", Seq: 9, Data: map[string]any{"step": 1}}))

	want := []int{1, 2, 3, 4}
	if !reflect.DeepEqual(revisions, want) {
		t.Fatalf("frame revisions = %v, want %v", revisions, want)
	}
}
