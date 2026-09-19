package dshstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
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
func (h *Handler) sessionProjectionBaseline(sessionID string, cursor int64) projectionBaseline {
	values := map[string]any{}
	if h.cfg.ModelSelections != nil {
		if state, ok := h.cfg.ModelSelections()[sessionID]; ok {
			values[modelSelectionProjectionKey] = state.Projection
		}
	}
	return projectionBaseline{AsOfSeq: cursor, Values: values}
}

// runFollow serves the session/follow logical stream.
//
// It sends exactly one opening snapshot, then the run's durable events as they
// are appended, then ends cleanly when the run reaches a terminal event. The
// snapshot and the live frames use the same SessionWireEvent mapping
// session/page already uses, so the two history paths cannot disagree about a
// record's shape.
//
// What this host does not send is assistant-stream frames. The harness does
// stream model output, but as durable model.delta events carrying
// attemptId/step/chunkSeq/offset/textDelta (agent.go callModelAttemptDurable)
// — not as the console's process-local, cursorless revision/index protocol.
// Those deltas therefore arrive as ordinary durable event frames. Minting
// assistant-stream frames would require inventing a dense revision/index
// sequence whose exact reconnect semantics the upstream recon itself could not
// establish (docs/dsh-console-protocol-recon.md §8.5), so it is not done. The
// opted-in opening baseline is still sent, because the shipped client throws
// when it is missing; revision 0 is upstream's own fallback for a session with
// no active accumulator (session-controller/src/history.ts:185).
func (h *Handler) runFollow(ctx context.Context, payload []byte, send func(any) error) error {
	request, failure := decodeFollowRequest(payload)
	if failure != nil {
		return failure
	}

	// Follow the run serving this session's newest turn: a conversation that
	// has moved past its first run must stream the run actually answering.
	runID := h.currentRun(ctx, request.sessionID)
	info, infoErr := h.manager.Get(runID)
	events, err := h.events.Read(ctx, runID, 0, 0)
	if err != nil {
		return streamFail(codeInternal, "read session log: "+err.Error(), nil)
	}
	if len(events) == 0 && infoErr != nil {
		if errors.Is(infoErr, harnesshttp.ErrRunNotFound) {
			return streamFail(codeSessionNotFound, fmt.Sprintf("session %q not found", request.sessionID),
				map[string]any{"sessionId": request.sessionID})
		}
		return streamFail(codeInternal, "look up session: "+infoErr.Error(), nil)
	}

	// cursor is the durable tail. The client requires the snapshot's last
	// record to end exactly at cursor, and an empty page to cite upstream's
	// empty cursor of -1 (api/gateway/src/client/journal-stream.ts
	// assertPageThrough + SessionEventStream's emptyCursor).
	cursor := int64(-1)
	if len(events) > 0 {
		cursor = events[len(events)-1].Seq
	}
	window := events
	if len(events) > request.maxMessages {
		window = events[len(events)-request.maxMessages:]
	}
	records := make([]eventRecord, 0, len(window))
	for _, event := range window {
		records = append(records, eventRecord{Type: "event", Event: newWireEvent(event)})
	}
	snapshot := snapshotFrame{
		Type:            "snapshot",
		Header:          followHeader(request.sessionID, info, infoErr, events),
		Cursor:          cursor,
		Records:         records,
		HasMore:         len(window) < len(events),
		Projections:     h.sessionProjectionBaseline(request.sessionID, cursor),
		AssistantStream: nil,
	}
	if request.assistantStream {
		snapshot.AssistantStream = &assistantBaseline{Revision: 0}
	}
	if err := send(snapshot); err != nil {
		return err
	}

	// Attach subscribes before reading the durable watermark, then replays
	// through it, so an append that races the snapshot is delivered exactly
	// once and in seq order. A negative cursor means "the log was empty"; the
	// live tail then starts at seq 1.
	afterSeq := cursor
	if afterSeq < 0 {
		afterSeq = 0
	}
	live, liveErr, err := h.manager.Attach(ctx, runID, afterSeq)
	if err != nil {
		if errors.Is(err, harnesshttp.ErrRunNotFound) {
			return streamFail(codeSessionNotFound, fmt.Sprintf("session %q not found", request.sessionID),
				map[string]any{"sessionId": request.sessionID})
		}
		return streamFail(codeInternal, "follow session: "+err.Error(), nil)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case followErr, ok := <-liveErr:
			if ok && followErr != nil {
				return streamFail(codeInternal, "follow session: "+followErr.Error(), nil)
			}
			// The error channel closing with no value means the durable
			// follower reached the log's end normally.
		case event, ok := <-live:
			if !ok {
				return nil
			}
			if err := send(eventRecord{Type: "event", Event: newWireEvent(event)}); err != nil {
				return err
			}
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
