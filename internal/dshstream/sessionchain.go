package dshstream

import (
	"context"

	"github.com/feiyu912/zenforge/internal/dshsession"
)

// A console session outlives its runs (internal/dshsession), so following a
// session means following the run that serves its newest turn. Resolving it
// here matters: a follow stream opened after a second prompt would otherwise
// attach to the first, finished run -- the stream would open, show history, and
// then never deliver the answer the operator is waiting for.

// currentRun resolves a session id to the run serving its newest turn. The
// chain is probed forward and stops at the first turn that does not exist, so a
// missing link (a run whose log was pruned) ends the resolution rather than
// skipping past it into an unrelated run.
func (h *Handler) currentRun(ctx context.Context, sessionID string) string {
	runID := sessionID
	for turn := 2; ; turn++ {
		next := dshsession.ContinuationRunID(sessionID, turn)
		if !h.runExists(ctx, next) {
			return runID
		}
		runID = next
	}
}

// runExists reports whether a run id names a run this host knows: tracked by the
// manager, or with a durable log the manager no longer tracks. Both are real
// turns of a conversation; a finished run's history must still be followed.
func (h *Handler) runExists(ctx context.Context, runID string) bool {
	if _, err := h.manager.Get(runID); err == nil {
		return true
	}
	latest, err := h.events.LatestSeq(ctx, runID)
	return err == nil && latest > 0
}
