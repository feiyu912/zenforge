package dshstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshwire"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

const (
	// defaultFollowMessages matches the opening-window size upstream uses when
	// a follow request omits maxMessages
	// (session-controller/src/history.ts DEFAULT_MAX_MESSAGES).
	defaultFollowMessages = 50
	// maxFollowMessages bounds one snapshot so a hostile or buggy maxMessages
	// cannot make the host build an unbounded JSON array.
	maxFollowMessages = 1000
)

// sessionProjectionBaseline is the followed session's opening projection values.
// The watermark is the session-log cursor the snapshot already cites -- the value
// is current as of that cursor -- while the selection's own sequence orders the
// control stream's live updates.
func (h *Handler) sessionProjectionBaseline(sessionID string, cursor int64, title string) projectionBaseline {
	values := map[string]any{}
	if title != "" {
		// The header and the sidebar both read the `title` cell, so a reconnected
		// console repaints the conversation's name instead of falling back to the
		// raw session id (ADR 0119).
		values[dshwire.TitleProjection] = title
	}
	if h.cfg.ModelSelections != nil {
		// The key is registered for every session this host serves, not only for
		// sessions a model has already been chosen in. The console's selector reads
		// the projection to decide whether the capability exists at all, and while
		// the key is absent it holds its own "Loading models…" state with no groups
		// (ui-model-selection/src/client/directory.ts:146-160) even though the
		// catalog answered: an empty projection is the honest value for a session
		// with no selection, meaning no next and no last-used, so the client falls
		// back to the catalog's default model.
		state, selected := h.cfg.ModelSelections()[sessionID]
		if !selected {
			state = ModelSelectionState{}
		}
		values[modelSelectionProjectionKey] = state.Projection
	}
	return projectionBaseline{AsOfSeq: cursor, Values: values}
}

// runFollow serves the session/follow logical stream.
//
// It sends exactly one opening snapshot, then the conversation's durable events
// as they are appended -- moving on to its next turn when the turn being followed
// ends -- and stays open until the client goes away. The snapshot and the live
// frames use the same SessionWireEvent mapping session/page already uses, so the
// two history paths cannot disagree about a record's shape, and both serve the
// session's whole conversation: each turn's events are shifted past the turns
// before them (dshwire.Session), so a second prompt does not restart the sequence
// the console cursors on.
//
// A turn's log ending is deliberately not the stream's end. The client builds a
// carrier failure for a stream that ends after its opening snapshot
// (api/gateway/src/client/journal-stream.ts, the `ended` callback) and reconnects,
// and every reconnect reinstalls the tail window over whatever "load earlier"
// added -- so ending here makes the conversation page backwards forever without
// ever showing less, and the console answers a click on "load earlier" with
// nothing (ADR 0114). The stream therefore waits on the finished turn and follows
// the conversation's next turn when one starts.
//
// The answer arrives twice over, and the two are not alternatives. The durable
// projection carries each step's settled assistant message, and the dense
// assistant-stream frames carry the prose while it is still being written -- the
// console renders live text from those frames alone, so a host that sent only the
// settlement would show the operator an answer that appears all at once (ADR 0116).
//
// A tracker is rebuilt from the newest turn's durable log before the snapshot is
// written, so a console that reconnects in the middle of an answer is handed the
// attempt it was rendering (ADR 0118) and the live tail continues that same
// attempt rather than announcing a second start. The snapshot's baseline cites the
// tracker's own frame counter and, when the turn has settled, omits the attempt
// (ADR 0119).
func (h *Handler) runFollow(ctx context.Context, payload []byte, send func(any) error) error {
	request, failure := decodeFollowRequest(payload)
	if failure != nil {
		return failure
	}

	// The session's served log is its whole conversation: every turn's records in
	// one session-wide sequence, with the newest turn's projection to tail. Turn
	// one is the session id itself, so a session that has never been prompted has
	// no turns and no records.
	turnIdentity := func(turn int) dshwire.Identity {
		identity := h.wireIdentity(request.sessionID)
		identity.Turn = turn
		return identity
	}
	log, err := dshwire.Session(ctx, h, request.sessionID, turnIdentity)
	if err != nil {
		return streamFail(codeInternal, "read session log: "+err.Error(), nil)
	}
	runID := log.NewestRun
	info, infoErr := h.manager.Get(runID)
	draft := len(log.Runs) == 0
	if draft {
		// A session this host created and no turn has started -- the draft the
		// console opens before its first prompt -- has an empty log and no run to
		// attach to. That is not a missing session: its history is empty, and the
		// stream is served (cursor -1, no records) so the client can open the
		// conversation. The tail then waits for the run the first prompt starts
		// instead of attaching to nothing. An id this host never created is still
		// not-found.
		if !h.isDraftSession(request.sessionID) {
			return streamFail(codeSessionNotFound, fmt.Sprintf("session %q not found", request.sessionID),
				map[string]any{"sessionId": request.sessionID})
		}
		runID = request.sessionID
	}

	// cursor is the session's newest sequence. The client requires the snapshot's
	// last record to end exactly at cursor, an empty page to cite upstream's empty
	// cursor of -1, every live event to be one past the cursor, and a resumed
	// generation to cite a cursor at or ahead of the last entry it applied
	// (api/gateway/src/client/journal-stream.ts assertPageThrough + follows +
	// opening). A session-wide sequence is what makes all four hold across turns.
	cursor := log.Cursor()
	// The console renders the answer from dense assistant-stream frames, not from
	// the ignorable records its deltas project to, so the tail mints them from the
	// same durable events. Only a client that asked for the stream's baseline gets
	// them: the frames and the baseline are one capability.
	var assistant *assistantTracker
	if request.assistantStream {
		// Rebuilding the tracker from the newest turn's durable log is what makes a
		// reconnect mid-answer resume: an attempt that is still streaming is handed
		// over in the snapshot's baseline and then continued by the live tail,
		// instead of being announced a second time (ADR 0118).
		assistant = newAssistantTracker(runID, cursor)
		if len(log.NewestEvents) > 0 {
			assistant = replayAssistant(runID, log.NewestTurn, cursor, log.NewestIdentity, log.NewestEvents)
		}
	}
	// The conversation's name travels as a projection cell, not as a field: the
	// header and the sidebar both fold it from there (ADR 0119).
	title, _ := log.Title()
	window, hasMore := log.Window(request.maxMessages)
	records := make([]eventRecord, 0, len(window))
	for _, event := range window {
		records = append(records, eventRecord{Type: "event", Event: event})
	}
	events := []zenforge.Event(nil)
	if log.Newest != nil {
		events = log.Newest.Source
	}
	snapshot := snapshotFrame{
		Type:            "snapshot",
		Header:          followHeader(request.sessionID, info, infoErr, events),
		Cursor:          cursor,
		Records:         records,
		HasMore:         hasMore,
		Projections:     h.sessionProjectionBaseline(request.sessionID, cursor, title),
		AssistantStream: nil,
	}
	if request.assistantStream {
		// The revision is the generation's frame counter, not a constant: a tracker
		// rebuilt from the durable log has already numbered the frames it replayed,
		// and the client holds every frame to revision+1 from the snapshot onward.
		// Reporting 0 after a replay makes the first live frame a carrier failure
		// (api/session-controller/src/client/transport.ts:87,100-105), which tears
		// the stream down and reconnects forever (ADR 0119).
		snapshot.AssistantStream = &assistantBaseline{
			Revision:      assistant.revision,
			ActiveAttempt: assistant.baselineOf(),
		}
	}
	if err := send(snapshot); err != nil {
		return err
	}

	// The live tail continues the newest turn's projection, and attaches at that
	// turn's own durable tail: the manager's follower speaks the run's sequence,
	// not the session's shifted one. Attach subscribes before reading the durable
	// watermark and then replays through it, so an append that races the snapshot
	// is delivered exactly once and in seq order.
	tail := log.Newest
	afterSeq := log.NewestTail
	if afterSeq < 0 {
		afterSeq = 0
	}
	draftTurn := log.NewestTurn
	if draft {
		// The run does not exist yet; session/prompt creates it. Wait for it here
		// rather than attaching to a run that is not there, so the first turn
		// arrives over this connection instead of after a client reconnect. The
		// empty snapshot cited cursor -1, so the console requires the first event
		// to be sequence 1: attach at the beginning and let the replay send the
		// whole first turn. The tail starts empty and is fed by that replay, which
		// projects it exactly once.
		draftTurn = 1
		tail = dshwire.Project(nil, turnIdentity(1))
		afterSeq = 0
	}
	if tail == nil {
		// A turn whose log is still empty (a run that has just started) has no
		// events to project yet; its first event creates the record.
		tail = dshwire.Project(nil, turnIdentity(draftTurn))
	}
	if assistant != nil {
		assistant.startTurn(runID, draftTurn)
	}
	for {
		if draft {
			if err := h.awaitDraftRun(ctx, runID); err != nil {
				return err
			}
			draft = false
		}
		live, liveErr, err := h.manager.Attach(ctx, runID, afterSeq)
		if err != nil {
			if errors.Is(err, harnesshttp.ErrRunNotFound) {
				return streamFail(codeSessionNotFound, fmt.Sprintf("session %q not found", request.sessionID),
					map[string]any{"sessionId": request.sessionID})
			}
			return streamFail(codeInternal, "follow session: "+err.Error(), nil)
		}
		ended, failure := pumpTurn(ctx, live, liveErr, tail, assistant, send)
		if failure != nil {
			return failure
		}
		if !ended {
			return ctx.Err()
		}
		// The turn ended; the stream does not. Wait for the conversation's next
		// turn, then continue the same sequence from its first event.
		next, err := h.awaitSessionTurn(ctx, request.sessionID, runID)
		if err != nil {
			return err
		}
		if next == "" {
			return ctx.Err()
		}
		nextLog, err := dshwire.Session(ctx, h, request.sessionID, turnIdentity)
		if err != nil {
			return streamFail(codeInternal, "read session log: "+err.Error(), nil)
		}
		runID = next
		afterSeq = 0
		// The next turn's events are all new to this stream, and its stamped
		// identity is where its own durable sequence moves onto the session's, so
		// the first frame it projects is one past the cursor the console holds.
		tail = dshwire.Project(nil, nextLog.NewestIdentity)
		if assistant != nil {
			assistant.startTurn(runID, nextLog.NewestTurn)
		}
	}
}

// pumpTurn forwards one turn's durable events onto the session sequence until the
// turn's log ends, the client goes away or the stream fails. ended reports that
// the turn reached the end of its log normally: the caller continues the
// conversation with its next turn instead of ending the stream.
//
// Each durable event is sent twice over the same connection: its console record,
// and (for a client that asked for the assistant stream) the dense frames its
// model deltas and settlement produce. The frames are minted before the record
// and its settlement's end frame after it, because the client stages a settlement
// while its attempt is open and publishes it when the end frame names its
// sequence.
func pumpTurn(ctx context.Context, live <-chan zenforge.Event, liveErr <-chan error, tail *dshwire.Projection, assistant *assistantTracker, send func(any) error) (bool, error) {
	// A turn that ends with an attempt still open -- cancelled mid-answer, or a log
	// that stops between attempts -- leaves the console rendering text that will
	// never settle. Closing it is what keeps the next turn's start frame from
	// making the client rebaseline.
	defer func() {
		if assistant == nil {
			return
		}
		for _, frame := range assistant.close() {
			if err := send(frame); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case followErr, ok := <-liveErr:
			if ok && followErr != nil {
				return false, streamFail(codeInternal, "follow session: "+followErr.Error(), nil)
			}
			// The error channel closing with no value means the durable follower
			// reached the log's end normally; the event channel drains first.
		case event, ok := <-live:
			if !ok {
				return true, nil
			}
			if assistant != nil {
				for _, frame := range assistant.onEvent(event) {
					if err := send(frame); err != nil {
						return false, err
					}
				}
			}
			// Continue the snapshot's projection of this turn rather than starting
			// a new one: the step being streamed is the same step, its settlement
			// must carry the deltas the snapshot already counted, and the turn's
			// sequence offset is what keeps this record one past the cursor the
			// console holds. An event with no console record -- the host's own
			// bookkeeping, and the deltas the dense stream already carried --
			// advances the projection's state and nothing else (ADR 0117).
			before := len(tail.Events)
			tail.Append(event)
			// Every record the event produced is sent, in order. One durable event
			// can produce more than one -- a turn's opening marker and the question
			// behind it -- and sending only the last would drop the marker the
			// console anchors the turn's process row on while still counting it in
			// the sequence, which leaves every later frame numbered one past the
			// cursor the console holds.
			for _, projected := range tail.Events[before:] {
				if err := send(eventRecord{Type: "event", Event: projected}); err != nil {
					return false, err
				}
				if assistant != nil {
					for _, frame := range assistant.onRecord(projected) {
						if err := send(frame); err != nil {
							return false, err
						}
					}
				}
			}
		}
	}
}

// sessionTurnPollInterval is how often a live follow re-checks whether the
// conversation has moved on to another turn. The wait is a poll for the same
// reason awaitDraftRun's is: the run registry has no "a turn was created"
// notification, and the follower a finished turn leaves behind speaks only that
// run. A quarter of a second keeps the next turn's first frame close to immediate
// without a timer per open conversation waking more often than it needs to.
const sessionTurnPollInterval = 250 * time.Millisecond

// awaitSessionTurn waits until the conversation has a turn after current and
// returns its run id. An empty id means the context ended first.
func (h *Handler) awaitSessionTurn(ctx context.Context, sessionID, current string) (string, error) {
	ticker := time.NewTicker(sessionTurnPollInterval)
	defer ticker.Stop()
	for {
		turns, err := h.Turns(ctx, sessionID)
		if err != nil {
			return "", streamFail(codeInternal, "read session turns: "+err.Error(), nil)
		}
		if len(turns) > 0 && turns[len(turns)-1] != current {
			return turns[len(turns)-1], nil
		}
		select {
		case <-ctx.Done():
			return "", nil
		case <-ticker.C:
		}
	}
}

// wireIdentity names the provider and model the projected transcript attributes a
// session's assistant messages to: the session's own choice when it made one,
// otherwise the host's configured default. It is provenance on the wire -- the
// run is served by whatever adapter the selection path applied -- so an empty
// answer leaves the label unset rather than inventing a route.
func (h *Handler) wireIdentity(sessionID string) dshwire.Identity {
	if h.cfg.ModelSelections != nil {
		if state, known := h.cfg.ModelSelections()[sessionID]; known {
			selection := state.Projection.Next
			if selection == nil {
				selection = state.Projection.LastUsed
			}
			if selection != nil && (selection.Provider != "" || selection.Model != "") {
				return dshwire.Identity{Provider: selection.Provider, Model: selection.Model}
			}
		}
	}
	if h.cfg.ModelDefault != nil {
		return h.cfg.ModelDefault()
	}
	return dshwire.Identity{}
}

// isDraftSession reports whether the RPC handler created this session without a
// turn in it yet. A transport with no draft seam says no, which keeps every
// unknown id a not-found rather than inventing an empty session for it.
func (h *Handler) isDraftSession(sessionID string) bool {
	if h.cfg.DraftSessions == nil {
		return false
	}
	return h.cfg.DraftSessions(sessionID)
}

// draftRunPollInterval is how often a draft's stream re-checks whether the first
// prompt has created the run. The wait is a poll because the run manager exposes
// no "a run was created" notification: eventlog.Bus is per-run and a run that
// does not exist has no bus to subscribe to. It is short enough that the first
// turn's first frame follows the prompt immediately.
const draftRunPollInterval = 50 * time.Millisecond

// awaitDraftRun waits until the run serving a draft session exists. The caller
// has already sent the empty snapshot, so this is the tail of a stream that was
// opened before the conversation's first turn: the wait is bounded by the
// client's own connection.
func (h *Handler) awaitDraftRun(ctx context.Context, runID string) error {
	ticker := time.NewTicker(draftRunPollInterval)
	defer ticker.Stop()
	for {
		if _, err := h.manager.Get(runID); err == nil {
			return nil
		} else if !errors.Is(err, harnesshttp.ErrRunNotFound) {
			return streamFail(codeInternal, "look up session: "+err.Error(), nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// followRequest is one validated session/follow request.
type followRequest struct {
	sessionID       string
	maxMessages     int
	assistantStream bool
}

// decodeFollowRequest validates the follow args. The shipped console sends the
// named request object:
//
//	{request: {address: {kind: "session", sessionId}, maxMessages?, assistantStream?}}
//
// and the flattened spelling without the request wrapper is accepted too, so a
// caller that sends the fields directly is not refused over a shape the
// protocol does not distinguish. Only the top-level "session" address arm is
// served; a subagent address names a child session this host does not model,
// exactly as session/page reports.
func decodeFollowRequest(payload []byte) (followRequest, *streamError) {
	args, failure := endpointArgs(payload)
	if failure != nil {
		return followRequest{}, failure
	}
	if _, ok := args["address"]; !ok {
		if rawRequest, ok := args["request"]; ok {
			request, err := decodeJSONObject(rawRequest)
			if err != nil {
				return followRequest{}, streamFail(codeArgumentsInvalid, `"request" must be a JSON object`,
					map[string]any{"argument": "request"})
			}
			args = request
		}
	}
	rawAddress, ok := args["address"]
	if !ok {
		return followRequest{}, streamFail(codeArgumentsInvalid, `argument "address" is required`,
			map[string]any{"argument": "address"})
	}
	address, err := decodeJSONObject(rawAddress)
	if err != nil {
		return followRequest{}, streamFail(codeArgumentsInvalid, `"address" must be a JSON object`,
			map[string]any{"argument": "address"})
	}
	kind := ""
	if rawKind, ok := address["kind"]; ok {
		if err := json.Unmarshal(rawKind, &kind); err != nil {
			return followRequest{}, streamFail(codeArgumentsInvalid, `"address.kind" must be a string`,
				map[string]any{"argument": "address"})
		}
	}
	switch kind {
	case "session", "":
		// An address without a kind is tolerated; the only arm this host
		// serves is the top-level session.
	case "subagent":
		return followRequest{}, streamFail(codeUnimplemented,
			"session/follow does not support subagent addresses: this host serves top-level runs only",
			map[string]any{"addressKind": "subagent"})
	default:
		return followRequest{}, streamFail(codeArgumentsInvalid, fmt.Sprintf("address.kind %q is unknown", kind),
			map[string]any{"argument": "address"})
	}
	sessionID := ""
	if rawID, ok := address["sessionId"]; ok {
		if err := json.Unmarshal(rawID, &sessionID); err != nil {
			return followRequest{}, streamFail(codeArgumentsInvalid, `"address.sessionId" must be a string`,
				map[string]any{"argument": "address"})
		}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return followRequest{}, streamFail(codeArgumentsInvalid, `argument "address.sessionId" is required`,
			map[string]any{"argument": "address"})
	}

	maxMessages, hasMax, failure := intArg(args, "maxMessages")
	if failure != nil {
		return followRequest{}, failure
	}
	if !hasMax {
		maxMessages = defaultFollowMessages
	}
	if maxMessages <= 0 {
		return followRequest{}, streamFail(codeArgumentsInvalid, `"maxMessages" must be a positive integer`,
			map[string]any{"argument": "maxMessages"})
	}
	if maxMessages > maxFollowMessages {
		maxMessages = maxFollowMessages
	}

	assistantStream, hasAssistant, failure := boolArg(args, "assistantStream")
	if failure != nil {
		return followRequest{}, failure
	}
	if hasAssistant && !assistantStream {
		// Upstream's request type is literally `assistantStream?: true`; a
		// false value is not part of the protocol.
		return followRequest{}, streamFail(codeArgumentsInvalid, `"assistantStream" must be true when present`,
			map[string]any{"argument": "assistantStream"})
	}
	return followRequest{sessionID: sessionID, maxMessages: int(maxMessages), assistantStream: hasAssistant}, nil
}

// followHeader builds the snapshot's SessionWireHeader. createdAt prefers the
// run manager's start time and falls back to the log's first event, so a run
// whose manager record has been retained away still gets a truthful timestamp.
func followHeader(sessionID string, info harnesshttp.RunInfo, infoErr error, events []zenforge.Event) sessionHeader {
	createdAt := int64(0)
	if infoErr == nil && !info.StartedAt.IsZero() {
		createdAt = info.StartedAt.UnixMilli()
	} else if len(events) > 0 {
		createdAt = events[0].Timestamp
	}
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}
	return sessionHeader{
		Version:   3,
		ID:        sessionID,
		CreatedAt: createdAt,
		IsSeeded:  false,
	}
}
