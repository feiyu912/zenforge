package dshstream

import "context"

// runControl serves the session/control logical stream.
//
// The first item is exactly one baseline frame, which the client's snapshot
// stream requires before it will accept any later frame
// (api/session-controller/src/client/transport.ts createSessionControlStream).
// Upstream's baseline carries {jobs, projections} keyed by session id; the jobs
// map is sent empty here, which upstream itself documents as a legal minimal
// baseline (types.ts SessionControlBaseline, and the recon's §3(f)).
//
// The jobs map stays empty and is honest about it: harnesshttp.RunManager models
// one run, not a per-session background-job list, and inventing rows would be a
// lie the panel renders. The projections map carries what this host does have:
// each session's durable model selection, which the model picker reads back
// (`projected.next ?? catalog.default`). It deliberately does not carry the goal
// cell even though the composer's goal dock renders one -- projectionBaseline
// records why, and the cell travels with the session's own follow snapshot and
// this stream's later frames instead. The stream stays open after the baseline
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
	// A change arrives on the goroutine answering a unary RPC (selectModel, a
	// goals mutation), so the hand-off never blocks that call and never stalls on
	// a slow socket: updates coalesce per session and key, because only the latest
	// value of a projection matters and a client that missed one sees the next
	// baseline.
	pending := map[string]projectionChange{}
	signal := make(chan struct{}, 1)
	enqueue := func(change projectionChange) {
		h.mu.Lock()
		pending[change.key()] = change
		h.mu.Unlock()
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	// Subscribe before reading the baseline. A change that lands while the
	// baseline is being assembled is then delivered as a frame instead of falling
	// into the gap between the two, and a value that appears in both is harmless:
	// the client applies the latest one and the sequence orders them.
	var unsubscribe []func()
	if h.cfg.ModelSelectionUpdates != nil {
		unsubscribe = append(unsubscribe, h.cfg.ModelSelectionUpdates(func(update ModelSelectionUpdate) {
			enqueue(projectionChange{
				SessionID: update.SessionID,
				Key:       modelSelectionProjectionKey,
				Value:     update.Projection,
				Seq:       update.Seq,
			})
		}))
	}
	if h.cfg.GoalUpdates != nil {
		unsubscribe = append(unsubscribe, h.cfg.GoalUpdates(func(update GoalUpdate) {
			enqueue(projectionChange{
				SessionID: update.SessionID,
				Key:       goalProjectionKey,
				Value:     goalCell(update.Projection),
				Seq:       update.Seq,
			})
		}))
	}
	baseline := controlBaseline{
		Jobs:        map[string][]sessionJob{},
		Queues:      map[string][]sessionQueuedItem{},
		Projections: h.projectionBaseline(ctx),
	}
	if err := send(controlBaselineFrame{Type: "baseline", Value: baseline}); err != nil {
		for _, stop := range unsubscribe {
			stop()
		}
		return err
	}
	if len(unsubscribe) == 0 {
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() {
		for _, stop := range unsubscribe {
			stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signal:
		}
		for {
			h.mu.Lock()
			change, ok := takePending(pending)
			h.mu.Unlock()
			if !ok {
				break
			}
			frame := projectionFrame{
				Type:      "projection",
				SessionID: change.SessionID,
				Key:       change.Key,
				Value:     change.Value,
				Seq:       change.Seq,
			}
			if err := send(frame); err != nil {
				return err
			}
		}
	}
}

// projectionChange is one projection cell at one sequence: the value and key a
// frame carries, before it is known which of the host's projection sources
// produced it.
type projectionChange struct {
	SessionID string
	Key       string
	Value     any
	Seq       int64
}

// key identifies the coalescing slot: one pending value per session and
// projection key, so a slow client sees the latest value of each cell rather
// than a backlog of superseded ones.
func (c projectionChange) key() string {
	return c.SessionID + "\x00" + c.Key
}

// takePending removes one coalesced update. The map is unordered, so the oldest
// sequence goes first and a client sees projection changes in the order they
// happened rather than in Go's map order.
func takePending(pending map[string]projectionChange) (projectionChange, bool) {
	oldest := ""
	var chosen projectionChange
	for key, change := range pending {
		if oldest == "" || change.Seq < chosen.Seq {
			oldest, chosen = key, change
		}
	}
	if oldest == "" {
		return projectionChange{}, false
	}
	delete(pending, oldest)
	return chosen, true
}

// projectionBaseline builds the control baseline's per-session projections. The
// sessions are the ones this host serves -- the run manager's list plus every
// session a store records -- so a session the console can open always has the
// keys its panels look up.
//
// A key this host has no value for is absent rather than null: an absent key
// means the capability has no value at this cursor, which is what the console's
// model selector expects before anyone chooses a model.
//
// The goal cell is deliberately not here, and the reason is the client's own
// ordering rule. A baseline block carries ONE watermark for every key in it, and
// the model-selection cell's watermark is that store's own sequence. A goal's
// sequence has to outrank the session-log cursor -- the follow snapshot seeds
// the block at the cursor, and a goal frame numbered below it would be discarded
// (see dshstream.GoalProjectionState) -- so folding the goal cell into the same
// block would raise the block's watermark above the model-selection cell's
// sequence and freeze the model picker. The goal cell is seeded by the session's
// own opening snapshot instead, which is the only surface that renders it, and
// every later value arrives as a control frame with the goal's own sequence. The
// omission is safe there: the client's seed clears only the keys the block
// omits, and the follow block carries the goal key.
func (h *Handler) projectionBaseline(ctx context.Context) map[string]any {
	sessionIDs := map[string]bool{}
	if infos, err := h.manager.List(ctx); err == nil {
		for _, info := range infos {
			sessionIDs[info.RunID] = true
		}
	}
	var selections map[string]ModelSelectionState
	if h.cfg.ModelSelections != nil {
		selections = h.cfg.ModelSelections()
		for sessionID := range selections {
			sessionIDs[sessionID] = true
		}
	}
	projections := map[string]any{}
	for sessionID := range sessionIDs {
		if h.cfg.ModelSelections == nil {
			continue
		}
		// A session with no selection gets an empty projection rather than no key
		// at all: the console's selector treats an absent key as "capability
		// absent" and then holds "Loading models…" with no groups forever, however
		// well the catalog answered
		// (api/session-controller/src/client/sessions/manager.ts
		// replaceControlBaseline, ui-model-selection/src/client/directory.ts:146-160).
		// An empty projection means no next and no last-used, so the selector falls
		// back to the catalog's default.
		selection, recorded := selections[sessionID]
		if !recorded {
			selection = ModelSelectionState{}
		}
		projections[sessionID] = projectionBaseline{
			AsOfSeq: selection.Seq,
			Values:  map[string]any{modelSelectionProjectionKey: selection.Projection},
		}
	}
	return projections
}
