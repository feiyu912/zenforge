package dshstream

import (
	"context"

	"github.com/feiyu912/zenforge"
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
	runIDs := h.sessionRunIDs(ctx, sessionID)
	if len(runIDs) == 0 {
		return sessionID
	}
	return runIDs[len(runIDs)-1]
}

// sessionRunIDs returns the run ids serving a session, in turn order, stopping
// at the first turn that does not exist.
func (h *Handler) sessionRunIDs(ctx context.Context, sessionID string) []string {
	runIDs := make([]string, 0, 2)
	for turn := 1; ; turn++ {
		runID := dshsession.ContinuationRunID(sessionID, turn)
		if !h.runExists(ctx, runID) {
			return runIDs
		}
		runIDs = append(runIDs, runID)
	}
}

// Turns implements dshwire.Source: the runs serving a session's turns, in turn
// order. The session log builder reads a session's turns through this, so the
// stream and the page cannot disagree about what a session is.
func (h *Handler) Turns(ctx context.Context, sessionID string) ([]string, error) {
	return h.sessionRunIDs(ctx, sessionID), nil
}

// Read implements dshwire.Source: one run's durable events from the beginning.
func (h *Handler) Read(ctx context.Context, runID string) ([]zenforge.Event, error) {
	return h.events.Read(ctx, runID, 0, 0)
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
