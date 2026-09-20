package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
// This host has one model adapter (ADR 0084's single credential, the serve
// command's one provider). A session's selection is therefore applied to that
// adapter before each of its runs rather than held per run: the selection is
// per-session, its effect is host-wide for the duration of the run. Two sessions
// running concurrently under different selections share whichever adapter was
// applied last, which is stated in the ADR and in docs/limitations.md.

// ModelSelectionStore is where a session's chosen provider and model lives, and
// what makes the choice real: applying it rebuilds the adapter the run uses. It is
// injected like the other seams, because only the serve command knows how to build
// an adapter and what it can route to.
type ModelSelectionStore interface {
	// SelectModel validates a selection against what this host can route to and
	// records it for the session, returning what it resolved.
	SelectModel(sessionID string, selection ModelSelection) (ModelSelection, error)
	// ApplyModelSelection makes the session's recorded selection the adapter the
	// run about to start will use. A session with no selection leaves the host's
	// configured adapter in place.
	ApplyModelSelection(sessionID string) error
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
// apply a selection does not have to be able to read one back, and the only
// consumer is the provenance stamped on a projected transcript.
type sessionModelIdentity interface {
	// SessionModelIdentity reports the provider and model a session will run on.
	// ok is false for a session with no recorded choice, which the caller answers
	// with the host's configured default.
	SessionModelIdentity(sessionID string) (provider, model string, ok bool)
}

// SetModelSelections installs the store session/selectModel answers through and
// the prompt path applies. Nil leaves the method unserved, which is the honest
// answer for a host that cannot rebuild a per-session adapter.
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
