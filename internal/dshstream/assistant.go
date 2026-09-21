package dshstream

import (
	"fmt"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
)

// This file mints the console's dense assistant-stream frames from a run's
// durable model events.
//
// The durable log already carries the answer as it arrives: model.delta (and
// model.reasoning) hold each chunk, and the step's settlement projects to an
// assistant/message record. What the console renders as a growing message is not
// those records -- a delta's record is marked ignorable and skipped by the
// surface -- but the assistant-stream items that ride the same connection: a
// start frame opening an attempt, one chunk frame per delta, and an end frame
// releasing the durable settlement it names
// (docs/dsh-console-protocol-recon.md, "assistant-stream";
// session-controller/src/client/assistant-stream.ts).
//
// Without them an answer appears all at once when its settlement arrives, which
// is what the operator reported. Every field of every frame is derived from the
// durable events and the sequence this stream already served: the attempt id and
// step from the model events, the turn from the projection's identity, the
// chunk's text from its own delta, and the settlement's end frame from the record
// the projection produced. Nothing is invented and nothing is buffered twice.

const (
	// Assistant chunks address content blocks, not chunks: the client's reducer
	// merges deltas that share an index into one block. Text and reasoning are
	// kept apart so a renderer can show them separately, exactly as the durable
	// projection keeps them, and a delta that continues the previous block's kind
	// continues its index rather than opening another one.
	assistantTextBlock      = "text"
	assistantReasoningBlock = "reasoning"
)

// assistantTracker folds one run's durable events into assistant-stream frames.
// It lives for the whole follow stream rather than one turn: the console's cursor
// and the attempt it believes is open both survive a turn boundary, and the next
// turn's first start frame must not make the client rebaseline.
// revision is a per-generation frame counter, not a per-attempt one. The client
// refuses a generation whose next frame does not cite exactly one more than the
// last (session-controller/src/client/session-wire.ts: `expected = (assistantRevision
// ?? 0) + 1`), and a mismatch is a carrier failure: the stream is torn down and
// reopened, which is a restart loop rather than an error message. The counter
// starts where the opening snapshot's baseline left it, which is 0.
type assistantTracker struct {
	runID   string
	turn    int
	lastSeq int64
	// revision is the last frame revision this generation published.
	revision int
	// pending is an attempt that model.started opened but that has not streamed a
	// chunk yet. It is what a reasoning delta -- which carries no attemptId --
	// attaches to, and what keeps the start frame's identity the run's own.
	pending *assistantAttempt
	// active is the attempt the console has been told about and is rendering.
	active *assistantAttempt
}

// assistantAttempt is one open model attempt as the console counts it: the
// frames it has received, one-based, must stay contiguous.
type assistantAttempt struct {
	id   string
	step int
	next int
	// blocks is the attempt's content in arrival order. A chunk's index is the
	// block's position here, and the block's text is what its block-end names.
	blocks []assistantBlock
}

// assistantBlock is one content block of an open attempt.
type assistantBlock struct {
	kind string
	text string
}

// newAssistantTracker starts tracking one follow stream. cursor is the sequence
// the console holds when the stream opens, so the first attempt's start frame
// cites a sequence the client has already applied.
func newAssistantTracker(runID string, cursor int64) *assistantTracker {
	return &assistantTracker{runID: runID, lastSeq: cursor}
}

// startTurn moves the tracker onto the conversation's next turn. The sequence
// watermark and the attempt counter are deliberately kept: the console's cursor
// and revision continue across turns, and only the turn number changes.
func (t *assistantTracker) startTurn(runID string, turn int) {
	t.runID = runID
	t.turn = turn
	t.pending = nil
}

// onEvent returns the frames one durable event produces, in order. They are sent
// before the event's own console record, so a settlement record always follows
// the chunks it settles.
func (t *assistantTracker) onEvent(event zenforge.Event) []any {
	switch event.Type {
	case zenforge.EventModelStarted, zenforge.EventModelRestarted:
		// A new attempt replaces the open one. Abandoning it first is what keeps
		// the client from rebaselining when the replacement starts, and it is also
		// how a retried step's partial text leaves the screen.
		frames := t.abandon()
		t.pending = &assistantAttempt{
			id:   payloadString(event.Payload, "attemptId"),
			step: payloadIntValue(event.Payload, "step"),
		}
		if t.pending.id == "" {
			t.pending.id = fmt.Sprintf("%s/attempt/%d", t.runID, t.pending.step)
		}
		return frames
	case zenforge.EventModelDelta:
		return t.chunk(event, assistantTextBlock)
	case zenforge.EventModelReasoning:
		return t.chunk(event, assistantReasoningBlock)
	}
	return nil
}

// onRecord releases the settlement one record carries. It must be called after
// that record was sent: the client stages a settlement while the attempt it
// belongs to is open, and publishes it only when the matching end frame names its
// sequence and event type.
func (t *assistantTracker) onRecord(record dshwire.Event) []any {
	t.lastSeq = record.Seq
	if t.active == nil {
		return nil
	}
	if record.Type != "assistant/message" && record.Type != "assistant/attempt" {
		return nil
	}
	// The client binds a settlement to an attempt by turn and step, so a record
	// that belongs to another step must not close this attempt.
	if step, ok := payloadIntValueOK(record.Data, "step"); !ok || step != t.active.step {
		return nil
	}
	// The block the attempt ends inside is finalized before the attempt is
	// released, which is the order a provider streams them in: the last block-end
	// carries the block's final text.
	frames := t.blockEnd(lastBlockIndex(t.active.blocks), record.Time)
	frames = append(frames, assistantStreamValue{Type: "assistant-stream", Frame: assistantEndFrame{
		Type:      "end",
		AttemptID: t.active.id,
		Revision:  t.nextRevision(),
		Index:     t.active.next,
		Outcome: assistantOutcome{
			Kind:      "committed",
			EventType: record.Type,
			Seq:       record.Seq,
		},
	}})
	t.active = nil
	t.pending = nil
	return frames
}

// close abandons an attempt that never settled: a cancelled turn, an attempt the
// log ends in the middle of, or a turn that ends between attempts. The client
// drops the live text on an abandonment instead of waiting for a settlement that
// will not come.
func (t *assistantTracker) close() []any {
	return t.abandon()
}

// chunk folds one delta into a chunk frame, opening the attempt and then the
// block first when this is their first visible content. The frames mirror a
// provider's: a block-start opens the block, its deltas follow, and the previous
// block is closed when the kind changes.
func (t *assistantTracker) chunk(event zenforge.Event, kind string) []any {
	text := payloadString(event.Payload, "textDelta")
	if text == "" {
		return nil
	}
	step := payloadIntValue(event.Payload, "step")
	var frames []any
	if t.active == nil || t.active.step != step {
		// An attempt that changes step without a model.started of its own (a log
		// whose opening events are outside the followed window) still has to open
		// one attempt, and the previous one has to be closed first.
		frames = append(frames, t.abandon()...)
		attempt := &assistantAttempt{id: payloadString(event.Payload, "attemptId"), step: step}
		if attempt.id == "" && t.pending != nil && t.pending.step == step {
			attempt.id = t.pending.id
		}
		if attempt.id == "" {
			attempt.id = fmt.Sprintf("%s/attempt/%d", t.runID, step)
		}
		t.active = attempt
		frames = append(frames, assistantStreamValue{Type: "assistant-stream", Frame: assistantStartFrame{
			Type:            "start",
			AttemptID:       attempt.id,
			Revision:        t.nextRevision(),
			StartedAfterSeq: t.lastSeq,
			Turn:            t.turn,
			Step:            step,
		}})
	}
	// The delta lands in the block that was streaming, or opens the next one.
	block := lastBlockIndex(t.active.blocks)
	if block < 0 || t.active.blocks[block].kind != kind {
		frames = append(frames, t.blockEnd(block, event.Timestamp)...)
		block = len(t.active.blocks)
		t.active.blocks = append(t.active.blocks, assistantBlock{kind: kind})
		frames = append(frames, assistantStreamValue{Type: "assistant-stream", Frame: assistantChunkFrame{
			Type:      "chunk",
			AttemptID: t.active.id,
			Revision:  t.nextRevision(),
			Index:     t.active.next,
			Time:      event.Timestamp,
			Chunk:     assistantBlockStart{Type: "block-start", Index: block, BlockType: kind},
		}})
		t.active.next++
	}
	t.active.blocks[block].text += text
	frames = append(frames, assistantStreamValue{Type: "assistant-stream", Frame: assistantChunkFrame{
		Type:      "chunk",
		AttemptID: t.active.id,
		Revision:  t.nextRevision(),
		Index:     t.active.next,
		Time:      event.Timestamp,
		Chunk:     assistantDeltaChunk{Type: kind + "-delta", Index: block, Text: text},
	}})
	t.active.next++
	return frames
}

// blockEnd finalizes one block of the open attempt, if there is one. The block
// carries its final content as a core content block, which is what the client
// renders in place of the deltas it accumulated.
func (t *assistantTracker) blockEnd(index int, time int64) []any {
	if t.active == nil || index < 0 || index >= len(t.active.blocks) {
		return nil
	}
	block := t.active.blocks[index]
	frame := assistantStreamValue{Type: "assistant-stream", Frame: assistantChunkFrame{
		Type:      "chunk",
		AttemptID: t.active.id,
		Revision:  t.nextRevision(),
		Index:     t.active.next,
		Time:      time,
		Chunk: assistantBlockEnd{
			Type:  "block-end",
			Index: index,
			Block: map[string]any{"type": block.kind, "text": block.text},
		},
	}}
	t.active.next++
	return []any{frame}
}

// lastBlockIndex is the index of the block that was streaming, or -1 when the
// attempt has not streamed one yet.
func lastBlockIndex(blocks []assistantBlock) int {
	return len(blocks) - 1
}

// nextRevision advances the generation's frame counter.
func (t *assistantTracker) nextRevision() int {
	t.revision++
	return t.revision
}

// abandon closes the open attempt, if any, without a settlement.
func (t *assistantTracker) abandon() []any {
	if t.active == nil {
		return nil
	}
	frame := assistantStreamValue{Type: "assistant-stream", Frame: assistantEndFrame{
		Type:      "end",
		AttemptID: t.active.id,
		Revision:  t.nextRevision(),
		Index:     t.active.next,
		Outcome:   assistantOutcome{Kind: "abandoned"},
	}}
	t.active = nil
	t.pending = nil
	return []any{frame}
}

// payloadString reads one string field out of a durable event payload. A durable
// event that travelled through the log is JSON, so only string is trusted here.
func payloadString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

// payloadIntValue reads one numeric field, defaulting to zero. A payload that has
// been through the log carries a float64; one built in process carries an int.
func payloadIntValue(data map[string]any, key string) int {
	value, _ := payloadIntValueOK(data, key)
	return value
}

// payloadIntValueOK is payloadIntValue with the field's presence reported.
func payloadIntValueOK(data map[string]any, key string) (int, bool) {
	switch value := data[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	}
	return 0, false
}
