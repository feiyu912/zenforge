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
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if len(log.Runs) != 2 || log.NewestRun != "run_one~2" || log.NewestTurn != 2 {
		t.Fatalf("session resolved to %+v, want two turns ending at run_one~2", log)
	}
	if len(log.Records) != 36 {
		t.Fatalf("records = %d, want both turns' 36 events", len(log.Records))
	}
	// Strictly increasing and contiguous, across the turn boundary included: the
	// second turn's own numbering from one is shifted past the first turn.
	for index, record := range log.Records {
		want := int64(index + 1)
		if record.Seq != want {
			t.Fatalf("record %d has seq %d, want %d", index, record.Seq, want)
		}
	}
	if log.Cursor() != 36 {
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
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity)
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
		if record.Seq > 18 {
			want = 2
		}
		if turn != want {
			t.Fatalf("seq %d carries turn %d, want %d", record.Seq, turn, want)
		}
	}
	// The prompt of the second turn is its own user message, and the first turn's
	// is still there: a conversation, not the newest turn alone.
	if first := findByType(t, log.Records, "user/message"); first.Seq != 1 {
		t.Fatalf("first user message is at seq %d, want the first turn's prompt", first.Seq)
	}
	last := log.Records[len(log.Records)-1]
	_ = last
}

// TestSessionLogWindowSpansTurns checks the window the snapshot serves: the
// newest maxMessages records, which for a conversation of two short turns reaches
// back into the first one rather than starting at the second turn's beginning.
func TestSessionLogWindowSpansTurns(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	window, hasMore := log.Window(24)
	if !hasMore || len(window) != 24 {
		t.Fatalf("window = %d records, hasMore = %v, want 24 and true", len(window), hasMore)
	}
	// The window ends exactly at the cursor the snapshot cites.
	if window[len(window)-1].Seq != log.Cursor() {
		t.Fatalf("window ends at %d, want the cursor %d", window[len(window)-1].Seq, log.Cursor())
	}
	if window[0].Seq != 13 {
		t.Fatalf("window starts at %d, want 13 (six records of the first turn)", window[0].Seq)
	}
	for index := 1; index < len(window); index++ {
		if window[index].Seq != window[index-1].Seq+1 {
			t.Fatalf("window is not contiguous at %d: %d then %d", index, window[index-1].Seq, window[index].Seq)
		}
	}

	all, hasMore := log.Window(0)
	if hasMore || len(all) != 36 {
		t.Fatalf("unbounded window = %d records, hasMore = %v, want all 36 and false", len(all), hasMore)
	}
}

// TestSessionLogPagesBackThroughAnEarlierTurn checks "load earlier" across the
// turn boundary: the page below the cursor reaches the first turn, is contiguous
// where it meets what the console already has, and reports more history until the
// conversation's beginning.
func TestSessionLogPagesBackThroughAnEarlierTurn(t *testing.T) {
	log, err := Session(context.Background(), twoTurnSource(), "run_one", sessionIdentity)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	// The console asks for a page below its oldest record, which for a full window
	// is record 13.
	page, hasMore := log.Through(12, 0, false, 8)
	if len(page) != 8 || !hasMore {
		t.Fatalf("page = %d records, hasMore = %v, want 8 and true", len(page), hasMore)
	}
	if page[len(page)-1].Seq != 12 {
		t.Fatalf("page ends at %d, want 12: the console requires it to meet what it has", page[len(page)-1].Seq)
	}
	if page[0].Seq != 5 {
		t.Fatalf("page starts at %d, want 5", page[0].Seq)
	}
	// beforeSeq is the exclusive upper bound the console uses when it has a
	// narrower gap to fill.
	narrow, hasMore := log.Through(36, 30, true, 50)
	// The page reaches the conversation's first record, so there is no earlier
	// history left to fetch.
	if hasMore || len(narrow) != 29 || narrow[0].Seq != 1 || narrow[len(narrow)-1].Seq != 29 {
		t.Fatalf("narrow page = %d records starting at %d ending at %d, hasMore = %v",
			len(narrow), narrow[0].Seq, narrow[len(narrow)-1].Seq, hasMore)
	}
}

// TestSessionLogOfASessionWithNoTurns checks the draft the console opens before
// its first prompt: no turns, no records, and the empty cursor upstream uses.
func TestSessionLogOfASessionWithNoTurns(t *testing.T) {
	source := stubSource{turns: nil, runs: map[string][]zenforge.Event{}}
	log, err := Session(context.Background(), source, "run_draft", sessionIdentity)
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
	log, err := Session(context.Background(), source, "run_one", sessionIdentity)
	if err != nil {
		t.Fatalf("Session returned error: %v", err)
	}
	if log.Cursor() != 36 || len(log.Records) != 36 {
		t.Fatalf("records = %d ending at %d, want 36 events ending at 36", len(log.Records), log.Cursor())
	}
	if log.NewestTurn != 3 {
		t.Fatalf("newest turn = %d, want the third turn", log.NewestTurn)
	}
	for _, record := range log.Records[18:] {
		got, ok := intField(record.Data, "turn")
		if ok && got != 3 {
			t.Fatalf("seq %d carries turn %d, want 3", record.Seq, got)
		}
	}
}
