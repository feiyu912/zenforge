package dshstream

import (
	"reflect"
	"testing"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
)

// durableEvent is one durable event as the harness writes it.
func durableEvent(seq int64, eventType zenforge.EventType, data map[string]any) zenforge.Event {
	return zenforge.Event{Seq: seq, Type: eventType, Timestamp: 1000 + seq, Payload: data}
}

// deltaEvent is one durable model.delta as the harness writes it.
func deltaEvent(seq int64, text string, attemptID string, step int) zenforge.Event {
	return zenforge.Event{
		Seq:       seq,
		Type:      zenforge.EventModelDelta,
		Timestamp: 1000 + seq,
		Payload:   map[string]any{"textDelta": text, "attemptId": attemptID, "step": step},
	}
}

// reasoningEvent is one durable model.reasoning, which carries no attempt id.
func reasoningEvent(seq int64, text string, step int) zenforge.Event {
	return zenforge.Event{
		Seq:       seq,
		Type:      zenforge.EventModelReasoning,
		Timestamp: 1000 + seq,
		Payload:   map[string]any{"textDelta": text, "step": step},
	}
}

// blockStartsOf returns the block-start chunks in a run of frames.
func blockStartsOf(t *testing.T, frames []assistantStreamValue) []assistantBlockStart {
	t.Helper()
	starts := []assistantBlockStart{}
	for _, frame := range frames {
		chunk, ok := frame.Frame.(assistantChunkFrame)
		if !ok {
			continue
		}
		if start, ok := chunk.Chunk.(assistantBlockStart); ok {
			starts = append(starts, start)
		}
	}
	return starts
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
	if len(start) != 3 {
		t.Fatalf("frames = %d, want a start, a block start and a chunk", len(start))
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
	// The block opens before its first delta, exactly as a provider streams it.
	opened, ok := start[1].Frame.(assistantChunkFrame)
	if !ok {
		t.Fatalf("second frame %T is not a chunk", start[1].Frame)
	}
	if block, ok := opened.Chunk.(assistantBlockStart); !ok || block.Type != "block-start" || block.BlockType != assistantTextBlock || block.Index != 0 {
		t.Fatalf("block start = %+v, want the first text block opened", opened.Chunk)
	}
	chunk, ok := start[2].Frame.(assistantChunkFrame)
	if !ok {
		t.Fatalf("third frame %T is not a chunk", start[2].Frame)
	}
	if chunk.Index != 1 || chunk.AttemptID != "attempt-1" {
		t.Fatalf("chunk = %+v, want frame index 1 of attempt-1", chunk)
	}
	delta, ok := chunk.Chunk.(assistantDeltaChunk)
	if !ok || delta.Type != "text-delta" || delta.Text != "hello" || delta.Index != 0 {
		t.Fatalf("chunk payload = %+v, want a text delta of hello in block 0", chunk.Chunk)
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
	if second.Index != 2 {
		t.Fatalf("chunk index = %d, want 2", second.Index)
	}
	if delta, ok := second.Chunk.(assistantDeltaChunk); !ok || delta.Index != 0 {
		t.Fatalf("chunk payload = %+v, want the same text block", second.Chunk)
	}

	// A settlement for another step must not close this attempt: the client binds
	// them by turn and step, and so does the tracker.
	if frames := tracker.onRecord(dshwire.Event{Type: "assistant/message", Seq: 40, Data: map[string]any{"step": 9}}); len(frames) != 0 {
		t.Fatalf("a settlement for another step closed the attempt: %+v", frames)
	}

	// The step's settlement is released with the sequence it has: the block closes
	// with its final text, and then the attempt is closed exactly once.
	end := framesOf(t, tracker.onRecord(dshwire.Event{Type: "assistant/message", Seq: 41, Time: 1050, Data: map[string]any{"step": 3}}))
	if len(end) != 2 {
		t.Fatalf("frames = %d, want a block end and an end", len(end))
	}
	blockEnd, ok := end[0].Frame.(assistantChunkFrame)
	if !ok {
		t.Fatalf("first frame %T is not a chunk", end[0].Frame)
	}
	if closed, ok := blockEnd.Chunk.(assistantBlockEnd); !ok || closed.Index != 0 {
		t.Fatalf("block end = %+v, want the text block finalized", blockEnd.Chunk)
	} else if closed.Block["type"] != "text" || closed.Block["text"] != "hello there" {
		t.Fatalf("block end block = %v, want the accumulated text", closed.Block)
	}
	closing, ok := end[1].Frame.(assistantEndFrame)
	if !ok {
		t.Fatalf("frame %T is not an end", end[0].Frame)
	}
	if closing.Outcome.Kind != "committed" || closing.Outcome.EventType != "assistant/message" || closing.Outcome.Seq != 41 {
		t.Fatalf("outcome = %+v, want a committed settlement of 41", closing.Outcome)
	}
	// Four frames were sent for the two deltas: the block start, both chunks, and
	// the block end.
	if closing.Index != 4 {
		t.Fatalf("end index = %d, want the four frames it settles", closing.Index)
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
	// Two frames were sent for the delta: the block start and the chunk itself.
	if closing.Index != 2 {
		t.Fatalf("end index = %d, want the two frames it abandons", closing.Index)
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
	if opening.Revision <= closing.Revision {
		t.Fatalf("revision = %d, want one past the abandonment's %d", opening.Revision, closing.Revision)
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
	if len(reasoning) != 3 {
		t.Fatalf("frames = %d, want a start, a block start and a chunk", len(reasoning))
	}
	opening, _ := reasoning[0].Frame.(assistantStartFrame)
	if opening.AttemptID != "attempt-2" {
		t.Fatalf("start = %+v, want the restarted attempt-2", opening)
	}
	block, _ := reasoning[1].Frame.(assistantChunkFrame).Chunk.(assistantBlockStart)
	if block.BlockType != assistantReasoningBlock || block.Index != 0 {
		t.Fatalf("block start = %+v, want the reasoning block opened", block)
	}
	chunk, _ := reasoning[2].Frame.(assistantChunkFrame)
	delta, _ := chunk.Chunk.(assistantDeltaChunk)
	if delta.Type != "reasoning-delta" || delta.Index != 0 {
		t.Fatalf("chunk = %+v, want a reasoning delta in its own block", delta)
	}
}

// A block is a run of deltas of one kind. Reasoning then text is two blocks, and
// the first is finalized before the second opens -- which is what the client's
// reducer needs to render them apart. A later run of the same kind is a third
// block rather than a continuation of the first.
func TestAssistantTrackerOpensAndClosesBlocksAsKindsChange(t *testing.T) {
	tracker := newAssistantTracker("run-1", 0)
	tracker.startTurn("run-1", 1)

	first := framesOf(t, tracker.onEvent(reasoningEvent(1, "why", 1)))
	starts := blockStartsOf(t, first)
	if len(starts) != 1 || starts[0].BlockType != assistantReasoningBlock || starts[0].Index != 0 {
		t.Fatalf("reasoning blocks = %+v, want block 0 opened as reasoning", starts)
	}

	// The kind changes: the reasoning block closes, then the text block opens.
	second := framesOf(t, tracker.onEvent(deltaEvent(2, "answer", "attempt-1", 1)))
	if len(second) != 3 {
		t.Fatalf("frames = %d, want a block end, a block start and a chunk", len(second))
	}
	closing, ok := second[0].Frame.(assistantChunkFrame)
	if !ok {
		t.Fatalf("first frame %T is not a chunk", second[0].Frame)
	}
	if closed, ok := closing.Chunk.(assistantBlockEnd); !ok || closed.Index != 0 || closed.Block["type"] != "reasoning" {
		t.Fatalf("block end = %+v, want reasoning block 0 finalized", closing.Chunk)
	}
	starts = blockStartsOf(t, second)
	if len(starts) != 1 || starts[0].BlockType != assistantTextBlock || starts[0].Index != 1 {
		t.Fatalf("text blocks = %+v, want block 1 opened as text", starts)
	}
	if delta, _ := second[2].Frame.(assistantChunkFrame).Chunk.(assistantDeltaChunk); delta.Index != 1 {
		t.Fatalf("delta block = %d, want the text block 1", delta.Index)
	}

	// Reasoning again is its own block: the reducer merges by index, so reusing
	// block 0 would append it to the answer instead of keeping it apart.
	third := framesOf(t, tracker.onEvent(reasoningEvent(3, "again", 1)))
	starts = blockStartsOf(t, third)
	if len(starts) != 1 || starts[0].BlockType != assistantReasoningBlock || starts[0].Index != 2 {
		t.Fatalf("reasoning blocks = %+v, want block 2 opened as reasoning", starts)
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

	// Two deltas contribute a start, a block start, two chunks and a block end,
	// and the settlement closes the attempt.
	want := []int{1, 2, 3, 4, 5, 6}
	if !reflect.DeepEqual(revisions, want) {
		t.Fatalf("frame revisions = %v, want %v", revisions, want)
	}
}

// A console that reconnects in the middle of an answer has to be handed the live
// attempt, or it repaints only the settlement and the operator watches the answer
// vanish and reappear (ADR 0118). The baseline is rebuilt by replaying the turn's
// durable log, and its compact stream must expand to exactly the frames nextIndex
// counts -- the client stops reading the baseline at that count.
func TestAssistantTrackerHandsOverAnOpenAttemptAsABaseline(t *testing.T) {
	events := []zenforge.Event{
		durableEvent(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
		durableEvent(2, zenforge.EventStepStarted, map[string]any{"step": 1}),
		durableEvent(3, zenforge.EventModelStarted, map[string]any{"step": 1, "attemptId": "attempt-1"}),
		reasoningEvent(4, "why", 1),
		deltaEvent(5, "the ", "attempt-1", 1),
		deltaEvent(6, "answer", "attempt-1", 1),
	}
	tracker := replayAssistant("run-1", 2, 8, dshwire.Identity{Turn: 2}, events)
	opening := tracker.baselineOf()
	if opening == nil {
		t.Fatal("a turn that is still streaming handed over no attempt")
	}
	if opening.AttemptID != "attempt-1" || opening.Step != 1 || opening.Turn != 2 {
		t.Fatalf("baseline = %+v, want attempt-1 step 1 of turn 2", opening)
	}
	// The replay stamped the records itself, so startedAfterSeq is the sequence of
	// the record the console already holds, not the cursor the stream opened with.
	if opening.StartedAfterSeq != 2 {
		t.Fatalf("startedAfterSeq = %d, want the last record before the attempt", opening.StartedAfterSeq)
	}
	// Two blocks, six chunk frames: the reasoning block's start and its delta, the
	// close of that block, the text block's start and its delta, then the second
	// text delta.
	if opening.NextIndex != 6 {
		t.Fatalf("nextIndex = %d, want the six frames the attempt has", opening.NextIndex)
	}
	if expanded := countExpanded(t, opening.Stream); expanded != opening.NextIndex {
		t.Fatalf("the baseline expands to %d frames but nextIndex says %d", expanded, opening.NextIndex)
	}
	// The stream is the console's own compaction: a verbatim chunk record for each
	// block boundary, and one run per block of deltas.
	kinds := []string{}
	for _, record := range opening.Stream {
		object := record.(map[string]any)
		kinds = append(kinds, object["type"].(string))
	}
	want := []string{"chunk", "reasoning-chunks", "chunk", "chunk", "text-chunks"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("baseline stream = %v, want %v", kinds, want)
	}

	// A turn whose answer settled has no attempt to hand over.
	settled := replayAssistant("run-1", 1, 0, dshwire.Identity{}, []zenforge.Event{
		durableEvent(1, zenforge.EventRunStarted, map[string]any{"input": "hello"}),
		durableEvent(2, zenforge.EventStepStarted, map[string]any{"step": 1}),
		deltaEvent(3, "done", "attempt-1", 1),
		durableEvent(4, zenforge.EventModelDone, map[string]any{"step": 1}),
	})
	if settling := settled.baselineOf(); settling != nil {
		t.Fatalf("a settled turn handed over %+v", settling)
	}
}

// countExpanded counts the frames the client's expander would produce from a
// compact stream: one per delta, and one per verbatim chunk record.
func countExpanded(t *testing.T, stream []any) int {
	t.Helper()
	count := 0
	for _, record := range stream {
		object, ok := record.(map[string]any)
		if !ok {
			t.Fatalf("stream record %T is not an object", record)
		}
		switch object["type"] {
		case "chunk":
			count++
		case "text-chunks", "reasoning-chunks":
			texts, ok := object["texts"].([]any)
			if !ok || len(texts) == 0 {
				t.Fatalf("timeline %v has no texts", object)
			}
			count += len(texts)
		default:
			t.Fatalf("unexpected stream record type %v", object["type"])
		}
	}
	return count
}
