// Package dshwire projects a zenforge run log into the DSH console's session
// event vocabulary.
//
// The console's transcript is not built from event names it does not know: a
// chat row is produced by `user/message`, `assistant/message` and `tool/result`
// on the session's model-visible surface, and the tool cards pair `tool/call`
// with `tool/result`. A log served in zenforge's own vocabulary therefore loads
// (once it is legal, see below) but renders no conversation at all -- and the
// live page showed exactly that as "Failed to load history", because the
// envelope rule was broken before the vocabulary question could even be reached.
//
// Two rules make a projected record legal, both verified against the vendored
// client rather than inferred (internal/dshwire/vocabulary_test.go):
//
//   - Only the four message-producing types -- `system/message`, `user/message`,
//     `assistant/message`, `tool/result` -- may carry `surfaceOp`. The client's
//     `surfaceOpOf` throws `session event "<type>" is not surface-eligible and
//     cannot carry surfaceOp` for anything else, which is the failure the
//     operator hit. Every other projected event omits the field.
//   - A type the console does not know must carry `ignorable: true`. That marker
//     is the console's own compatibility mechanism for events written by a
//     newer or external harness: the persistence read path refuses an unmarked
//     unknown type, because silently skipping a required event would reconstruct
//     a wrong session. zenforge's names (`run.started`, `model.delta`, ...) are
//     all outside the console's vocabulary, so the pass-through arm marks them.
//
// The projection is one durable event in, one wire event out, and the wire `seq`
// is the durable `seq`. That keeps the console's cursor, its `throughSeq` paging
// and the live tail's `afterSeq` all speaking the log's own sequence, and it
// keeps sequence numbers contiguous, which the console's session format
// documents as an invariant. An event that carries no console meaning is passed
// through as an ignorable record rather than dropped, so the log stays
// losslessly readable through the wire.
package dshwire

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge"
)

// Event is the console's SessionWireEvent. Field order and omission matter: the
// client rejects unexpected fields, allows `ignorable` only as literal true, and
// allows `surfaceOp` only on the four surface-eligible types.
type Event struct {
	Type      string         `json:"type"`
	Seq       int64          `json:"seq"`
	Time      int64          `json:"time"`
	Data      map[string]any `json:"data"`
	Ignorable bool           `json:"ignorable,omitempty"`
	SurfaceOp string         `json:"surfaceOp,omitempty"`
}

// Identity is the provider and model a session's runs are served by. It is
// stamped on the projected assistant messages as their required source
// provenance, which is the label the transcript shows for "which model answered".
// An empty identity is legal on the wire (both fields are plain strings) and is
// what a host that cannot name its route must send rather than inventing one.
type Identity struct {
	Provider string
	Model    string
	// Turn is the console turn number these events belong to. A console session
	// outlives its runs: each turn is one zenforge run, and the console groups a
	// transcript by this number, so turn 1 and turn 2 must not share it. Zero
	// means the first turn.
	Turn int
	// SeqOffset moves this run's durable sequence into the session's. A session's
	// served log is the concatenation of its turns (dshwire.Session), so turn k's
	// wire sequence is its durable sequence shifted past every event of the turns
	// before it -- which is what keeps the console's cursor monotone across turns
	// instead of restarting at one.
	SeqOffset int64
}

// DefaultTurn is the turn number a projection uses when its identity names none.
const DefaultTurn = 1

// turn returns the console turn this identity projects onto.
func (i Identity) turn() int {
	if i.Turn <= 0 {
		return DefaultTurn
	}
	return i.Turn
}

// SurfaceEligibleTypes are the only event types the console allows `surfaceOp`
// on. They are the message-producing types of the model-visible surface
// (packages/core/session/src/surface.ts SURFACE_EVENT_TYPES).
var SurfaceEligibleTypes = map[string]bool{
	"system/message":    true,
	"user/message":      true,
	"assistant/message": true,
	"tool/result":       true,
}

// Projector turns durable events into wire events, in order. It is stateful
// because a console transcript is not a one-event-at-a-time view of the log: the
// assistant's message is the settlement of the deltas that preceded it, so the
// projector accumulates the step's streamed blocks and emits them at
// `model.done`. Project one run's events through one Projector, from the log's
// beginning, so the state matches the window being served.
type Projector struct {
	identity Identity

	// blocks is the ordered content of the step currently streaming. Deltas
	// arrive interleaved (reasoning, then text, then more reasoning), so the
	// blocks are appended in arrival order and merged into the previous block
	// only while it is the same kind.
	blocks []map[string]any
	// usage is the step's token accounting, reported by a `model.usage` event
	// and attached to the step's assistant message. Absent when the adapter
	// reported none, which is how the console distinguishes "no accounting".
	usage map[string]any
	// steps remembers which step each tool call belongs to, so a tool result
	// carries the step its call opened even when the call sits in a window the
	// page did not request.
	steps map[string]int
	// current is the step the log is inside. The host reports a step on the step
	// and model events but not on tool calls or results, so those inherit the
	// enclosing step rather than claiming step zero.
	current int
}

// New returns a projector that stamps assistant messages with the given identity.
func New(identity Identity) *Projector {
	return &Projector{identity: identity, steps: map[string]int{}}
}

// Projection is a run's whole log projected by one projector. It is the shape
// both read paths need: session/follow snapshots it and tails it live, and
// session/page windows it.
type Projection struct {
	projector *Projector

	// Events holds one wire event per durable event, in log order.
	Events []Event
	// Source holds the durable events the projection was built from, aligned with
	// Events by index.
	Source []zenforge.Event
}

// Project projects a run's durable log from its beginning. The returned
// projection keeps the projector so a live tail can continue it: the follower
// appends later durable events with Append rather than re-projecting, which keeps
// a step's streamed blocks accumulated exactly once.
func Project(events []zenforge.Event, identity Identity) *Projection {
	projection := &Projection{projector: New(identity)}
	projection.Append(events...)
	return projection
}

// Append projects more durable events onto the projection, in order.
func (p *Projection) Append(events ...zenforge.Event) {
	for _, event := range events {
		p.Events = append(p.Events, p.projector.Next(event))
		p.Source = append(p.Source, event)
	}
}

// Window returns the newest maxMessages wire events, or all of them when
// maxMessages is not positive, plus whether older events were left out.
func (p *Projection) Window(maxMessages int) ([]Event, bool) {
	if maxMessages <= 0 || len(p.Events) <= maxMessages {
		return p.Events, false
	}
	return p.Events[len(p.Events)-maxMessages:], true
}

// Next projects one durable event.
func (p *Projector) Next(event zenforge.Event) Event {
	data := payload(event)
	base := Event{Seq: p.identity.SeqOffset + event.Seq, Time: event.Timestamp, Data: data}

	switch event.Type {
	case zenforge.EventRunStarted:
		if text, ok := stringField(data, "input"); ok && text != "" {
			return p.surface(base, "user/message", userMessage(base.Seq, text))
		}
	case zenforge.EventRequestSteer:
		if text, ok := firstStringField(data, "input", "text", "content"); ok {
			return p.surface(base, "user/message", userMessage(base.Seq, text))
		}
	case zenforge.EventStepStarted:
		if step, ok := intField(data, "step"); ok {
			p.current = step
			return base.known("step/start", p.turnStep(step))
		}
	case zenforge.EventStepDone:
		if step, ok := intField(data, "step"); ok {
			return base.known("step/end", p.turnStep(step))
		}
	case zenforge.EventModelStarted:
		// A new attempt starts a fresh stream; a retried step must not carry the
		// failed attempt's text into its settlement.
		if step, ok := intField(data, "step"); ok {
			p.current = step
		}
		p.resetStep()
	case zenforge.EventModelDelta:
		if delta, ok := stringField(data, "textDelta"); ok {
			p.appendBlock("text", delta)
		}
	case zenforge.EventModelReasoning:
		if delta, ok := stringField(data, "textDelta"); ok {
			p.appendBlock("reasoning", delta)
		}
	case zenforge.EventModelUsage:
		if usage, ok := mapField(data, "usage"); ok {
			p.usage = tokenUsage(usage)
		}
	case zenforge.EventModelDone:
		step, ok := intField(data, "step")
		if !ok {
			step = p.current
		}
		p.current = step
		content, usage := p.takeStep()
		if len(content) == 0 {
			// A step that settled without model-visible content is an attempt,
			// not a message: the console records it as log-only history.
			return base.known("assistant/attempt", map[string]any{
				"turn": p.identity.turn(), "step": step, "stream": []any{},
			})
		}
		message := map[string]any{
			"turn": p.identity.turn(),
			"step": step,
			"message": map[string]any{
				"id":      messageID(base.Seq),
				"role":    "assistant",
				"content": wireBlocks(content),
				"source": map[string]any{
					"kind":     "model",
					"provider": p.identity.Provider,
					"model":    p.identity.Model,
				},
			},
			"stream": []any{},
		}
		if usage != nil {
			message["usage"] = usage
		}
		return p.surface(base, "assistant/message", message)
	case zenforge.EventToolCall:
		callID, _ := stringField(data, "toolCallId")
		name, _ := stringField(data, "toolName")
		if callID == "" || name == "" {
			break
		}
		step, ok := intField(data, "step")
		if !ok || step == 0 {
			step = p.current
		}
		p.steps[callID] = step
		return base.known("tool/call", map[string]any{
			"turn":      p.identity.turn(),
			"step":      step,
			"callId":    callID,
			"name":      name,
			"arguments": rawArguments(data["arguments"]),
		})
	case zenforge.EventToolResult, zenforge.EventToolError:
		callID, _ := stringField(data, "toolCallId")
		if callID == "" {
			break
		}
		output, _ := stringField(data, "output")
		if message, ok := firstStringField(data, "error"); ok && message != "" {
			output = message
		}
		step := p.steps[callID]
		if step == 0 {
			step = p.current
		}
		return p.surface(base, "tool/result", p.toolResult(base.Seq, step, callID, output, event.Type == zenforge.EventToolError))
	case zenforge.EventRunDone:
		return base.known("turn/end", map[string]any{
			"turn": p.identity.turn(), "reason": map[string]any{"kind": "completed"},
		})
	case zenforge.EventRunError:
		message, _ := firstStringField(data, "error", "message")
		if message == "" {
			message = "the run failed"
		}
		return base.known("turn/end", map[string]any{
			"turn": p.identity.turn(),
			"reason": map[string]any{
				"kind":  "error",
				"error": map[string]any{"message": message, "code": "UNKNOWN"},
			},
		})
	case zenforge.EventRunCancelled:
		return base.known("turn/end", map[string]any{
			"turn":   p.identity.turn(),
			"reason": map[string]any{"kind": "aborted", "reason": map[string]any{"kind": "user"}},
		})
	}
	// Everything else keeps its own name and payload as an ignorable record: the
	// console cannot interpret it, and the marker is what tells the client that
	// omitting it from the surface is deliberate. A type the console does know is
	// never marked ignorable -- it is passed through for the console to read.
	return Event{
		Type:      string(event.Type),
		Seq:       base.Seq,
		Time:      base.Time,
		Data:      data,
		Ignorable: !KnownEventTypes[string(event.Type)],
	}
}

// surface marks an event as a surface append. Only the eligible types reach it.
func (p *Projector) surface(base Event, eventType string, data map[string]any) Event {
	if !SurfaceEligibleTypes[eventType] {
		// Unreachable: the mapping only calls this with the four eligible types,
		// and a mistake here is exactly the wire bug the client refuses.
		panic("dshwire: " + eventType + " is not surface-eligible")
	}
	return Event{Type: eventType, Seq: base.Seq, Time: base.Time, Data: data, SurfaceOp: "append"}
}

// known writes a console event that is not on the model-visible surface. It
// carries no `surfaceOp`, and no `ignorable` marker because the console knows
// the type and must read it.
func (base Event) known(eventType string, data map[string]any) Event {
	return Event{Type: eventType, Seq: base.Seq, Time: base.Time, Data: data}
}

func (p *Projector) resetStep() {
	p.blocks = nil
	p.usage = nil
}

func (p *Projector) takeStep() ([]map[string]any, map[string]any) {
	content, usage := p.blocks, p.usage
	p.resetStep()
	if content == nil {
		content = []map[string]any{}
	}
	return content, usage
}

// appendBlock adds a delta to the step's content, merging into the previous
// block while it is the same kind so a streamed answer is one block.
func (p *Projector) appendBlock(kind, text string) {
	if text == "" {
		return
	}
	if count := len(p.blocks); count > 0 && p.blocks[count-1]["type"] == kind {
		previous, _ := p.blocks[count-1]["text"].(string)
		p.blocks[count-1]["text"] = previous + text
		return
	}
	p.blocks = append(p.blocks, map[string]any{"type": kind, "text": text})
}

// wireBlocks renders accumulated content blocks as the JSON array the wire
// carries. The projector's own buffer is typed, so without this an in-memory
// projection would hand a `[]map[string]any` to code expecting the decoded shape
// (`[]any`) -- the one place the projected value must match its marshaled form.
func wireBlocks(blocks []map[string]any) []any {
	encoded := make([]any, 0, len(blocks))
	for _, block := range blocks {
		encoded = append(encoded, block)
	}
	return encoded
}

func (p *Projector) turnStep(step int) map[string]any {
	return map[string]any{"turn": p.identity.turn(), "step": step}
}

// messageID names one projected message. The console carries a message identity
// it only has to keep stable within the session, and the session's own sequence
// is exactly that: unique, stable, and reproduced by any window over the log.
// It is the *session* sequence, not the run's: each turn numbers its durability
// from one, so a run-local number would name turn one's message and turn two's
// message identically (ADR 0110).
func messageID(seq int64) string {
	return fmt.Sprintf("msg-%d", seq)
}

func userMessage(seq int64, text string) map[string]any {
	return map[string]any{
		"id":      messageID(seq),
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": text}},
		"source":  map[string]any{"kind": "user"},
	}
}

// toolResult carries the tool's model-facing result, correlated with its call.
// The console validates a `tool/result` payload: when it has no `error` field
// the block is the only place failure is stated, which is the shape this host
// can honestly produce -- it has the tool's exit state, not the console's
// structured failure identity.
func (p *Projector) toolResult(seq int64, step int, callID, output string, isError bool) map[string]any {
	block := map[string]any{
		"type":       "tool-result",
		"toolCallId": callID,
		"content":    []any{map[string]any{"type": "text", "text": output}},
	}
	if isError {
		block["isError"] = true
	}
	return map[string]any{
		"turn": p.identity.turn(),
		"step": step,
		"message": map[string]any{
			"id":      messageID(seq),
			"role":    "user",
			"content": []any{block},
			"source":  map[string]any{"kind": "tool", "callId": callID},
		},
	}
}

// tokenUsage maps the host's accounting onto the console's TokenUsage, whose
// names are the model-facing ones. The total is only written when the adapter
// reported it: the console treats an absent total as "unavailable".
func tokenUsage(usage map[string]any) map[string]any {
	mapped := map[string]any{}
	if value, ok := intField(usage, "promptTokens"); ok {
		mapped["inputTokens"] = value
	}
	if value, ok := intField(usage, "completionTokens"); ok {
		mapped["outputTokens"] = value
	}
	if value, ok := intField(usage, "totalTokens"); ok {
		mapped["totalTokens"] = value
	}
	if _, ok := mapped["inputTokens"]; !ok {
		if _, ok := mapped["outputTokens"]; !ok {
			return nil
		}
	}
	return mapped
}

// payload clones the event's data so a projected record can never alias the
// durable event the log handed us.
func payload(event zenforge.Event) map[string]any {
	data := map[string]any{}
	for key, value := range event.Payload {
		data[key] = value
	}
	return data
}

// rawArguments renders a tool call's arguments as the raw JSON string the model
// produced, which is what the console stores. A non-string value is encoded
// rather than dropped: the tool card shows the exact call either way.
func rawArguments(value any) string {
	switch typed := value.(type) {
	case nil:
		return "{}"
	case string:
		if strings.TrimSpace(typed) == "" {
			return "{}"
		}
		return typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "{}"
		}
		return string(encoded)
	}
}

func stringField(data map[string]any, key string) (string, bool) {
	value, ok := data[key].(string)
	if !ok {
		return "", false
	}
	return value, true
}

// firstStringField returns the first present, non-empty string among the keys,
// for payloads whose text key the host has used more than one name for.
func firstStringField(data map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := stringField(data, key); ok && value != "" {
			return value, true
		}
	}
	return "", false
}

func mapField(data map[string]any, key string) (map[string]any, bool) {
	value, ok := data[key].(map[string]any)
	return value, ok
}

// intField accepts every numeric shape a JSON round trip can produce: the log's
// in-process events carry ints, and a value that has been through JSON is a
// float64.
func intField(data map[string]any, key string) (int, bool) {
	switch value := data[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}
