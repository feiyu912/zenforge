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
// documents as an invariant. A durable event that carries no console meaning --
// this host's own bookkeeping, and the model deltas that feed the dense
// assistant-stream instead -- produces no record at all, because upstream's
// session log contains only the console's own vocabulary and a record the
// console can only skip still costs a window slot and a sequence number
// (ADR 0117).
package dshwire

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge"
)

// Event is the console's SessionWireEvent. Field order and omission matter: the
// client rejects unexpected fields and allows `surfaceOp` only on the four
// surface-eligible types. This host never sends `ignorable`: it serves only the
// console's vocabulary, so every record is one the console must read (ADR 0117).
type Event struct {
	Type      string         `json:"type"`
	Seq       int64          `json:"seq"`
	Time      int64          `json:"time"`
	Data      map[string]any `json:"data"`
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
	// SeqOffset moves this turn's records into the session's sequence. A session's
	// served log is the concatenation of its turns (dshwire.Session), so turn k
	// numbers its records past the records of every turn before it -- which is
	// what keeps the console's cursor monotone across turns instead of restarting
	// at one. It counts records, not durable events: the served sequence is the
	// console's own (ADR 0117).
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
	// calls is the tool calls of this turn that have not been answered yet, in the
	// order they were made. A run that ends before they do leaves the console with
	// cards stuck in "running" unless they are resolved (see
	// interruptedToolResults), and the console's own vocabulary for that is
	// `{name: "Interrupted", code: "interrupted"}`, which it renders as "stopped"
	// (ui-chat conversation-nodes/tool.ts).
	calls []string
	// steps remembers which step each tool call belongs to, so a tool result
	// carries the step its call opened even when the call sits in a window the
	// page did not request.
	steps map[string]int
	// current is the step the log is inside. The host reports a step on the step
	// and model events but not on tool calls or results, so those inherit the
	// enclosing step rather than claiming step zero.
	current int
	// records counts the console records this turn has produced, which is what
	// numbers them: an event with no console meaning consumes no sequence number.
	records int
	// chunks is the step's streamed timeline in the console's compact form, one
	// entry per content block, and `blocks`' counterpart: the settled message
	// carries it as `stream`, which is where the console's trajectory view reads
	// the byte-exact answer from (ADR 0117).
	chunks []wireChunks
	// headerConfig is the provider/model of the last request header this run
	// logged. The console's request-prompt card is anchored on the header event,
	// and upstream logs one only when the model-visible request changes -- the
	// first request of a series, or a different provider/model (agent-loop
	// buildRequest) -- so an unchanged request must not mint a second card.
	headerConfig string
	// pending holds the records the last durable event produced beyond the first,
	// in order. One durable event can need more than one console record, and the
	// console renders them in this order.
	pending []Event
}

// wireChunks is one content block's streamed timeline, compacted the way the
// console stores it inside a settled message: the first chunk's time, the gaps
// between the chunks that follow, and the chunks themselves
// (api/session-controller expandAssistantStream text-chunks/reasoning-chunks).
type wireChunks struct {
	kind  string
	time0 int64
	dt    []int64
	texts []string
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

	// Events holds one wire record per console-meaningful durable event, in log
	// order, numbered by the session sequence.
	Events []Event
	// Source holds the durable event each record was projected from, aligned with
	// Events by index. It is not one entry per durable event: events with no
	// console meaning are absent.
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

// Append projects more durable events onto the projection, in order. An event
// that projects to no record is consumed and contributes nothing.
func (p *Projection) Append(events ...zenforge.Event) {
	for _, event := range events {
		record, ok := p.projector.Next(event)
		for ok {
			p.Events = append(p.Events, record)
			p.Source = append(p.Source, event)
			record, ok = p.projector.Pop()
		}
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

// Next projects one durable event. The second result is false when the event has
// no console record. Records beyond the first -- the question a step answers, which
// follows the step's own opening marker -- are drained with Pop.
func (p *Projector) Next(event zenforge.Event) (Event, bool) {
	p.pending = p.project(event)
	return p.Pop()
}

// Pop returns the next queued record from the last durable event, if any. The
// projection drains it so one durable event can produce several records without
// losing the ones after the first.
func (p *Projector) Pop() (Event, bool) {
	if len(p.pending) == 0 {
		return Event{}, false
	}
	next := p.pending[0]
	p.pending = p.pending[1:]
	return next, true
}

// project returns every record one durable event produces, in order.
func (p *Projector) project(event zenforge.Event) []Event {
	out := make([]Event, 0, 2)
	switch event.Type {
	case zenforge.EventRunStarted:
		// The turn's opening marker comes first: it is what the console hangs the
		// turn's process row off, and without it the whole step timeline for the
		// turn is dropped (session-controller turnProcessDefinition).
		out = append(out, p.mustEmit(Event{Time: event.Timestamp}.known("turn/start",
			map[string]any{"turn": p.identity.turn()})))
		data := payload(event)
		if text, ok := stringField(data, "input"); ok && text != "" {
			// The question follows immediately. Upstream records it inside the step
			// that answers it, but holding it back would hide a prompt the operator
			// just submitted until the model's first step opens, and this host has no
			// separate echo to carry it in the meantime. The console does not require
			// the upstream order either: its turn row anchors on turn/start and its
			// step rows on step/start (session-controller turnProcessDefinition), so
			// the question renders where it belongs either way.
			//
			// The caller's identity for this prompt (Task.PromptID) is the console's
			// requestId: it retires the echo it painted locally when the durable
			// message carrying that identity renders (api-session-controller
			// observeSubmissionEvent, ui-chat observedRpcIds), and without it the echo
			// stays on screen as a second copy of the question (ADR 0111).
			seq := p.nextSeq()
			out = append(out, p.surface(Event{Seq: seq, Time: event.Timestamp}, "user/message",
				userMessage(seq, text, promptIdentity(data))))
		}
		return out
	case zenforge.EventSystemPrompt:
		// One system node per section, which is how the prompt is assembled: the
		// console keeps the loaded system nodes in surface order and reads the last
		// non-empty one as the prompt in force (ui-conversation
		// contract/system-prompt.ts inspectSystemPrompt).
		step, _ := intField(payload(event), "step")
		sections, _ := payload(event)["sections"].([]any)
		for _, raw := range sections {
			text, ok := raw.(string)
			if !ok || text == "" {
				continue
			}
			seq := p.nextSeq()
			out = append(out, p.surface(Event{Seq: seq, Time: event.Timestamp}, "system/message",
				map[string]any{
					"turn": p.identity.turn(),
					"step": step,
					"message": map[string]any{
						"content": []any{map[string]any{"type": "text", "text": text}},
					},
				}))
		}
		return out
	case zenforge.EventRunCancelled, zenforge.EventRunError:
		// The turn's close comes last, after the tool calls it left open have been
		// answered.
		out := p.interruptedToolResults(event)
		record, ok := p.projectOne(event)
		if ok {
			out = append(out, record)
		}
		return out
	}
	record, ok := p.projectOne(event)
	if ok {
		out = append(out, record)
	}
	return out
}

// mustEmit emits one record and returns it. Every minted record is emitted, so a
// caller that needs the record cannot fail.
func (p *Projector) mustEmit(event Event) Event {
	record, _ := p.emit(event)
	return record
}

// projectOne projects one durable event into at most one record. The second result
// is false when the event has no console record: either it is this host's
// bookkeeping or a model delta, or it is a console event this host cannot project
// truthfully. A record's sequence is this turn's next unused number, which is only
// committed when a record is produced -- so the served sequence counts records, not
// durable events.
func (p *Projector) projectOne(event zenforge.Event) (Event, bool) {
	data := payload(event)
	base := Event{Seq: p.identity.SeqOffset + int64(p.records) + 1, Time: event.Timestamp, Data: data}

	switch event.Type {
	case zenforge.EventRequestSteer:
		// The harness records a queued turn as `request.steer` with the text
		// under "message"; without that key the console would never see the
		// question it queued, only its own local echo.
		if text, ok := firstStringField(data, "input", "text", "content", "message"); ok {
			// A queued turn arrives durably as request.steer; the host passes the
			// prompt's requestId as the steer id, so the same identity is here.
			return p.emit(p.surface(base, "user/message", userMessage(base.Seq, text, steerIdentity(data))))
		}
	case zenforge.EventSessionTitle:
		// The console names a conversation from the `title` projection it folds out
		// of this event, and the projection is the only place the name can come
		// from: a list row seeds its projection store with it and the header reads
		// the same cell, while a title field of its own renders nowhere
		// (api-session-controller/src/client/sessions/manager.ts). The durable
		// record carries the name and a source string; the wire source is a tagged
		// object, and the two kinds this host writes are an operator's rename and a
		// title derived from the first prompt (ADR 0119).
		if title, ok := stringField(data, "title"); ok && title != "" {
			kind := "fallback"
			if source, ok := stringField(data, "source"); ok && source == "user" {
				kind = "user"
			}
			return p.emit(base.known("session/title", map[string]any{
				"title":       title,
				"messageSeqs": []any{},
				"source":      map[string]any{"kind": kind},
			}))
		}
	case zenforge.EventStepStarted:
		if step, ok := intField(data, "step"); ok {
			p.current = step
			return p.emit(base.known("step/start", p.turnStep(step)))
		}
	case zenforge.EventStepDone:
		if step, ok := intField(data, "step"); ok {
			return p.emit(base.known("step/end", p.turnStep(step)))
		}
	case zenforge.EventModelStarted:
		// A new attempt starts a fresh stream; a retried step must not carry the
		// failed attempt's text into its settlement.
		step, _ := intField(data, "step")
		p.current = step
		p.resetStep()
		// The request itself, which is what the console's request-prompt card and
		// its context meter are built from. The provider/model are the run's own
		// identity: the host does not record them per attempt, because they are the
		// route the run was started on.
		config := p.identity.Provider + "\x00" + p.identity.Model
		if config != p.headerConfig {
			reason := "change"
			if p.headerConfig == "" {
				reason = "initial"
			}
			p.headerConfig = config
			return p.emit(base.known("request/header", map[string]any{
				"turn":   p.identity.turn(),
				"step":   step,
				"reason": reason,
				"header": map[string]any{
					"config": map[string]any{
						"provider": p.identity.Provider,
						"model":    p.identity.Model,
					},
				},
			}))
		}
	case zenforge.EventModelDelta:
		if delta, ok := stringField(data, "textDelta"); ok {
			p.appendBlock("text", delta, base.Time)
		}
	case zenforge.EventModelReasoning:
		if delta, ok := stringField(data, "textDelta"); ok {
			p.appendBlock("reasoning", delta, base.Time)
		}
	case zenforge.EventModelUsage:
		if usage, ok := mapField(data, "usage"); ok {
			p.usage = TokenUsage(usage)
		}
	case zenforge.EventModelDone:
		step, ok := intField(data, "step")
		if !ok {
			step = p.current
		}
		p.current = step
		content, usage, chunks := p.takeStep()
		if len(content) == 0 {
			// A step that settled without model-visible content is an attempt,
			// not a message: the console records it as log-only history.
			return p.emit(base.known("assistant/attempt", map[string]any{
				"turn": p.identity.turn(), "step": step, "stream": []any{},
			}))
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
			"stream": p.compactStream(chunks, usage, base.Time, toolCallFinishKind(data)),
		}
		if usage != nil {
			message["usage"] = usage
		}
		return p.emit(p.surface(base, "assistant/message", message))
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
		p.calls = append(p.calls, callID)
		return p.emit(base.known("tool/call", map[string]any{
			"turn":      p.identity.turn(),
			"step":      step,
			"callId":    callID,
			"name":      name,
			"arguments": rawArguments(data["arguments"]),
		}))
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
		p.resolveCall(callID)
		return p.emit(p.surface(base, "tool/result", p.toolResult(base.Seq, step, callID, output, event.Type == zenforge.EventToolError, nil)))
	case zenforge.EventRunDone:
		return p.emit(base.known("turn/end", map[string]any{
			"turn": p.identity.turn(), "reason": map[string]any{"kind": "completed"},
		}))
	case zenforge.EventRunError:
		message, _ := firstStringField(data, "error", "message")
		if message == "" {
			message = "the run failed"
		}
		return p.emit(base.known("turn/end", map[string]any{
			"turn": p.identity.turn(),
			"reason": map[string]any{
				"kind":  "error",
				"error": map[string]any{"message": message, "code": "UNKNOWN"},
			},
		}))
	case zenforge.EventRunCancelled:
		return p.emit(base.known("turn/end", map[string]any{
			"turn":   p.identity.turn(),
			"reason": map[string]any{"kind": "aborted", "reason": map[string]any{"kind": "user"}},
		}))
	}
	// A type the console knows but this host does not map keeps its own name and
	// payload, so nothing the console can read is lost. A type outside the
	// console's vocabulary is not served at all: upstream's session log holds only
	// the console's vocabulary, and forwarding the rest would put records the
	// console skips into its window and its sequence (ADR 0117).
	if !KnownEventTypes[string(event.Type)] {
		return Event{}, false
	}
	return p.emit(Event{Type: string(event.Type), Time: base.Time, Data: data})
}

// emit commits one record's sequence number. Nothing else may advance the
// counter: a number the console is served must never be skipped, because its
// journal stream reads a gap as a carrier failure and reconnects.
func (p *Projector) emit(event Event) (Event, bool) {
	event.Seq = p.nextSeq()
	return event, true
}

// nextSeq reserves the turn's next console sequence. It is reserved rather than
// predicted so a record can be built from its own number.
func (p *Projector) nextSeq() int64 {
	p.records++
	return p.identity.SeqOffset + int64(p.records)
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
	p.chunks = nil
}

func (p *Projector) takeStep() ([]map[string]any, map[string]any, []wireChunks) {
	content, usage, chunks := p.blocks, p.usage, p.chunks
	p.resetStep()
	if content == nil {
		content = []map[string]any{}
	}
	return content, usage, chunks
}

// appendBlock adds a delta to the step's content, merging into the previous
// block while it is the same kind so a streamed answer is one block. The same
// merge keeps the block's compact timeline in step with it: both are consumed by
// the settled message. A delta whose time is not after the previous one is
// recorded as no gap, so the timeline the console reads is monotone.
func (p *Projector) appendBlock(kind, text string, time int64) {
	if text == "" {
		return
	}
	if count := len(p.blocks); count > 0 && p.blocks[count-1]["type"] == kind {
		previous, _ := p.blocks[count-1]["text"].(string)
		p.blocks[count-1]["text"] = previous + text
		if count := len(p.chunks); count > 0 {
			block := &p.chunks[count-1]
			gap := time - (block.time0 + sumInts(block.dt))
			if gap < 0 {
				gap = 0
			}
			block.dt = append(block.dt, gap)
			block.texts = append(block.texts, text)
		}
		return
	}
	p.blocks = append(p.blocks, map[string]any{"type": kind, "text": text})
	p.chunks = append(p.chunks, wireChunks{kind: kind, time0: time, texts: []string{text}})
}

// compactStream renders the step's block timelines as the console's compact
// stream, which is what a settled assistant message carries. The console expands
// it back into byte-exact timed deltas for the trajectory view
// (api/session-controller expandAssistantStream).
// toolCallFinishKind is the reason a response finished, in the console's own union:
// a response that asked for tool calls is a different end state from one that
// stopped, and the client reads it from the finish chunk
// (`{type: "finish", reason: {kind: "stop" | "tool-calls"}}`, the pair upstream's
// own fixtures use).
func toolCallFinishKind(data map[string]any) string {
	if count, ok := intField(data, "toolCallCount"); ok && count > 0 {
		return "tool-calls"
	}
	return "stop"
}

// compactStream compacts an attempt's text deltas into the records the console
// expands back into chunks, and appends the two chunks a provider sends after its
// text: the token accounting and the finish reason. They are verbatim chunk records
// -- the client's expander passes any `{type: "chunk"}` through untouched -- which is
// what makes the usage pill readable from a settlement the console loaded rather than
// streamed (lastAssistantStreamChunk(stream, "usage")).
func (p *Projector) compactStream(chunks []wireChunks, usage map[string]any, time int64, finish string) []any {
	stream := make([]any, 0, len(chunks))
	for index, block := range chunks {
		if len(block.texts) == 0 {
			continue
		}
		kind := "text-chunks"
		if block.kind == "reasoning" {
			kind = "reasoning-chunks"
		}
		gaps := make([]any, 0, len(block.dt))
		for _, gap := range block.dt {
			gaps = append(gaps, gap)
		}
		texts := make([]any, 0, len(block.texts))
		for _, text := range block.texts {
			texts = append(texts, text)
		}
		stream = append(stream, map[string]any{
			"type":  kind,
			"time0": block.time0,
			"index": index,
			"dt":    gaps,
			"texts": texts,
		})
	}
	if usage != nil {
		stream = append(stream, map[string]any{
			"type": "chunk", "time": time,
			"chunk": map[string]any{"type": "usage", "usage": usage},
		})
	}
	stream = append(stream, map[string]any{
		"type": "chunk", "time": time,
		"chunk": map[string]any{"type": "finish", "reason": map[string]any{"kind": finish}},
	})
	return stream
}

func sumInts(values []int64) int64 {
	total := int64(0)
	for _, value := range values {
		total += value
	}
	return total
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

func userMessage(seq int64, text, rpcID string) map[string]any {
	source := map[string]any{"kind": "user"}
	if rpcID != "" {
		source["rpcId"] = rpcID
	}
	return map[string]any{
		"id":      messageID(seq),
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": text}},
		"source":  source,
	}
}

// promptIdentity is the caller's identity for the prompt a run started with, as
// the deep layer recorded it beside the input. Absent for a run nobody labelled
// (a CLI run, a resumed run), and then the console simply has no echo to retire.
func promptIdentity(data map[string]any) string {
	value, _ := stringField(data, "promptId")
	return strings.TrimSpace(value)
}

// steerIdentity is the same identity on the queued-turn path, where the host
// passes the prompt's requestId as the steer id.
func steerIdentity(data map[string]any) string {
	value, _ := stringField(data, "steerId")
	return strings.TrimSpace(value)
}

// toolResult carries the tool's model-facing result, correlated with its call.
// The console validates a `tool/result` payload: when it has no `error` field
// the block is the only place failure is stated, which is the shape this host
// can honestly produce -- it has the tool's exit state, not the console's
// structured failure identity.
func (p *Projector) toolResult(seq int64, step int, callID, output string, isError bool, failure map[string]any) map[string]any {
	block := map[string]any{
		"type":       "tool-result",
		"toolCallId": callID,
		"content":    []any{map[string]any{"type": "text", "text": output}},
	}
	if isError {
		block["isError"] = true
	}
	if failure != nil {
		// The console reads the failure off the block, and its own validator
		// requires an error to be accompanied by isError on the first content block.
		block["isError"] = true
		block["error"] = failure
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

// resolveCall forgets a tool call that has been answered.
func (p *Projector) resolveCall(callID string) {
	for index, pending := range p.calls {
		if pending == callID {
			p.calls = append(p.calls[:index], p.calls[index+1:]...)
			return
		}
	}
}

// interruptedToolResults answers every tool call this turn left open. A run that is
// cancelled or fails while a tool is running never records its result, and the
// console renders a call without one as still running -- forever. Upstream answers
// those calls with an error result of its own (`AbortError` /
// `ABORTED_BEFORE_DISPATCH`, and `Interrupted` / `interrupted` for the tool node),
// so the console shows the call as stopped rather than live. This host cannot tell a
// call that never dispatched from one that was running, so every pending call is
// reported as interrupted, which is what the console renders as "stopped".
func (p *Projector) interruptedToolResults(event zenforge.Event) []Event {
	if len(p.calls) == 0 {
		return nil
	}
	pending := p.calls
	p.calls = nil
	out := make([]Event, 0, len(pending))
	for _, callID := range pending {
		step := p.steps[callID]
		if step == 0 {
			step = p.current
		}
		seq := p.nextSeq()
		out = append(out, p.surface(Event{Seq: seq, Time: event.Timestamp}, "tool/result",
			p.toolResult(seq, step, callID, "", false, map[string]any{
				"name": "Interrupted",
				"code": "interrupted",
			})))
	}
	return out
}

// TokenUsage maps the host's accounting onto the console's TokenUsage, whose names
// are the model-facing ones. The total is only written when the adapter reported it:
// the console treats an absent total as "unavailable", and it renders the usage pill
// only from a stream chunk carrying both counts (ui-chat normalizeUsage requires
// inputTokens and outputTokens).
func TokenUsage(usage map[string]any) map[string]any {
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
