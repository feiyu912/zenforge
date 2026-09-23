package dshwire

import (
	"context"

	"github.com/feiyu912/zenforge"
)

// This file serves a console session's log, which is more than one run's.
//
// A console session is a conversation: the operator keeps prompting it, and each
// prompt after the first is a new zenforge run (dshsession.ContinuationRunID) so
// a finished turn can be continued. Each run numbers its own durable events from
// one, and the console's cursor is that number. Serving a later turn's log with
// its own numbering therefore moves the cursor *backwards*, which the console
// refuses:
//
//	Failed to load history: session event stream resumed at a cursor behind the
//	last applied entry (gateway/internal)
//
// The invariant is not cosmetic. The shipped client keeps one last-applied cursor
// for the whole conversation, requires every live event to be exactly one past
// it, requires a snapshot's last record to end exactly at the snapshot's cursor,
// and requires a resumed generation's cursor to be at or ahead of what it already
// applied (api/gateway/src/client/journal-stream.ts assertPageThrough +
// opening + follows). A conversation whose second turn renumbers from one breaks
// all three at once, and its earlier turn is unreachable as well.
//
// So a session's served log is the concatenation of its turns in one sequence:
// turn k's wire sequence is its durable sequence shifted past every event of the
// turns before it. The shift is derived, not stored -- a turn's event count is
// fixed once that turn has ended, and a later turn only starts after the previous
// one reached a terminal event -- so the same session always serves the same
// numbers, and a restart or another process rebuilds them identically.

// Source is what a session log needs from a host: the runs serving a session's
// turns, in turn order, and their durable logs. dshapi and dshstream each
// implement it over the same manager and event store, so the RPC surface and the
// stream cannot disagree about which runs a session is or how they are numbered.
type Source interface {
	// Turns returns the run ids serving a session, in turn order. A session whose
	// first turn has not started has no turns, which is not an error: that is the
	// draft the console opens before its first prompt.
	Turns(ctx context.Context, sessionID string) ([]string, error)
	// Read returns one run's durable events from the beginning.
	Read(ctx context.Context, runID string) ([]zenforge.Event, error)
}

// SessionLog is the served view of a console session: every turn's projected
// records, in one session-wide sequence, plus the newest turn's own projection so
// a live follow can continue it.
type SessionLog struct {
	// SessionID is the session the log belongs to.
	SessionID string
	// Runs are the runs serving the session's turns, in turn order. The last is
	// the turn a follow stream tails.
	Runs []string
	// Records are every turn's wire events, in session sequence: turn k's records
	// carry SeqOffset(k) added to their durable sequence, so the sequence is
	// strictly increasing across turns and contiguous within them.
	Records []Event
	// ParentSessionID is the conversation this one was forked from, when it was
	// forked at all. It is read from the *first* turn's opening event, because a
	// fork is a copy of another conversation's turns and the copy is where the
	// lineage lives -- this host has no session-metadata plane, and the console
	// reads the link from the list row to nest a fork under its source
	// (api-session-controller: flattenLineage). Empty for a conversation nobody
	// forked.
	ParentSessionID string
	// TurnRecords is how many records each turn contributed, parallel to Runs:
	// turn k's records are Records[sum(TurnRecords[:k-1])] through the next
	// TurnRecords[k-1] entries. The served sequence alone cannot name the turn a
	// record belongs to, because a turn that contributed no records repeats the
	// continuation point of the one before it -- its own records start where the
	// previous turn's ended either way (ADR 0117) -- so a caller that has to map a
	// console sequence back to a turn (session/fork's boundary rule) reads it
	// here instead of re-deriving the offsets.
	TurnRecords []int
	// Newest is the newest turn's projection, continued by a live tail. It is nil
	// when the session has no turns yet.
	Newest *Projection
	// NewestRun is the run id of the newest turn, empty when there is none.
	NewestRun string
	// NewestTail is the newest turn's own durable tail sequence (not the session
	// sequence), which is where a follower attaches.
	NewestTail int64
	// NewestTurn is the newest turn's console turn number, one-based.
	NewestTurn int
	// NewestEvents is the newest turn's durable log, from its beginning. It is
	// what a reader replays to reconstruct state the projection does not keep --
	// an assistant attempt that is still streaming, which a reconnecting console
	// must be handed as its baseline rather than discovering at the settlement
	// (ADR 0118). It is nil when the session has no turns.
	NewestEvents []zenforge.Event
	// NewestIdentity is the stamp the newest turn's projection carries: its turn
	// number and the sequence offset its events are shifted by. A follower that
	// moves on to the conversation's next turn stamps it with this value, so the
	// offset stays derived from the durable shape instead of being reinvented at
	// the attach point (ADR 0108).
	NewestIdentity Identity
}

// Title is the conversation's title and the served sequence that set it, from the
// newest title record in the log. It is empty for a conversation nobody named.
//
// The console reads the title from the `title` projection, not from a field of
// session/list: a list row seeds its projection store with this value and the
// header folds it from the same key (api-session-controller/src/client/sessions/
// manager.ts reads s.projections.values, :804-812 derives the row title). Serving
// the durable record alone is not enough, and a run-scoped title lookup misses a
// rename that landed on an earlier turn.
func (l *SessionLog) Title() (string, int64) {
	title, seq := "", int64(0)
	for _, record := range l.Records {
		if record.Type != "session/title" {
			continue
		}
		value, ok := record.Data["title"].(string)
		if !ok || value == "" {
			continue
		}
		title, seq = value, record.Seq
	}
	return title, seq
}

// Session builds a session's log by projecting each of its turns and shifting it
// into the session sequence. Identity reports the provenance and the turn number
// for one turn -- the provider and model are the host's answer for every turn,
// while turn and sequence offset are the session's own coordinates.
//
// A session with no turns yet yields an empty log with no error; callers decide
// whether that is a draft (the console's new chat) or a session this host does
// not serve.
func Session(ctx context.Context, source Source, sessionID string, identity func(turn int) Identity, inputs Inputs) (*SessionLog, error) {
	runs, err := source.Turns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	log := &SessionLog{SessionID: sessionID, Runs: runs}
	offset := int64(0)
	for index, runID := range runs {
		events, err := source.Read(ctx, runID)
		if err != nil {
			return nil, err
		}
		turn := index + 1
		stamp := identity(turn)
		stamp.Turn = turn
		stamp.SeqOffset = offset
		if index == 0 {
			for _, event := range events {
				if parent, ok := event.Payload["parentSessionId"].(string); ok && parent != "" {
					log.ParentSessionID = parent
					break
				}
			}
		}
		// Each turn's opening message carries that turn's own attachments, which
		// is why the provider is asked by run id rather than once per session.
		var runInputs []Attachment
		if inputs != nil {
			runInputs = inputs(runID)
		}
		projection := Project(events, stamp, func(string) []Attachment { return runInputs })
		log.Records = append(log.Records, projection.Events...)
		log.TurnRecords = append(log.TurnRecords, len(projection.Events))
		// The next turn starts where this turn's records end, whether or not it
		// produced any: the served sequence counts the console's records, so a
		// turn that was nothing but bookkeeping contributes no numbers to skip
		// (ADR 0117).
		offset += int64(len(projection.Events))
		if index == len(runs)-1 {
			log.Newest = projection
			log.NewestRun = runID
			log.NewestTurn = turn
			log.NewestEvents = events
			log.NewestIdentity = stamp
			if len(events) > 0 {
				log.NewestTail = events[len(events)-1].Seq
			}
		}
	}
	return log, nil
}

// TurnContaining returns the one-based turn whose records contain Records[index],
// or zero when the index is outside the log. It is the inverse of the served
// sequence: a console sequence identifies a record, and a turn is what a caller
// inheriting whole turns has to count in.
func (l *SessionLog) TurnContaining(index int) int {
	if index < 0 || index >= len(l.Records) {
		return 0
	}
	seen := 0
	for turn, count := range l.TurnRecords {
		seen += count
		if index < seen {
			return turn + 1
		}
	}
	return 0
}

// Cursor is the session's newest sequence number, or -1 for a session with no
// records. It is what a snapshot cites and what the console's cursor becomes: the
// value the next event must be one past.
func (l *SessionLog) Cursor() int64 {
	if len(l.Records) == 0 {
		return -1
	}
	return l.Records[len(l.Records)-1].Seq
}

// Window returns the newest maxMessages records, or all of them when maxMessages
// is not positive, plus whether older records were left out. The window can span
// turns: the operator scrolling back through a conversation expects the earlier
// turn, not the current turn's beginning.
func (l *SessionLog) Window(maxMessages int) ([]Event, bool) {
	if maxMessages <= 0 || len(l.Records) <= maxMessages {
		return l.Records, false
	}
	return l.Records[len(l.Records)-maxMessages:], true
}

// Through returns the records of one page: the newest maxMessages records below
// the inclusive cursor, honoring beforeSeq as an exclusive upper bound when it is
// lower. It is the selection session/page answers with, and it exists here so the
// page and the snapshot agree on what a window is.
//
// hasMore reports whether any record precedes the page, which is what the
// console's "load earlier" reads.
func (l *SessionLog) Through(cursor int64, beforeSeq int64, hasBefore bool, maxMessages int) ([]Event, bool) {
	if cursor < 0 || cursor > l.Cursor() {
		cursor = l.Cursor()
	}
	upper := cursor + 1
	if hasBefore && beforeSeq < upper {
		upper = beforeSeq
	}
	if maxMessages <= 0 {
		maxMessages = len(l.Records)
	}
	selected := make([]Event, 0, maxMessages)
	for index := len(l.Records) - 1; index >= 0; index-- {
		record := l.Records[index]
		if record.Seq >= upper {
			continue
		}
		if len(selected) >= maxMessages {
			break
		}
		selected = append(selected, record)
	}
	hasMore := false
	if len(selected) > 0 {
		hasMore = selected[len(selected)-1].Seq > l.Records[0].Seq
	}
	// selected is newest-first; the page is served oldest-first.
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected, hasMore
}
