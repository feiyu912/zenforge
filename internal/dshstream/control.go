package dshstream

import "context"

// runControl serves the session/control logical stream.
//
// The first item is exactly one baseline frame, which the client's snapshot
// stream requires before it will accept any later frame
// (api/session-controller/src/client/transport.ts createSessionControlStream).
// Upstream's baseline carries {jobs, projections} keyed by session id; both are
// sent empty here, which upstream itself documents as a legal minimal baseline
// (types.ts SessionControlBaseline, and the recon's §3(f)).
//
// The jobs map stays empty and is honest about it: harnesshttp.RunManager models
// one run, not a per-session background-job list, and inventing rows would be a
// lie the panel renders. The projections map carries what this host does have:
// each session's durable model selection, which the model picker reads back
// (`projected.next ?? catalog.default`). The stream stays open after the baseline
// because the client treats an end after the baseline as a lost carrier and
// retries; it ends only when the client cancels it or the socket closes.
func (h *Handler) runControl(ctx context.Context, payload []byte, send func(any) error) error {
	args, failure := endpointArgs(payload)
	if failure != nil {
		return failure
	}
	if !emptyArgs(args) {
		return streamFail(codeArgumentsInvalid,
			"the session/control stream takes no arguments",
			map[string]any{"endpoint": "session/control"})
	}
	// A selection change arrives on the goroutine answering session/selectModel,
	// so the hand-off never blocks that call and never stalls on a slow socket:
	// updates coalesce per session, because only the latest value of a projection
	// matters and a client that missed one sees the next baseline.
	pending := map[string]ModelSelectionUpdate{}
	signal := make(chan struct{}, 1)
	// Subscribe before reading the baseline. A change that lands while the
	// baseline is being assembled is then delivered as a frame instead of falling
	// into the gap between the two, and a value that appears in both is harmless:
	// the client applies the latest one and the sequence orders them.
	var unsubscribe func()
	if h.cfg.ModelSelectionUpdates != nil {
		unsubscribe = h.cfg.ModelSelectionUpdates(func(update ModelSelectionUpdate) {
			h.mu.Lock()
			pending[update.SessionID] = update
			h.mu.Unlock()
			select {
			case signal <- struct{}{}:
			default:
			}
		})
	}
	baseline := controlBaseline{
		Jobs:        map[string][]sessionJob{},
		Queues:      map[string][]sessionQueuedItem{},
		Projections: h.projectionBaseline(ctx),
	}
	if err := send(controlBaselineFrame{Type: "baseline", Value: baseline}); err != nil {
		if unsubscribe != nil {
			unsubscribe()
		}
		return err
	}
	if unsubscribe == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signal:
		}
		for {
			h.mu.Lock()
			update, ok := takePending(pending)
			h.mu.Unlock()
			if !ok {
				break
			}
			frame := projectionFrame{
				Type:      "projection",
				SessionID: update.SessionID,
				Key:       modelSelectionProjectionKey,
				Value:     update.Projection,
				Seq:       update.Seq,
			}
			if err := send(frame); err != nil {
				return err
			}
		}
	}
}

// takePending removes one coalesced update. The map is unordered, so the oldest
// sequence goes first and a client sees projection changes in the order they
// happened rather than in Go's map order.
func takePending(pending map[string]ModelSelectionUpdate) (ModelSelectionUpdate, bool) {
	oldest := ""
	var chosen ModelSelectionUpdate
	for sessionID, update := range pending {
		if oldest == "" || update.Seq < chosen.Seq {
			oldest, chosen = sessionID, update
		}
	}
	if oldest == "" {
		return ModelSelectionUpdate{}, false
	}
	delete(pending, oldest)
	return chosen, true
}

// projectionBaseline builds the control baseline's per-session projections. A
// session with no selection is absent rather than null: an absent key means the
// capability has no value at this cursor, which is what the client expects before
// anyone chooses a model.
func (h *Handler) projectionBaseline(ctx context.Context) map[string]any {
	projections := map[string]any{}
	if h.cfg.ModelSelections == nil {
		return projections
	}
	for sessionID, state := range h.cfg.ModelSelections() {
		projections[sessionID] = projectionBaseline{
			AsOfSeq: state.Seq,
			Values:  map[string]any{modelSelectionProjectionKey: state.Projection},
		}
	}
	// Every other session this host serves gets the key too, with no selection in
	// it. The console seeds its per-session projection store from this baseline
	// and treats an absent key as "capability absent": its selector then holds
	// "Loading models…" with no groups forever, however well the catalog answered
	// (api/session-controller/src/client/sessions/manager.ts replaceControlBaseline,
	// ui-model-selection/src/client/directory.ts:146-160). An empty projection
	// means no next and no last-used, so the selector falls back to the catalog's
	// default, and the cut is 0 because nothing has been selected at any cursor.
	if infos, err := h.manager.List(ctx); err == nil {
		for _, info := range infos {
			if _, recorded := projections[info.RunID]; recorded {
				continue
			}
			projections[info.RunID] = projectionBaseline{
				AsOfSeq: 0,
				Values:  map[string]any{modelSelectionProjectionKey: ModelSelectionProjection{}},
			}
		}
	}
	return projections
}
