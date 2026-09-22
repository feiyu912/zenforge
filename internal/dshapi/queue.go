package dshapi

import (
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// The pending queue's projection source (ADR 0130, ADR 0136).
//
// A message a console submits while a turn is running does not enter the
// transcript yet: `session/prompt` hands it to the run's queue, and the agent
// takes it at the next model-turn boundary. Until then the console renders it
// from the `inbox` projection cell, and edits it with `session/updateQueue`.
//
// **The queue is session-log state, and this store is a fold over it.** Every
// acceptance, edit, removal, promotion and delivery is one `agent/inbox/spliced`
// event in the session's own log (the reference's event type and payload, so the
// console reads the same history upstream writes), and the cell is derived by
// replaying them. That is what makes a queued message outlive the run it was
// queued for: a restarted host folds the same rows back, and the session's next
// turn is handed them before it runs.
//
// The run queue is still consulted, because it is the authority on *delivery*:
// a folded row the live run no longer holds was handed to the agent, and this
// store records that as a splice (a removal with no `outcome`, since delivery is
// not a cancellation) before it publishes the cell. A run this process no longer
// holds proves nothing -- its rows stay pending -- which is the difference
// between "delivered" and "the process that held it is gone".

// The two prompt modes `session/prompt` accepts, which are also the console's own
// names for the two halves of its queue.
const (
	promptModeQueue = "queue"
	promptModeSteer = "steer"
)

// queueStore derives the pending queue from the session's log, records what the
// log cannot say by itself (a delivery observed on the live run queue), and feeds
// the observers a live stream subscribes with.
type queueStore struct {
	manager *harnesshttp.RunManager
	// readEvents reads one session's raw events, which is the fold's input.
	readEvents func(sessionID string) ([]zenforge.Event, error)
	// sessionRuns names the session's turns, oldest first, so the store can ask
	// the newest one's queue what it still holds.
	sessionRuns func(sessionID string) []string
	// appendEvent adds one splice to the session's newest turn.
	appendEvent func(sessionID string, payload map[string]any) error

	mu sync.Mutex
	// published is the last cell each session was told about, so a read that
	// changed nothing owes no frame.
	published map[string]dshstream.QueueState
	// broken remembers a session whose inbox history could not be folded. The
	// read answers an empty cell -- the one case where that answer is not the
	// truth -- and every mutation refuses with the reason, because a host that
	// cannot read its own log must not keep writing to it.
	broken    map[string]error
	observers map[int]func(dshstream.QueueUpdate)
	nextID    int
	seq       int64
}

func newQueueStore(
	manager *harnesshttp.RunManager,
	readEvents func(sessionID string) ([]zenforge.Event, error),
	sessionRuns func(sessionID string) []string,
	appendEvent func(sessionID string, payload map[string]any) error,
) *queueStore {
	return &queueStore{
		manager:     manager,
		readEvents:  readEvents,
		sessionRuns: sessionRuns,
		appendEvent: appendEvent,
		published:   map[string]dshstream.QueueState{},
		broken:      map[string]error{},
		observers:   map[int]func(dshstream.QueueUpdate){},
	}
}

// state reports one session's current cell. A session that never queued anything
// has an empty queue, which the fold answers without any special case.
func (s *queueStore) state(sessionID string) dshstream.QueueState {
	return s.reconcile(sessionID)
}

// reconcile folds the session's log, records any delivery the live run queue
// proves, and publishes the cell when the rows moved. It is the one place the
// queue is derived, so a read and a mutation cannot disagree about it.
func (s *queueStore) reconcile(sessionID string) dshstream.QueueState {
	if sessionID == "" {
		return dshstream.QueueState{}
	}
	folded, err := s.fold(sessionID)
	if err != nil {
		s.mu.Lock()
		s.broken[sessionID] = err
		s.mu.Unlock()
		return dshstream.QueueState{}
	}
	s.mu.Lock()
	delete(s.broken, sessionID)
	s.mu.Unlock()

	folded = s.recordDelivery(sessionID, folded)
	state := dshstream.QueueState{NextTurn: folded.NextTurn, NextStep: folded.NextStep}

	s.mu.Lock()
	previous, seen := s.published[sessionID]
	if seen && sameQueueRows(state, previous) {
		s.mu.Unlock()
		return previous
	}
	state.Seq = s.nextSeqLocked()
	s.published[sessionID] = state
	observers := s.observersLocked()
	s.mu.Unlock()
	notifyQueue(observers, dshstream.QueueUpdate{SessionID: sessionID, State: state})
	return state
}

// fold reads one session's turns and replays its splices.
func (s *queueStore) fold(sessionID string) (inboxState, error) {
	events, err := s.readEvents(sessionID)
	if err != nil {
		return inboxState{}, err
	}
	return foldInbox(events)
}

// recordDelivery turns "the live run no longer holds this row" into a durable
// claim, so a restarted host does not restore a message the agent already
// received. Only a *live* run can prove delivery: a run that ended, or one this
// process never held, leaves its rows pending -- and those are exactly the rows
// the session's next turn is handed.
func (s *queueStore) recordDelivery(sessionID string, folded inboxState) inboxState {
	if len(folded.NextTurn) == 0 && len(folded.NextStep) == 0 {
		return folded
	}
	runs := s.sessionRuns(sessionID)
	if len(runs) == 0 {
		return folded
	}
	live, err := s.manager.PendingSteers(runs[len(runs)-1])
	if err != nil {
		return folded
	}
	held := make(map[string]bool, len(live))
	for _, steer := range live {
		held[steer.ID] = true
	}
	claimed := false
	for _, target := range []string{inboxNextTurn, inboxNextStep} {
		items := folded.NextTurn
		if target == inboxNextStep {
			items = folded.NextStep
		}
		// Walk backwards so each recorded removal is expressed against the list
		// as it stands at that point, which is what the fold replays.
		for index := len(items) - 1; index >= 0; index-- {
			if held[items[index].ID] {
				continue
			}
			payload := inboxSplice(target, index, 1, nil, false)
			if err := s.appendEvent(sessionID, payload); err != nil {
				// A claim the host cannot record stays pending: it is still true
				// that the row was queued, and the next read tries again.
				continue
			}
			folded = removeInboxItem(folded, target, index)
			claimed = true
		}
	}
	if !claimed {
		return folded
	}
	return folded
}

// removeInboxItem applies one local removal to the folded state, mirroring what
// the appended splice will fold to.
func removeInboxItem(state inboxState, target string, index int) inboxState {
	if target == inboxNextStep {
		state.NextStep = append(append([]dshstream.QueueItem{}, state.NextStep[:index]...), state.NextStep[index+1:]...)
		return state
	}
	state.NextTurn = append(append([]dshstream.QueueItem{}, state.NextTurn[:index]...), state.NextTurn[index+1:]...)
	return state
}

// record appends one splice and republishes the session's cell. Every mutation
// this handler serves goes through here, so the log and the cell cannot drift:
// the write is durable first, and the cell is then derived from it.
func (s *queueStore) record(sessionID string, payload map[string]any) *methodError {
	if sessionID == "" {
		return nil
	}
	if failure := s.brokenFailure(sessionID); failure != nil {
		return failure
	}
	if err := s.appendEvent(sessionID, payload); err != nil {
		return fail(codeInternal, "record pending input: "+err.Error(), map[string]any{"sessionId": sessionID})
	}
	s.reconcile(sessionID)
	return nil
}

// brokenFailure refuses a mutation on a session whose inbox history cannot be
// folded. Writing more splices onto a history the host cannot read would bury the
// problem rather than answer it.
func (s *queueStore) brokenFailure(sessionID string) *methodError {
	s.mu.Lock()
	defer s.mu.Unlock()
	err, broken := s.broken[sessionID]
	if !broken {
		return nil
	}
	return fail(codeInternal, "read pending input: "+err.Error(), map[string]any{"sessionId": sessionID})
}

// enqueue records one accepted message: the console's own submission, spliced
// onto the end of the list its mode names.
func (s *queueStore) enqueue(sessionID, mode, steerID, text string) *methodError {
	target := inboxNextStep
	if mode == promptModeQueue {
		target = inboxNextTurn
	}
	folded, err := s.fold(sessionID)
	if err != nil {
		s.mu.Lock()
		s.broken[sessionID] = err
		s.mu.Unlock()
		return fail(codeInternal, "read pending input: "+err.Error(), map[string]any{"sessionId": sessionID})
	}
	length := len(folded.NextStep)
	if target == inboxNextTurn {
		length = len(folded.NextTurn)
	}
	return s.record(sessionID, inboxSplice(target, length, 0, []any{inboxMessage(steerID, text)}, false))
}

// replace records an edit in place: one message removed and one inserted at the
// same position, which is what keeps the row where the operator put it. The row is
// located by identity at write time rather than by a position read earlier,
// because a delivery recorded in between would have moved it.
func (s *queueStore) replace(sessionID, itemID, text string) *methodError {
	located, ok, failure := s.locate(sessionID, itemID)
	if failure != nil || !ok {
		return failure
	}
	return s.record(sessionID, inboxSplice(located.Target, located.Index, 1,
		[]any{inboxMessage(itemID, text)}, false))
}

// discard records a removal the operator asked for: the row is canceled, which is
// the outcome the reference writes when input is discarded rather than delivered.
func (s *queueStore) discard(sessionID, itemID string) *methodError {
	located, ok, failure := s.locate(sessionID, itemID)
	if failure != nil || !ok {
		return failure
	}
	return s.record(sessionID, inboxSplice(located.Target, located.Index, 1, nil, true))
}

// locate folds the session's pending rows and finds one by identity.
//
// It deliberately does **not** infer a delivery first. The operator's own
// mutation has already taken the row out of the live run queue by the time this
// runs -- `DropSteer` and `EditSteer` are applied first, so a refusal is the
// reference's own -- and inference would read that as "the agent received it"
// and record a delivery where the log owes a cancellation. Inference belongs to
// the read path (`item`, `state`), where the run queue is the only evidence
// there is.
func (s *queueStore) locate(sessionID, itemID string) (queuedMessage, bool, *methodError) {
	folded, err := s.fold(sessionID)
	if err != nil {
		s.mu.Lock()
		s.broken[sessionID] = err
		s.mu.Unlock()
		return queuedMessage{}, false, fail(codeInternal, "read pending input: "+err.Error(),
			map[string]any{"sessionId": sessionID})
	}
	if item, ok := locateInboxItem(folded, itemID); ok {
		return item, true, nil
	}
	return queuedMessage{}, false, nil
}

// locateInboxItem finds one pending row and the list it sits in.
func locateInboxItem(state inboxState, itemID string) (queuedMessage, bool) {
	for index, item := range state.NextTurn {
		if item.ID == itemID {
			return queuedMessage{ID: item.ID, Text: item.Text, Mode: promptModeQueue, Target: inboxNextTurn, Index: index}, true
		}
	}
	for index, item := range state.NextStep {
		if item.ID == itemID {
			return queuedMessage{ID: item.ID, Text: item.Text, Mode: promptModeSteer, Target: inboxNextStep, Index: index}, true
		}
	}
	return queuedMessage{}, false
}

// restore hands the session's still-pending rows to a run that does not hold them
// yet, and leaves them pending until that run claims them.
//
// This is the half that makes the fold worth having: after a restart -- or after
// the run a message was queued for ended before its boundary -- the rows are in
// the log and not in any queue, so the session's next turn is handed them here
// instead of carrying them to a turn nobody will run (ADR 0136 records the
// deviation: upstream starts a turn of its own for them).
func (s *queueStore) restore(sessionID, runID string) {
	if sessionID == "" || runID == "" {
		return
	}
	folded, err := s.fold(sessionID)
	if err != nil {
		s.mu.Lock()
		s.broken[sessionID] = err
		s.mu.Unlock()
		return
	}
	live, err := s.manager.PendingSteers(runID)
	if err != nil {
		return
	}
	held := make(map[string]bool, len(live))
	for _, steer := range live {
		held[steer.ID] = true
	}
	for _, item := range append(append([]dshstream.QueueItem{}, folded.NextTurn...), folded.NextStep...) {
		if held[item.ID] || item.ID == "" {
			continue
		}
		// The identity the console knows the row by is the steer id, so the run
		// queue receives it under the same id the log holds.
		_, _ = s.manager.Steer(runID, item.ID, item.Text)
	}
	s.reconcile(sessionID)
}

// item locates one pending message and the list it is in, with its index, as the
// console's own read sees it: a delivery is recorded first, so a message the run
// has already been handed is reported as gone rather than as a row the console may
// still be painting.
func (s *queueStore) item(sessionID, itemID string) (queuedMessage, bool) {
	folded, err := s.fold(sessionID)
	if err != nil {
		s.mu.Lock()
		s.broken[sessionID] = err
		s.mu.Unlock()
		return queuedMessage{}, false
	}
	if item, ok := locateInboxItem(s.recordDelivery(sessionID, folded), itemID); ok {
		return item, true
	}
	return queuedMessage{}, false
}

// queuedMessage is one pending message, with the list it is in and where.
type queuedMessage struct {
	ID     string
	Text   string
	Mode   string
	Target string
	Index  int
}

// refresh republishes one session's cell after a mutation this handler applied to
// the run queue. The splices are written by the caller; this only derives the
// cell again.
func (s *queueStore) refresh(sessionID string) {
	s.reconcile(sessionID)
}

// promote records the console's "steer" action: the row leaves the list awaiting a
// turn of its own and joins the list the running turn is about to receive. This
// host's queue delivers both halves at the same boundary (ADR 0111), so the change
// is where the row is shown -- and the log says the same thing.
func (s *queueStore) promote(sessionID, itemID string) *methodError {
	located, ok, failure := s.locate(sessionID, itemID)
	if failure != nil || !ok {
		return failure
	}
	if failure := s.discard(sessionID, itemID); failure != nil {
		return failure
	}
	folded, err := s.fold(sessionID)
	if err != nil {
		return fail(codeInternal, "read pending input: "+err.Error(), map[string]any{"sessionId": sessionID})
	}
	return s.record(sessionID, inboxSplice(inboxNextStep, len(folded.NextStep), 0,
		[]any{inboxMessage(located.ID, located.Text)}, false))
}

// observe registers one carrier's callback and returns the function that stops
// it. The id is what makes a subscription removable at all: Go function values
// are not comparable, so the observer set is keyed by a counter.
func (s *queueStore) observe(observe func(dshstream.QueueUpdate)) (unsubscribe func()) {
	if observe == nil {
		return func() {}
	}
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.observers[id] = observe
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.observers, id)
		s.mu.Unlock()
	}
}

// observersLocked copies the subscriber set so the callbacks run without the
// lock. Callers hold the lock.
func (s *queueStore) observersLocked() []func(dshstream.QueueUpdate) {
	observers := make([]func(dshstream.QueueUpdate), 0, len(s.observers))
	for _, observe := range s.observers {
		observers = append(observers, observe)
	}
	return observers
}

// nextSeqLocked advances the queue's sequence for one change. It is a
// wall-clock-anchored monotone counter, not a session-log watermark: the console's
// history seed installs the projections block at the session cursor's watermark
// and discards a frame numbered at or below it, while a pending message is not a
// session-log event and has no cursor of its own. The clock supplies the margin
// above every cursor a session can reach and survives a restart, where a small
// counter would restart below the cursors already served. Callers hold the lock.
func (s *queueStore) nextSeqLocked() int64 {
	now := time.Now().UnixMilli()
	if now > s.seq {
		s.seq = now
	} else {
		s.seq++
	}
	return s.seq
}

// notifyQueue delivers one change to every subscribed carrier. Callers must not
// hold the store's lock: a subscriber hands the frame to a stream.
func notifyQueue(observers []func(dshstream.QueueUpdate), update dshstream.QueueUpdate) {
	for _, observe := range observers {
		observe(update)
	}
}

// sameQueueRows reports whether two cells hold the same rows in the same order,
// which is how a read decides that nothing changed and no frame is owed.
func sameQueueRows(state, other dshstream.QueueState) bool {
	return sameQueueItems(state.NextTurn, other.NextTurn) && sameQueueItems(state.NextStep, other.NextStep)
}

func sameQueueItems(items, other []dshstream.QueueItem) bool {
	if len(items) != len(other) {
		return false
	}
	for index := range items {
		if items[index] != other[index] {
			return false
		}
	}
	return true
}

// PendingQueue reports one session's pending queue in the shape the console's
// `inbox` projection cell carries. It is the transport's read for that cell: the
// follow snapshot publishes it and every later value arrives as a control frame.
func (h *Handler) PendingQueue(sessionID string) dshstream.QueueState {
	if h == nil || h.queues == nil {
		return dshstream.QueueState{}
	}
	return h.queues.state(sessionID)
}

// PendingQueueUpdates delivers every later queue change until the returned
// function is called. A queue a console is not watching still changes -- the run
// takes what it was given -- so the read is what reports a delivery, and this is
// the feed a live stream subscribes to.
func (h *Handler) PendingQueueUpdates(observe func(dshstream.QueueUpdate)) (unsubscribe func()) {
	if h == nil || h.queues == nil {
		return func() {}
	}
	return h.queues.observe(observe)
}
