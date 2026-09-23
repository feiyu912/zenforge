package dshapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshsession"
	"github.com/feiyu912/zenforge/internal/dshwire"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// session/fork opens a new conversation whose history is another one's up to a
// completed turn: the message action that forks "from here" and the sidebar's
// fork button both call it, and the child is then an ordinary conversation the
// console opens, renames, prompts and deletes like any other.
//
// This file is where that becomes real here. The reference implements it by
// seeding a child session with a slice of the source's events
// (`agents.create({sessionId, seed, inheritedEventCount})`), and this host's
// sessions are runs in an event store with no seeding path, so the fork is a
// *copy*: the source's turn logs up to the boundary are written into the child's
// own run chain, which is what its transcript, its continuation history and the
// session list all read. Two consequences are deliberate:
//
//   - The child inherits whole turns. The reference cuts at a record and then
//     walks forward to the next `turn/start`, which lands on the same boundary
//     this host can express: turns are runs, so the boundary turn is the last
//     one copied and no later turn is touched.
//   - The copy is materialized once and then owns itself. Renaming the child,
//     deleting the source, or changing the source's later turns cannot reach it.
//
// The boundary rules, the refusals and their messages are the reference's, word
// for word (ADR 0129).

// sessionFork answers POST /api/session/fork.
func (h *Handler) sessionFork(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	// Every field the client's SessionForkRequest carries, and nothing else. The
	// caller's `increaseTitle` never reaches the wire: renaming the child is the
	// console's own follow-up step.
	if failure := rejectUnknownArguments(args, "sessionId", "atSeq"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	atSeq, hasAtSeq, failure := forkSeqArg(args["atSeq"])
	if failure != nil {
		return nil, failure
	}
	runs := h.sessionRunIDs(ctx, sessionID)
	if len(runs) == 0 && !h.isPending(sessionID) {
		return nil, fail(codeSessionNotFound, fmt.Sprintf("session %q not found", sessionID),
			map[string]any{"sessionId": sessionID})
	}
	log, err := dshwire.Session(ctx, h, sessionID, func(turn int) dshwire.Identity {
		identity := h.wireIdentity(sessionID)
		identity.Turn = turn
		return identity
	}, nil)
	if err != nil {
		return nil, fail(codeInternal, fmt.Sprintf("fork source unavailable for session %q: %s", sessionID, err),
			map[string]any{"sessionId": sessionID})
	}
	boundary, failure := forkBoundary(log, atSeq, hasAtSeq, sessionID)
	if failure != nil {
		return nil, failure
	}
	// The boundary is a turn's end, so every turn up to and including it is
	// complete: those are the turns the child inherits, and the first of them is
	// the child's first turn.
	inherited := runs[:log.TurnContaining(boundary)]

	childID, failure := h.forkChildID(ctx)
	if failure != nil {
		return nil, failure
	}
	// Read the source's inherited turns before writing anything: a log this host
	// cannot read is a fork that must not leave a half-written child behind.
	plan := make([]forkTurn, 0, len(inherited))
	for index, parentRun := range inherited {
		events, err := h.events.Read(ctx, parentRun, 0, 0)
		if err != nil {
			return nil, fail(codeInternal, fmt.Sprintf("fork source unavailable for session %q: %s", sessionID, err),
				map[string]any{"sessionId": sessionID})
		}
		plan = append(plan, forkTurn{runID: dshsession.ContinuationRunID(childID, index+1), events: events})
	}
	now := time.Now().UTC()
	for index, turn := range plan {
		for eventIndex, event := range turn.events {
			copied := zenforge.NewEvent(event.Type, turn.runID, event.Payload)
			// The inherited turn happened when the source's did, so its records
			// keep their time; only the record the fork creates is new (below).
			copied.Timestamp = event.Timestamp
			// The child's first turn opens by naming the conversation it was
			// copied from: this host has no session-metadata plane, so the lineage
			// travels in the log, where the console's list row reads it to nest the
			// fork under its source (dshwire.SessionLog.ParentSessionID). The
			// projection ignores the field, so the transcript is unaffected.
			if index == 0 && eventIndex == 0 {
				copied.Payload["parentSessionId"] = sessionID
			}
			if err := h.events.Append(ctx, copied); err != nil {
				return nil, fail(codeInternal,
					fmt.Sprintf("failed to fork session %q: %s (the partially written child is %q; delete it with session/delete)",
						sessionID, err, childID),
					map[string]any{"sessionId": childID})
			}
		}
		info := forkRunInfo(turn, index == len(plan)-1, now)
		if err := h.manager.Record(ctx, info); err != nil {
			return nil, fail(codeInternal, fmt.Sprintf("failed to fork session %q: %s", sessionID, err),
				map[string]any{"sessionId": childID})
		}
	}
	// The child belongs where its source is. This host runs every conversation in
	// one workspace, so "the source's workspace" is that one -- and a fork whose
	// events exist but whose row is missing from the sidebar is still openable,
	// which is why the child id goes back in the error the console reads
	// (workspaceAttachSessionId in api/session-controller).
	if workspaces := h.workspaceRegistry(); workspaces != nil {
		if err := workspaces.AttachSession("", childID); err != nil {
			return nil, fail(codeWorkspaceAttachFailed,
				fmt.Sprintf("session %q was forked but could not attach to workspace %q: %s", childID, workspaces.Root(), err),
				map[string]any{"sessionId": childID, "workspacePath": workspaces.Root()})
		}
	}
	// The child is a conversation the console can open right away, so its model
	// projection is seeded the same way a created session's is; the reference
	// composes the child with the host's default model rather than the source's
	// choice, and this is that default (ADR 0102, ADR 0103).
	if store := h.modelSelectionStore(); store != nil {
		if registrar, ok := store.(sessionRegistrar); ok {
			registrar.RegisterSession(childID)
		}
	}
	return map[string]any{"sessionId": childID}, nil
}

// forkTurn is one inherited turn of the source, ready to be written under the
// child's own run id.
type forkTurn struct {
	runID  string
	events []zenforge.Event
}

// forkRunInfo is the record of one inherited turn. The turn is complete by
// construction -- the boundary rule only ever returns a `turn/end` -- so the
// record is terminal, with no lease. The newest one is stamped with the moment
// of the fork: the conversation was created now even though the turns it
// inherited happened earlier, which is what keeps the child from sorting behind
// its own source in the sidebar.
func forkRunInfo(turn forkTurn, newest bool, now time.Time) harnesshttp.RunInfo {
	info := harnesshttp.RunInfo{RunID: turn.runID, Status: harnesshttp.RunCompleted}
	if len(turn.events) > 0 {
		info.StartedAt = time.UnixMilli(turn.events[0].Timestamp).UTC()
		info.UpdatedAt = time.UnixMilli(turn.events[len(turn.events)-1].Timestamp).UTC()
		info.FinishedAt = info.UpdatedAt
	}
	if newest {
		info.UpdatedAt = now
		info.FinishedAt = now
	}
	return info
}

// forkBoundary finds the record the child's history ends at: the first turn that
// ends at or after atSeq, or -- when atSeq is absent or past the log -- the last
// turn that ended at all. It is the reference's rule, including both of its
// refusals, because a caller that asked to fork inside an unfinished turn must be
// told that rather than handed the turn before it.
func forkBoundary(log *dshwire.SessionLog, atSeq int64, hasAtSeq bool, sessionID string) (int, *methodError) {
	unavailable := func(message string) *methodError {
		return fail(codeForkUnavailable, message, map[string]any{"sessionId": sessionID})
	}
	if hasAtSeq && atSeq <= log.Cursor() {
		for index, record := range log.Records {
			if record.Type == "turn/end" && record.Seq >= atSeq {
				return index, nil
			}
		}
		return 0, unavailable(fmt.Sprintf("session %q has not completed the turn containing event %d", sessionID, atSeq))
	}
	for index := len(log.Records) - 1; index >= 0; index-- {
		if log.Records[index].Type == "turn/end" {
			return index, nil
		}
	}
	return 0, unavailable(fmt.Sprintf("session %q has no completed turn to fork from", sessionID))
}

// forkSeqArg reads the optional atSeq. It is a console sequence: the number the
// page shows for a record (SessionSeq upstream), not a durable event sequence,
// which is why it is searched in the projected log. A value that is not a number
// is the schema's business; a number that is not a non-negative safe integer is
// the reference's own bad-request, with its own words, because the console's
// SessionSeq would have thrown the same way before the request was sent.
func forkSeqArg(raw json.RawMessage) (int64, bool, *methodError) {
	if len(raw) == 0 {
		return 0, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false, fail(codeArgumentsInvalid, `"atSeq" must be a number`,
			map[string]any{"argument": "atSeq"})
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, fail(codeArgumentsInvalid, `"atSeq" must be a number`,
			map[string]any{"argument": "atSeq"})
	}
	badRequest := func() (int64, bool, *methodError) {
		return 0, true, fail(codeBadRequest, "atSeq must be a non-negative safe integer", nil)
	}
	parsed, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || parsed != float64(int64(parsed)) {
		return badRequest()
	}
	if parsed < 0 || parsed > maxSafeInteger {
		return badRequest()
	}
	return int64(parsed), true, nil
}

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER: the reference's
// SessionSeq accepts nothing larger, because a console sequence above it would
// already have lost precision in the page.
const maxSafeInteger = 1<<53 - 1

// forkChildID mints the child's session id. This host issues run ids and a
// session is its first turn, so the child gets one of those rather than the
// reference's `session-<uuid>`: an id in a second shape would have to be taught
// to every reader of the chain. A candidate that already exists is a collision
// and is retried, never adopted -- adopting would fork onto someone else's
// conversation.
func (h *Handler) forkChildID(ctx context.Context) (string, *methodError) {
	for attempt := 0; attempt < 8; attempt++ {
		candidate := zenforge.NewRunID()
		if !h.runExists(ctx, candidate) {
			return candidate, nil
		}
	}
	return "", fail(codeInternal, "mint a fork session id: every candidate was already taken", nil)
}
