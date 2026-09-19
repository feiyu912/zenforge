package dshapi

import (
	"context"
	"errors"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshsession"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// This file makes a console session outlive its first run. The console names a
// session and keeps prompting it; without this, a prompt to a finished session
// answered `unimplemented` and the conversation ended with the run.
//
// A session's later turns are found by probing the deterministic run-id chain
// internal/dshsession defines, so nothing has to be remembered in memory: a
// restart, or another process owning the next turn, loses no part of the
// conversation. Turn 1 is the session id itself, which keeps every existing
// session (and every existing test) exactly as it was.

// sessionRunIDs returns the run ids serving a session, in turn order. It probes
// forward and stops at the first turn that does not exist: the chain is written
// in order, so a missing link means the conversation ended there, and probing
// past it could pick up an unrelated run whose id merely looks like a
// continuation.
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

// runExists reports whether a run id names a run this host knows: live in the
// manager, or with a durable log the manager no longer tracks.
func (h *Handler) runExists(ctx context.Context, runID string) bool {
	if _, err := h.manager.Get(runID); err == nil {
		return true
	} else if !errors.Is(err, harnesshttp.ErrRunNotFound) {
		// A manager error that is not "not found" is not evidence of absence;
		// fall through to the durable log, which is the authority on a finished
		// run anyway.
		_ = err
	}
	latest, err := h.events.LatestSeq(ctx, runID)
	return err == nil && latest > 0
}

// resolveSession returns the session a prompt names, given the run ids that are
// known to exist. A prompt may name a continuation run id -- an older listing,
// or a client that followed one -- and that run still belongs to its session, so
// the chain is walked back to its first turn. A run id whose base is absent is
// left as its own session, so an adopted id that merely looks like a
// continuation is never merged into someone else's conversation.
func (h *Handler) resolveSession(ctx context.Context, runID string) string {
	sessionID, turn := dshsession.Base(runID)
	if turn <= 1 {
		return runID
	}
	if !h.runExists(ctx, sessionID) {
		return runID
	}
	return sessionID
}

// currentSessionRun returns the run serving a session's newest turn, with its
// manager info when the run is still tracked. The second result reports whether
// any turn exists at all.
func (h *Handler) currentSessionRun(ctx context.Context, sessionID string) (string, harnesshttp.RunInfo, bool) {
	runIDs := h.sessionRunIDs(ctx, sessionID)
	if len(runIDs) == 0 {
		return "", harnesshttp.RunInfo{}, false
	}
	current := runIDs[len(runIDs)-1]
	info, err := h.manager.Get(current)
	if err != nil {
		return current, harnesshttp.RunInfo{}, true
	}
	return current, info, true
}

// conversationMessages rebuilds the exchange a session has already had, in
// order, so a continuation run starts with the conversation rather than only the
// newest line.
//
// It reads what the durable log actually records: the user's turn is the
// `run.started` input and the assistant's reply is the `run.done` output. That
// is the conversation at run granularity -- the intermediate tool traffic a run
// performed is not replayed, because those turns are a record of one execution,
// not something to hand a new one. An empty output (a run that failed, or one
// still going) contributes no assistant message rather than an empty one.
func (h *Handler) conversationMessages(ctx context.Context, runIDs []string) []model.Message {
	messages := make([]model.Message, 0, len(runIDs)*2)
	for _, runID := range runIDs {
		events, err := h.events.Read(ctx, runID, 0, 0)
		if err != nil {
			// A missing log is a turn this host cannot reconstruct. Skipping it
			// keeps the remaining turns in order; inventing a message for it
			// would put words in the operator's mouth.
			continue
		}
		input := ""
		output := ""
		for _, event := range events {
			switch event.Type {
			case zenforge.EventRunStarted:
				if value, ok := event.Payload["input"].(string); ok && value != "" {
					input = value
				}
			case zenforge.EventRunDone:
				if value, ok := event.Payload["output"].(string); ok && value != "" {
					output = value
				}
			}
		}
		if input != "" {
			messages = append(messages, model.Message{Role: "user", Content: input})
		}
		if output != "" {
			messages = append(messages, model.Message{Role: "assistant", Content: output})
		}
	}
	return messages
}
