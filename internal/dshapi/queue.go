package dshapi

import (
	"sync"
	"time"

	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// The pending queue's projection source (ADR 0130).
//
// A message a console submits while a turn is running does not enter the
// transcript yet: `session/prompt` hands it to the run's queue, and the agent
// takes it at the next model-turn boundary. Until then the console renders it
// from the `inbox` projection cell, and edits it with `session/updateQueue`.
//
// The cell is derived here rather than in the stream because the run queue is
// the only copy of what has not been delivered, and the two ways it changes are
// visible only around this handler: a mutation (prompt, edit, drop, promote)
// happens here, and a delivery is performed by the agent, which is why a read
// reconciles against `RunManager.PendingSteers` instead of trusting a cache. A
// message that leaves the run queue is gone from the cell on the next read,
// which is what retires the console's row when the message becomes a durable turn.
//
// What this store owns is only what the run queue cannot say: the *mode* each
// message was queued with. The console splits its queue into prompts awaiting a
// turn of their own and input awaiting the current turn's next step boundary, and
// that split is the prompt mode the console asked for -- not something the
// harness records, because here both modes take the same path (ADR 0111).

// The two prompt modes `session/prompt` accepts, which are also the console's own
// names for the two halves of its queue.
const (
	promptModeQueue = "queue"
	promptModeSteer = "steer"
)

// queueStore is the pending queue's source of truth for projection: one entry per
// session that has queued anything in this process, and the observers a live
// stream subscribes with.
type queueStore struct {
	manager *harnesshttp.RunManager

	mu        sync.Mutex
	sessions  map[string]*sessionQueue
	observers map[int]func(dshstream.QueueUpdate)
	nextID    int
	seq       int64
}

// sessionQueue is what one session's pending messages were queued as. The rows
// themselves are read from the run queue on every reconcile; only the mode of
// each message, and the last published cell, are remembered here.
type sessionQueue struct {
	// runID is the turn whose queue holds the pending messages.
	runID string
	// modes maps a steer id to the prompt mode it was queued with. A message this
	// host queued without a console mode is absent, and then it is treated as input
	// awaiting the next step boundary -- which is what this host's queue always is.
	modes map[string]string
	// state is the last derived cell, so a read that changed nothing owes no frame.
	state dshstream.QueueState
}

func newQueueStore(manager *harnesshttp.RunManager) *queueStore {
	return &queueStore{
		manager:   manager,
		sessions:  map[string]*sessionQueue{},
		observers: map[int]func(dshstream.QueueUpdate){},
	}
}

// state reports one session's current cell, reconciling it against the run queue
// first. A session that never queued anything has an empty queue and no entry to
// read, which is the honest answer rather than a lookup.
func (s *queueStore) state(sessionID string) dshstream.QueueState {
	s.mu.Lock()
	entry := s.sessions[sessionID]
	s.mu.Unlock()
	if entry == nil {
		return dshstream.QueueState{}
	}
	return s.reconcile(sessionID, entry)
}

// reconcile rebuilds one session's cell from the run queue that holds the truth,
// publishes the change when the rows moved, and returns the current cell. A run
// this process no longer holds has nothing pending: the queue died with it.
func (s *queueStore) reconcile(sessionID string, entry *sessionQueue) dshstream.QueueState {
	live, err := s.manager.PendingSteers(entry.runID)
	if err != nil {
		live = nil
	}
	s.mu.Lock()
	state := dshstream.QueueState{}
	for _, steer := range live {
		item := dshstream.QueueItem{ID: steer.ID, Text: steer.Message}
		if entry.modes[steer.ID] == promptModeQueue {
			state.NextTurn = append(state.NextTurn, item)
		} else {
			state.NextStep = append(state.NextStep, item)
		}
	}
	if sameQueueRows(state, entry.state) {
		current := entry.state
		s.mu.Unlock()
		return current
	}
	state.Seq = s.nextSeqLocked()
	entry.state = state
	observers := s.observersLocked()
	s.mu.Unlock()
	notifyQueue(observers, dshstream.QueueUpdate{SessionID: sessionID, State: state})
	return state
}

// enqueue remembers the console's mode for one message it just queued and
// republishes the session's cell, so the row the console drew as its own local
// echo is replaced by the host's row without waiting for anything else to happen.
func (s *queueStore) enqueue(sessionID, runID, mode, steerID string) {
	if sessionID == "" || runID == "" {
		return
	}
	s.mu.Lock()
	entry := s.sessions[sessionID]
	if entry == nil {
		entry = &sessionQueue{modes: map[string]string{}}
		s.sessions[sessionID] = entry
	}
	entry.runID = runID
	if steerID != "" {
		entry.modes[steerID] = mode
	}
	s.mu.Unlock()
	s.reconcile(sessionID, entry)
}

// refresh republishes one session's cell after a mutation this handler applied to
// the run queue: the rows are read from that queue, so the write itself changes
// nothing a client can see until the cell is derived again and the change is
// announced. A session that never queued anything has no entry and nothing to
// republish.
func (s *queueStore) refresh(sessionID string) {
	s.mu.Lock()
	entry := s.sessions[sessionID]
	s.mu.Unlock()
	if entry != nil {
		s.reconcile(sessionID, entry)
	}
}

// promote moves one queued message into the steering half of the cell, which is
// what the console's "steer" action asks for: the message is handed to the
// running turn at its next model-turn boundary instead of waiting for a turn of
// its own. This host's queue delivers both halves at that same boundary, so the
// change is in where the console shows the row; nothing about delivery moves.
func (s *queueStore) promote(sessionID, itemID string) {
	s.mu.Lock()
	entry := s.sessions[sessionID]
	if entry != nil && itemID != "" {
		entry.modes[itemID] = promptModeSteer
	}
	s.mu.Unlock()
	if entry != nil {
		s.reconcile(sessionID, entry)
	}
}

// item locates one pending message and the half of the queue it is in. It
// reconciles first, so a message the run has already been handed is reported as
// gone rather than as a row the console may still be painting.
func (s *queueStore) item(sessionID, itemID string) (queuedMessage, bool) {
	s.mu.Lock()
	entry := s.sessions[sessionID]
	s.mu.Unlock()
	if entry == nil {
		return queuedMessage{}, false
	}
	state := s.reconcile(sessionID, entry)
	for _, item := range state.NextTurn {
		if item.ID == itemID {
			return queuedMessage{RunID: entry.runID, ID: item.ID, Text: item.Text, Mode: promptModeQueue}, true
		}
	}
	for _, item := range state.NextStep {
		if item.ID == itemID {
			return queuedMessage{RunID: entry.runID, ID: item.ID, Text: item.Text, Mode: promptModeSteer}, true
		}
	}
	return queuedMessage{}, false
}

// queuedMessage is one pending message, with the half of the queue it is in.
type queuedMessage struct {
	RunID string
	ID    string
	Text  string
	Mode  string
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
