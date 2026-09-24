package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/model"
)

// POST /api/session/selectModel: the console's model picker submits the complete
// provider/model selection here (client/ui-model-selection/src/client/directory.ts:87-99),
// and the picker reads the effective selection back from the session's durable
// projection (`modelSelection`), not from this response: the response says the
// selection was accepted, and the projection is what the composer renders
// (directory.ts:144-172: `current = projected.next ?? catalog.default`).
//
// Upstream types: SessionSelectModelRequest is ModelSelection plus sessionId
// (session-controller/lib/types/types.d.ts:270-276), and SessionSelectModelValue is
// `{selected: ModelSelection}`.
//
// This host builds one adapter per session and hands it to that session's run
// instead of installing it as the host's adapter (ADR 0140). A run's model is
// therefore its own: two sessions running concurrently under different
// selections keep the adapters their own selections resolved to, and a
// selection made later can no longer change a run that is already answering.

// ModelRoute is one session's run model: the provider and model its operator
// chose, together with the adapter this host built for that pair. The pair is
// what a resume or a fork resolves again; the adapter is what a run that is
// starting now uses.
type ModelRoute struct {
	Provider string
	Model    string
	Adapter  model.Model
}

// ModelSelectionStore is where a session's chosen provider and model lives, and
// what makes the choice real: the route it reports is what the session's next run
// is started on. It is injected like the other seams, because only the serve
// command knows how to build an adapter and what it can route to.
type ModelSelectionStore interface {
	// SelectModel validates a selection against what this host can route to and
	// records it for the session, returning what it resolved.
	SelectModel(sessionID string, selection ModelSelection) (ModelSelection, error)
	// ModelRoute builds and reports the route this session's next run must use.
	// ok is false for a session with no recorded choice, which runs on the host's
	// configured adapter. An error is a recorded choice this host can no longer
	// resolve, which refuses the prompt rather than running it on another model.
	// The read has no side effect, so a projection asking what a session runs on
	// consumes nothing.
	ModelRoute(sessionID string) (ModelRoute, bool, error)
	// MarkModelUsed records that a run actually started on the session's route.
	// It is what the console's "last used" hint reports, and it is separate from
	// ModelRoute because a read must not consume: a projection asking what a
	// session runs on would otherwise report a model no run has used.
	MarkModelUsed(sessionID string)
}

// sessionRegistrar is the optional half of the model-selection store a host
// implements when it can announce a session the moment it exists. The control
// stream needs that announcement to seed the console's per-session projection
// store, and a store that cannot does not have to say anything: the baseline
// still covers the sessions that existed when the stream opened.
type sessionRegistrar interface {
	// RegisterSession records that a session exists, with no selection in it yet.
	RegisterSession(sessionID string)
}

// sessionModelIdentity is the optional half of the model-selection store that
// names what a session currently runs on. It is optional because a store that can
// build a session's adapter does not have to be able to answer this cheaply, and
// the only consumer is the provenance stamped on a projected transcript: a read
// that must not build anything, and must not consume the route it reports.
type sessionModelIdentity interface {
	// SessionModelIdentity reports the provider and model a session will run on.
	// ok is false for a session with no recorded choice, which the caller answers
	// with the host's configured default.
	SessionModelIdentity(sessionID string) (provider, model string, ok bool)
}

// SetModelSelections installs the store session/selectModel answers through and
// the prompt path reads for the run's own model. Nil leaves the method unserved,
// which is the honest answer for a host that cannot build a per-session adapter.
func (h *Handler) SetModelSelections(store ModelSelectionStore) {
	h.modelSelectionsMu.Lock()
	h.modelSelections = store
	h.modelSelectionsMu.Unlock()
}

func (h *Handler) modelSelectionStore() ModelSelectionStore {
	h.modelSelectionsMu.RLock()
	defer h.modelSelectionsMu.RUnlock()
	return h.modelSelections
}

// sessionSelectModel answers POST /api/session/selectModel.
func (h *Handler) sessionSelectModel(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "sessionId", "provider", "model", "reasoningEffort"); failure != nil {
		return nil, failure
	}
	sessionID, _, failure := stringArg(args, "sessionId")
	if failure != nil {
		return nil, failure
	}
	providerName, _, failure := stringArg(args, "provider")
	if failure != nil {
		return nil, failure
	}
	modelName, _, failure := stringArg(args, "model")
	if failure != nil {
		return nil, failure
	}
	effort, _, failure := stringArg(args, "reasoningEffort")
	if failure != nil {
		return nil, failure
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, argumentRequired("sessionId")
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, fail(codeArgumentsInvalid, err.Error(), map[string]any{"argument": "sessionId"})
	}
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil, argumentRequired("provider")
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, argumentRequired("model")
	}
	store := h.modelSelectionStore()
	if store == nil {
		return nil, fail(codeUnimplemented,
			"session/selectModel is not configured: this host has no model-selection store; the serve command must install one with Handler.SetModelSelections",
			map[string]any{"dependency": "ModelSelectionStore"})
	}
	selection := ModelSelection{Provider: providerName, Model: modelName, ReasoningEffort: strings.TrimSpace(effort)}
	selected, err := store.SelectModel(sessionID, selection)
	if err != nil {
		return nil, fail(codeArgumentsInvalid,
			fmt.Sprintf("session/selectModel: %s", err.Error()),
			map[string]any{"sessionId": sessionID, "provider": providerName, "model": modelName})
	}
	return map[string]any{"selected": selected}, nil
}
