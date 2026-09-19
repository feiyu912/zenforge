// Package dshsession names the runs that serve one console session.
//
// A session is a conversation. A run is one execution of the harness. The
// console names a session and expects to keep prompting it, so a session that
// outlives its first run needs its later turns to be findable from the session
// id alone -- including after a restart, when no in-memory table survives and a
// second process may own the next turn.
//
// The convention is a deterministic chain: the first turn's run id is the
// session id, and the k-th turn's run id is ContinuationRunID(sessionID, k) for
// k >= 2. Existence is decided by the callers probing the run manager and the
// durable log, so nothing has to be stored to keep a conversation together and
// no shared table has to be threaded through the console's packages.
//
// The separator is "~" rather than "#": a run id can reach a URL path, where
// "#" starts a fragment, and "~" is reserved by neither RFC 3986 nor a file
// system. The chain is a naming convention, not a claim about ids this host did
// not issue, so a caller that sees a run id it cannot verify as a continuation
// must treat it as its own session (see RecognisesBase).
package dshsession

import (
	"fmt"
	"strconv"
	"strings"
)

// continuationSeparator joins a session id to a turn number.
const continuationSeparator = "~"

// ContinuationRunID returns the run id serving turn number turn of sessionID.
// Turn 1 is the session id itself, which is what makes the first turn's run
// indistinguishable from the session and keeps the common case unchanged.
func ContinuationRunID(sessionID string, turn int) string {
	if turn <= 1 || sessionID == "" {
		return sessionID
	}
	return fmt.Sprintf("%s%s%d", sessionID, continuationSeparator, turn)
}

// Base splits a run id into the session it serves and its turn number. A run id
// that is not a continuation is its own session's first turn, which is exactly
// what a run this host did not continue looks like.
//
// A leading separator (or an empty session part) is not a continuation either:
// the convention only ever appends, so an id that begins with the separator was
// issued by someone else and keeps its own name.
func Base(runID string) (sessionID string, turn int) {
	index := strings.LastIndex(runID, continuationSeparator)
	if index <= 0 || index == len(runID)-1 {
		return runID, 1
	}
	digits := runID[index+1:]
	parsed, err := strconv.Atoi(digits)
	// strconv.Atoi accepts a leading sign and whitespace that fmt.Sprintf never
	// produced, so the digits are required to round-trip before the suffix is
	// trusted as a turn number.
	if err != nil || parsed < 2 || strconv.Itoa(parsed) != digits {
		return runID, 1
	}
	return runID[:index], parsed
}

// NextTurn returns the turn number for a session whose runs are the given ids,
// in order. It is the next turn after the highest one seen, so a chain that is
// missing a link (a run whose log was pruned) still continues forward instead of
// reusing an id a log may already own.
func NextTurn(runIDs []string) int {
	highest := 1
	for _, runID := range runIDs {
		if _, turn := Base(runID); turn > highest {
			highest = turn
		}
	}
	return highest + 1
}

// RecognisesBase reports whether runID can be treated as a continuation of a
// session, given which run ids are known to exist. A continuation is only
// recognised when the run it continues is itself present: an adopted session id
// that merely looks like a continuation keeps its own identity rather than being
// merged into a conversation it never belonged to.
func RecognisesBase(runID string, known map[string]struct{}) bool {
	sessionID, turn := Base(runID)
	if turn <= 1 {
		return false
	}
	_, ok := known[sessionID]
	return ok
}
