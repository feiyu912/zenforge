package dshsession

import "testing"

func TestContinuationRunIDAndBaseRoundTrip(t *testing.T) {
	sessionID := "run_0123456789abcdef"
	for turn := 1; turn <= 4; turn++ {
		runID := ContinuationRunID(sessionID, turn)
		gotSession, gotTurn := Base(runID)
		if gotSession != sessionID || gotTurn != turn {
			t.Fatalf("turn %d: runID %q split into (%q, %d)", turn, runID, gotSession, gotTurn)
		}
	}
	if ContinuationRunID(sessionID, 1) != sessionID {
		t.Fatalf("turn 1 = %q, want the session id itself", ContinuationRunID(sessionID, 1))
	}
}

func TestBaseLeavesForeignRunIDsAlone(t *testing.T) {
	cases := []string{
		"run_0123456789abcdef",
		"~2",                           // leading separator: nothing to continue
		"session~",                     // trailing separator: no turn number
		"session~1",                    // the first turn is the session, never a suffix
		"session~0",                    // turn numbers start at two
		"session~-3",                   // a sign is not a turn number this package issues
		"session~two",                  // not a number
		"session~02",                   // Atoi would accept it; fmt.Sprintf never wrote it
		"session~99999999999999999999", // out of range
	}
	for _, runID := range cases {
		sessionID, turn := Base(runID)
		if sessionID != runID || turn != 1 {
			t.Fatalf("Base(%q) = (%q, %d), want the id to keep its own identity", runID, sessionID, turn)
		}
	}
}

func TestNextTurnAdvancesPastTheHighestSeen(t *testing.T) {
	sessionID := "run_abc"
	if got := NextTurn(nil); got != 2 {
		t.Fatalf("NextTurn(nil) = %d, want 2", got)
	}
	if got := NextTurn([]string{sessionID}); got != 2 {
		t.Fatalf("NextTurn(first turn only) = %d, want 2", got)
	}
	if got := NextTurn([]string{sessionID, ContinuationRunID(sessionID, 2)}); got != 3 {
		t.Fatalf("NextTurn(two turns) = %d, want 3", got)
	}
	// A pruned link must not make the chain reuse an id: turn 4 follows 3 even
	// when 2 is missing.
	if got := NextTurn([]string{sessionID, ContinuationRunID(sessionID, 3)}); got != 4 {
		t.Fatalf("NextTurn(gap at 2) = %d, want 4", got)
	}
}

func TestRecognisesBaseRequiresTheBaseToExist(t *testing.T) {
	sessionID := "run_abc"
	known := map[string]struct{}{sessionID: {}}
	if !RecognisesBase(ContinuationRunID(sessionID, 2), known) {
		t.Fatal("a continuation of a known run was not recognised")
	}
	if RecognisesBase(ContinuationRunID(sessionID, 2), map[string]struct{}{}) {
		t.Fatal("a continuation was recognised without its base present")
	}
	if RecognisesBase(sessionID, known) {
		t.Fatal("a first-turn run id was treated as a continuation")
	}
}
