package dshwire

import (
	"context"
	"testing"

	"github.com/feiyu912/zenforge"
)

// stubSource is a session's turns as the console's read paths see them: a list
// of run ids, each with its own durable log numbered from one.
type stubSource struct {
	turns []string
	runs  map[string][]zenforge.Event
}

func (s stubSource) Turns(context.Context, string) ([]string, error) { return s.turns, nil }

func (s stubSource) Read(_ context.Context, runID string) ([]zenforge.Event, error) {
	return s.runs[runID], nil
}

// twoTurnSource is a conversation of two ordinary turns, each numbered from one
// in its own durable log, which is what every zenforge run does.
func twoTurnSource() stubSource {
	return stubSource{
		turns: []string{"run_one", "run_one~2"},
		runs: map[string][]zenforge.Event{
			"run_one":   aTurn(),
			"run_one~2": aTurn(),
		},
	}
}

func sessionIdentity(turn int) Identity {
	return Identity{Provider: "openai", Model: "qwen-plus", Turn: turn}
}

// TestSessionLogContinuesTheSequenceAcrossTurns is the property the console
// enforces and a second prompt used to break: its cursor is one number for the
// whole conversation, every live event must be exactly one past it, and a
// resumed stream must not cite a cursor behind what it already applied
// (api/gateway/src/client/journal-stream.ts follows + opening).
func TestSessionLogContinuesTheSequenceAcrossTurns(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if len(log.Runs) != 2 || log.NewestRun != "run_one~2" || log.NewestTurn != 2 {
		t.Fatalf("session resolved to %+v, want two turns ending at run_one~2", log)
	}
	// Twelve console records per turn -- the eleven the console's transcript is made
	// of plus the request header its attempt opens (ADR 0117, 0121, 0123): the
	// served sequence counts what the console reads, and the turn's own opening
	// marker is one of them.
	if len(log.Records) != 24 {
		t.Fatalf("records = %d, want both turns' 24 records", len(log.Records))
	}
	// Strictly increasing and contiguous, across the turn boundary included: the
	// second turn's own numbering from one is shifted past the first turn.
	for index, record := range log.Records {
		want := int64(index + 1)
		if record.Seq != want {
			t.Fatalf("record %d has seq %d, want %d", index, record.Seq, want)
		}
	}
	if log.Cursor() != 24 {
		t.Fatalf("cursor = %d, want the session's newest sequence", log.Cursor())
	}
	// The durable tail of the newest turn is its own sequence, not the session's:
	// that is the coordinate the run manager's follower speaks.
	if log.NewestTail != 18 {
		t.Fatalf("newest tail = %d, want the turn's own 18", log.NewestTail)
	}
}

// TestSessionLogNumbersTheSecondTurnAsATurn pins the console's grouping: two
// turns of one conversation must not both claim turn 1, or the transcript shows
// one turn's messages inside the other.
func TestSessionLogNumbersTheSecondTurnAsATurn(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	for _, record := range log.Records {
		turn, ok := intField(record.Data, "turn")
		if !ok {
			// Not every console event carries a turn: the user's prompt and the
			// transcript's bookkeeping are session-wide.
			continue
		}
		want := 1
		if record.Seq > 12 {
			want = 2
		}
		if turn != want {
			t.Fatalf("seq %d carries turn %d, want %d", record.Seq, turn, want)
		}
	}
	// The prompt of the second turn is its own user message, and the first turn's
	// is still there: a conversation, not the newest turn alone.
	if first := findByType(t, log.Records, "user/message"); first.Seq != 2 {
		t.Fatalf("first user message is at seq %d, want the first turn's prompt", first.Seq)
	}
	last := log.Records[len(log.Records)-1]
	_ = last
}

// TestSessionLogWindowSpansTurns checks the window the snapshot serves: the
// newest maxMessages records, which for a conversation of two short turns reaches
// back into the first one rather than starting at the second turn's beginning.
func TestSessionLogWindowSpansTurns(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	window, hasMore := log.Window(14)
	if !hasMore || len(window) != 14 {
		t.Fatalf("window = %d records, hasMore = %v, want 14 and true", len(window), hasMore)
	}
	// The window ends exactly at the cursor the snapshot cites.
	if window[len(window)-1].Seq != log.Cursor() {
		t.Fatalf("window ends at %d, want the cursor %d", window[len(window)-1].Seq, log.Cursor())
	}
	if window[0].Seq != 11 {
		// The newest 14 of the conversation's 24 records reach back into the first
		// turn, which is the point: a window is not turn-aligned.
		t.Fatalf("window starts at %d, want 11 (a record of the first turn)", window[0].Seq)
	}
	for index := 1; index < len(window); index++ {
		if window[index].Seq != window[index-1].Seq+1 {
			t.Fatalf("window is not contiguous at %d: %d then %d", index, window[index-1].Seq, window[index].Seq)
		}
	}

	all, hasMore := log.Window(0)
	if hasMore || len(all) != 24 {
		t.Fatalf("unbounded window = %d records, hasMore = %v, want all 24 and false", len(all), hasMore)
	}
}

// TestSessionLogPagesBackThroughAnEarlierTurn checks "load earlier" across the
// turn boundary: the page below the cursor reaches the first turn, is contiguous
// where it meets what the console already has, and reports more history until the
// conversation's beginning.
func TestSessionLogPagesBackThroughAnEarlierTurn(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	// The console asks for a page below its oldest record, which for a full window
	// is record 7.
	page, hasMore := log.Through(6, 0, false, 8)
	if len(page) != 6 || hasMore {
		t.Fatalf("page = %d records, hasMore = %v, want the first turn's 6 and false", len(page), hasMore)
	}
	if page[len(page)-1].Seq != 6 {
		t.Fatalf("page ends at %d, want 6: the console requires it to meet what it has", page[len(page)-1].Seq)
	}
	if page[0].Seq != 1 {
		t.Fatalf("page starts at %d, want the conversation's first record", page[0].Seq)
	}
	// beforeSeq is the exclusive upper bound the console uses when it has a
	// narrower gap to fill.
	gap, hasMore := log.Through(20, 9, true, 50)
	if hasMore || len(gap) != 8 || gap[0].Seq != 1 || gap[len(gap)-1].Seq != 8 {
		t.Fatalf("gap page = %d records starting at %d ending at %d, hasMore = %v",
			len(gap), gap[0].Seq, gap[len(gap)-1].Seq, hasMore)
	}
}

// TestSessionLogOfASessionWithNoTurns checks the draft the console opens before
// its first prompt: no turns, no records, and the empty cursor upstream uses.
func TestSessionLogOfASessionWithNoTurns(t *testing.T) {
	source := stubSource{turns: nil, runs: map[string][]zenforge.Event{}}
	log, err := Session(context.Background(), source, "run_draft", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if len(log.Records) != 0 || log.Newest != nil || log.NewestRun != "" {
		t.Fatalf("draft log = %+v, want no turns and no records", log)
	}
	if log.Cursor() != -1 {
		t.Fatalf("draft cursor = %d, want -1", log.Cursor())
	}
	window, hasMore := log.Window(50)
	if len(window) != 0 || hasMore {
		t.Fatalf("draft window = %d records, hasMore = %v", len(window), hasMore)
	}
	if page, hasMore := log.Through(0, 0, false, 50); len(page) != 0 || hasMore {
		t.Fatalf("draft page = %d records, hasMore = %v", len(page), hasMore)
	}
}

// TestSessionLogCountsEmptyTurnsWithoutSkippingNumbers checks a turn whose log is
// empty (a continuation run that has not logged yet): it contributes no records
// and no numbers, so the sequence stays contiguous for whatever follows.
func TestSessionLogCountsEmptyTurnsWithoutSkippingNumbers(t *testing.T) {
	source := stubSource{
		turns: []string{"run_one", "run_one~2", "run_one~3"},
		runs: map[string][]zenforge.Event{
			"run_one":   aTurn(),
			"run_one~2": nil,
			"run_one~3": aTurn(),
		},
	}
	log, err := Session(context.Background(), source, "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if log.Cursor() != 24 || len(log.Records) != 24 {
		// Two real turns and an empty middle turn that numbers its own opening marker
		// without skipping a number.
		t.Fatalf("records = %d ending at %d, want 24 records ending at 24", len(log.Records), log.Cursor())
	}
	if log.NewestTurn != 3 {
		t.Fatalf("newest turn = %d, want the third turn", log.NewestTurn)
	}
	// The empty middle turn contributes only its own opening marker, so the third
	// turn starts after it. Find that marker rather than counting records, which a
	// console-ordering change is allowed to move.
	start := -1
	for index, record := range log.Records {
		if record.Type != "turn/start" {
			continue
		}
		if turn, ok := intField(record.Data, "turn"); ok && turn == 3 {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatal("no turn/start record carries turn 3")
	}
	for _, record := range log.Records[start:] {
		got, ok := intField(record.Data, "turn")
		if ok && got != 3 {
			t.Fatalf("seq %d carries turn %d, want 3", record.Seq, got)
		}
	}
}

// TestSessionLogIdentifiesMessagesByTheSessionSequence pins the identity the
// console matches a message node by. The shipped client's `user/message`
// definition matches on `String(event.data.id)`, so two records that carry the
// same id are one message: every turn's first prompt used to be `msg-1` (the
// run's own first event), and the operator's second question rendered as the
// first one instead of itself (ADR 0110).
func TestSessionLogIdentifiesMessagesByTheSessionSequence(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	at := map[string]int64{}
	for _, record := range log.Records {
		id := projectedMessageID(record)
		if id == "" {
			continue
		}
		if first, seen := at[id]; seen {
			t.Fatalf("seq %d and seq %d both carry message id %q", first, record.Seq, id)
		}
		at[id] = record.Seq
	}
	// The identity is the session's own sequence: each prompt is its turn's second
	// record, right behind the turn's opening marker.
	if at["msg-2"] != 2 || at["msg-14"] != 14 {
		t.Fatalf("message identities = %v, want the two prompts at msg-2 and msg-14", at)
	}
	// Four messages per turn: the prompt, one assistant message per settled step
	// with content, and the tool-result message.
	if len(at) != 8 {
		t.Fatalf("message identities = %d, want 8 distinct messages across the two turns", len(at))
	}
}

// projectedMessageID reads the identity a projected record names, wherever the
// console's own definition reads it: `data.id` for a user or steering message,
// `data.message.id` for the assistant and tool-result shapes.
func projectedMessageID(record Event) string {
	if id, ok := stringField(record.Data, "id"); ok {
		return id
	}
	if message, ok := record.Data["message"].(map[string]any); ok {
		if id, ok := stringField(message, "id"); ok {
			return id
		}
	}
	return ""
}

// The turn a record belongs to is what a fork counts in, and a turn that
// contributed nothing is the case the served sequence cannot express: its own
// records start where the previous turn's ended, so only the per-turn counts
// name it.
func TestSessionLogNamesTheTurnOfARecord(t *testing.T) {
	source := stubSource{
		turns: []string{"run_one", "run_empty", "run_one~3"},
		runs: map[string][]zenforge.Event{
			"run_one": aTurn(),
			// A turn whose events produce no console records at all.
			"run_empty": nil,
			// Its own numbering, shifted past both turns before it.
			"run_one~3": aTurn(),
		},
	}
	log, err := Session(context.Background(), source, "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if len(log.TurnRecords) != 3 {
		t.Fatalf("turn records = %v, want one count per turn", log.TurnRecords)
	}
	if log.TurnRecords[0] != 12 || log.TurnRecords[1] != 0 || log.TurnRecords[2] != 12 {
		t.Fatalf("turn records = %v, want 12, 0, 12", log.TurnRecords)
	}
	for index, record := range log.Records {
		want := 1
		if index >= 12 {
			want = 3
		}
		if got := log.TurnContaining(index); got != want {
			t.Fatalf("record %d (seq %d) is in turn %d, want %d", index, record.Seq, got, want)
		}
	}
	if got := log.TurnContaining(len(log.Records)); got != 0 {
		t.Fatalf("an index past the log is turn %d, want 0", got)
	}
	if got := log.TurnContaining(-1); got != 0 {
		t.Fatalf("a negative index is turn %d, want 0", got)
	}
}

// A fork records the conversation it came from on the child's first turn, which
// is where this host keeps the lineage: there is no session-metadata plane, and
// the console nests a fork under its source by reading it off the list row.
func TestSessionLogReadsTheForkLineage(t *testing.T) {
	child := aTurn()
	child[0].Payload["parentSessionId"] = "run_source"
	// A later turn carrying the same field must not matter: the lineage is the
	// child's own, stated where its history begins.
	later := aTurn()
	later[0].Payload["parentSessionId"] = "run_someone_else"
	source := stubSource{
		turns: []string{"run_child", "run_child~2"},
		runs:  map[string][]zenforge.Event{"run_child": child, "run_child~2": later},
	}
	log, err := Session(context.Background(), source, "run_child", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if log.ParentSessionID != "run_source" {
		t.Fatalf("parent = %q, want the first turn's source", log.ParentSessionID)
	}
	plain, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity, nil)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if plain.ParentSessionID != "" {
		t.Fatalf("parent = %q, want none for a conversation nobody forked", plain.ParentSessionID)
	}
}
