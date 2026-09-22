package dshstream

import (
	"context"
	"sort"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/internal/dshsession"
)

// runEvents serves the $events logical stream.
//
// The first item is the ready frame, which binds later POST /api/$events/result
// answers to this generation (stream-protocol.ts RemoteEventReadyFrame;
// api/gateway/src/index.ts openRemoteEvents yields it before anything else).
// After that the stream delivers the forwarded events this host can honestly
// produce: an approval waterfall for every pending approval.Inbox request, a
// cancellation frame when one stops being pending, and a
// `goal/activation-changed` emit for every committed goal mutation.
//
// It does not deliver emit frames for the api-session/* family. Upstream gets
// those from a host-wide Cordis event bus; this repository's eventlog.Bus is
// per-run and the run manager exposes no registry change feed, so a session
// emit frame could only be invented or polled into existence here. The console
// still lists sessions from POST /api/session/list and follows a run's events
// from session/follow; what it loses is live sidebar mutation. That gap is
// deliberate and reported rather than papered over.
//
// The goal emit is a different case and is why it is here: the goal store is a
// host-side store with a real change feed (the same one the control stream's
// projection frames ride), so the dock's activation hook can be told the exact
// current `{id, revision, activation}` instead of only discovering it when it
// asks for a read.
func (h *Handler) runEvents(ctx context.Context, payload []byte, send func(any) error) error {
	args, failure := endpointArgs(payload)
	if failure != nil {
		return failure
	}
	if !emptyArgs(args) {
		return streamFail(codeArgumentsInvalid,
			"the $events stream requires an empty args object",
			map[string]any{"endpoint": eventStreamEndpoint})
	}

	clientID := newClientID()
	h.registerClient(clientID)
	defer h.unregisterClient(clientID)

	// Subscribe before the ready frame, so a mutation that commits while the
	// stream is opening is delivered rather than lost in the gap, and coalesce
	// per session: only the latest activation of a session matters, and a client
	// that missed one reads the current value instead.
	pending := map[string]GoalUpdate{}
	signal := make(chan struct{}, 1)
	var unsubscribe func()
	if h.cfg.GoalUpdates != nil {
		unsubscribe = h.cfg.GoalUpdates(func(update GoalUpdate) {
			h.mu.Lock()
			pending[update.SessionID] = update
			h.mu.Unlock()
			select {
			case signal <- struct{}{}:
			default:
			}
		})
	}
	if unsubscribe != nil {
		defer unsubscribe()
	}

	if err := send(readyValue{
		Type:     "ready",
		ClientID: clientID,
		Host:     hostInfo{Home: h.cfg.Home},
	}); err != nil {
		return err
	}

	delivered := make(map[string]struct{})
	ticker := time.NewTicker(h.cfg.ApprovalPollInterval)
	defer ticker.Stop()
	for {
		if err := h.deliverApprovals(ctx, send, delivered); err != nil {
			return err
		}
		if err := h.deliverGoalActivations(send, pending); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-signal:
			// A goal mutation committed: deliver it now instead of waiting for the
			// next approval poll, which exists for a different carrier.
		}
	}
}

// deliverGoalActivations drains the coalesced goal mutations into
// `goal/activation-changed` emit frames, oldest sequence first so a client sees
// them in the order they happened rather than in Go's map order.
func (h *Handler) deliverGoalActivations(send func(any) error, pending map[string]GoalUpdate) error {
	for {
		h.mu.Lock()
		oldest := ""
		var chosen GoalUpdate
		for sessionID, update := range pending {
			if oldest == "" || update.Seq < chosen.Seq {
				oldest, chosen = sessionID, update
			}
		}
		if oldest != "" {
			delete(pending, oldest)
		}
		h.mu.Unlock()
		if oldest == "" {
			return nil
		}
		if err := send(emitValue{
			Type:  "emit",
			Event: "goal/activation-changed",
			Args:  []any{chosen.activationChanged()},
		}); err != nil {
			return err
		}
	}
}

// deliverApprovals reconciles the inbox against what this generation has
// already delivered: new pending requests become waterfalls, and a previously
// delivered request that is no longer pending becomes a cancellation. The
// inbox is the source of truth on purpose — a waterfall is only published for
// a request that can actually be answered, so the client never gets an
// approval it cannot act on.
func (h *Handler) deliverApprovals(ctx context.Context, send func(any) error, delivered map[string]struct{}) error {
	pending, err := h.inbox.List(ctx, "")
	if err != nil {
		return streamFail(codeInternal, "list pending approvals: "+err.Error(), nil)
	}
	now := time.Now()
	current := make(map[string]approval.Request, len(pending))
	for _, request := range pending {
		if request.ExpiresAt != nil && !request.ExpiresAt.After(now) {
			// An expired request is not answerable. It is left out of the
			// waterfall set; a later answer fails closed in the result route.
			continue
		}
		current[request.ID] = request
	}

	cancelled := make([]string, 0, len(delivered))
	for id := range delivered {
		if _, ok := current[id]; !ok {
			cancelled = append(cancelled, id)
		}
	}
	sort.Strings(cancelled)
	for _, id := range cancelled {
		delete(delivered, id)
		if err := send(cancelValue{Type: "cancel", EventID: id}); err != nil {
			return err
		}
	}

	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := delivered[id]; ok {
			continue
		}
		delivered[id] = struct{}{}
		request := current[id]
		// The console scopes an approval by the session it belongs to: its
		// approval panel resolves the event's agent to a session it has open and
		// drops the request ("next()") when it cannot. A conversation's later turn
		// runs under "<session>~<turn>" (ADR 0108), which is not an id the console
		// knows, so the waterfall names the conversation. The answer still routes
		// by eventId and clientId, and the request id names the turn it belongs to.
		sessionID, _ := dshsession.Base(request.RunID)
		if err := send(waterfallValue{
			Type:    "waterfall",
			Event:   "approval/request",
			EventID: request.ID,
			AgentID: sessionID,
			Request: approvalRequestPayload(request),
		}); err != nil {
			return err
		}
	}
	return nil
}

// approvalRequestPayload projects one approval.Request onto the console's
// client-safe request object. Upstream's projectRemoteEventRequest strips the
// scoped Agent and the AbortSignal and keeps the rest; the client additionally
// rejects a request object that carries "agent" or "signal", so neither key is
// ever written here.
//
// The console's approval panel reads toolName, callId, and reason. toolName and
// reason are made total here (a zenforge request may omit ToolName and carry
// its human-readable text in Title/Description) because the panel renders them
// directly. operation, risk, and title are extra JSON fields the client keeps
// and ignores; they cost nothing and make the pending approval legible.
func approvalRequestPayload(request approval.Request) map[string]any {
	toolName := request.ToolName
	if toolName == "" {
		toolName = request.Operation
	}
	reason := request.Description
	if reason == "" {
		reason = request.Title
	}
	payload := map[string]any{
		"toolName":  toolName,
		"operation": request.Operation,
		"risk":      string(request.Risk),
		"title":     request.Title,
	}
	if request.ToolCallID != "" {
		payload["callId"] = request.ToolCallID
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return payload
}
