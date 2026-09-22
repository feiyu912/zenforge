package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/goals"
	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshstream"
)

// consoleGoals is the console's goal store: the framework's goal state machine
// over the same on-disk goal state the command line and the goal tools already
// use, plus the two faces the transport reads (ADR 0125).
//
// It exists so the dock's seven remote methods and the projection it renders are
// backed by the rules the rest of the repository already enforces -- create
// requires an absent or complete goal, every other mutation advances exactly one
// revision, and only legal phase transitions pass -- instead of a second
// interpretation written for the browser. The translation this file owns is the
// console's: the reference's error codes, the GoalView/GoalProjection envelopes,
// and the local sequence that orders a client's frames.
//
// The state directory is the serve command's own checkpoint directory, under the
// `goals/` subdirectory the CLI's `zenforge goal` and the goal tools write, so a
// goal belongs to its session whichever surface created it.
type consoleGoals struct {
	store goals.Store

	mu        sync.Mutex
	seq       int64
	observers map[int]func(dshstream.GoalUpdate)
	nextID    int
}

// newConsoleGoals wraps a goal store. The sequence is anchored at the wall clock
// so it outranks any session-log cursor the console may have seeded a row with
// (see nextSeqLocked).
func newConsoleGoals(store goals.Store) *consoleGoals {
	return &consoleGoals{
		store:     store,
		seq:       time.Now().UnixMilli(),
		observers: map[int]func(dshstream.GoalUpdate){},
	}
}

// Goal implements dshapi.GoalStore. A session with no current goal is the false
// arm, not an error: the reference answers that read with `undefined`.
func (c *consoleGoals) Goal(ctx context.Context, sessionID string) (dshapi.GoalView, bool, error) {
	state, err := c.load(ctx, sessionID)
	if err != nil {
		// A session whose state document does not exist has no goal; that is the
		// false arm, not a failure the console could act on.
		if goals.IsNotFound(err) {
			return dshapi.GoalView{}, false, nil
		}
		return dshapi.GoalView{}, false, err
	}
	if state.Goal == nil {
		return dshapi.GoalView{}, false, nil
	}
	return consoleGoalView(state), true, nil
}

// Create implements dshapi.GoalStore: a fresh revision-one active goal whose id
// this host mints, because the console never chooses one.
func (c *consoleGoals) Create(ctx context.Context, sessionID string, request dshapi.CreateGoalRequest) (dshapi.CreateGoalResult, error) {
	state, err := c.mutate(ctx, sessionID, func(state goals.State) (goals.State, error) {
		next, err := goals.Create(state, goals.CreateOptions{
			ID:            goalIdentifier(time.Now(), state),
			Objective:     request.Objective,
			MaxGoalRounds: request.MaxGoalRounds,
		})
		if err != nil {
			// A create while a goal is still current is the reference's
			// GOAL_ALREADY_EXISTS; the state machine reports the same rule as an
			// invalid transition, which is its own vocabulary.
			if state.Goal != nil && goals.IsCode(err, goals.CodeInvalidTransition) {
				return state, &dshapi.GoalError{
					Code: dshapi.GoalCodeAlreadyExists,
					Message: fmt.Sprintf("goal %s is still current (phase %s); complete or clear it before creating another",
						state.Goal.ID, state.Goal.Phase),
					Details: map[string]any{"goalId": state.Goal.ID},
				}
			}
			return state, consoleGoalError(err)
		}
		return next, nil
	})
	if err != nil {
		return dshapi.CreateGoalResult{}, err
	}
	return dshapi.CreateGoalResult{Ref: consoleGoalRef(state.Goal)}, nil
}

// Edit implements dshapi.GoalStore: one exact revision's objective and/or round
// cap. The compare-and-set check happens before the state machine, which has no
// ref of its own.
func (c *consoleGoals) Edit(ctx context.Context, sessionID string, ref dshapi.GoalRef, request dshapi.EditGoalRequest) (dshapi.GoalView, error) {
	state, err := c.mutate(ctx, sessionID, func(state goals.State) (goals.State, error) {
		if err := consoleGoalRefMatches(state, ref); err != nil {
			return state, err
		}
		options := goals.EditOptions{}
		if request.Objective != nil {
			options.Objective = *request.Objective
		}
		if request.MaxGoalRounds != nil {
			options.MaxGoalRounds = *request.MaxGoalRounds
		}
		next, err := goals.Edit(state, options)
		if err != nil {
			return state, consoleGoalError(err)
		}
		return next, nil
	})
	if err != nil {
		return dshapi.GoalView{}, err
	}
	return consoleGoalView(state), nil
}

// Pause implements dshapi.GoalStore.
func (c *consoleGoals) Pause(ctx context.Context, sessionID string, ref dshapi.GoalRef) (dshapi.GoalView, error) {
	return c.transition(ctx, sessionID, ref, goals.Pause)
}

// Resume implements dshapi.GoalStore.
func (c *consoleGoals) Resume(ctx context.Context, sessionID string, ref dshapi.GoalRef) (dshapi.GoalView, error) {
	return c.transition(ctx, sessionID, ref, goals.Resume)
}

// Complete implements dshapi.GoalStore.
func (c *consoleGoals) Complete(ctx context.Context, sessionID string, ref dshapi.GoalRef) (dshapi.GoalView, error) {
	return c.transition(ctx, sessionID, ref, goals.Complete)
}

// transition is the shared body of the three ref-only lifecycle moves.
func (c *consoleGoals) transition(
	ctx context.Context,
	sessionID string,
	ref dshapi.GoalRef,
	apply func(goals.State, time.Time) (goals.State, error),
) (dshapi.GoalView, error) {
	state, err := c.mutate(ctx, sessionID, func(state goals.State) (goals.State, error) {
		if err := consoleGoalRefMatches(state, ref); err != nil {
			return state, err
		}
		next, err := apply(state, time.Now())
		if err != nil {
			return state, consoleGoalError(err)
		}
		return next, nil
	})
	if err != nil {
		return dshapi.GoalView{}, err
	}
	return consoleGoalView(state), nil
}

// Clear implements dshapi.GoalStore. The state document is removed rather than
// tombstoned, which is a deviation from the reference: upstream retains the
// cleared goal's identity and every identity the session has already used, so a
// pre-clear ref is refused as stale and a reused id as a duplicate, while both are
// GOAL_NOT_FOUND here. The dock cannot observe the difference -- it hides on the
// null cell and offers no mutation without a current goal -- and ADR 0125 records
// the deviation rather than keeping an id table for a client that never sends
// one.
func (c *consoleGoals) Clear(ctx context.Context, sessionID string, ref dshapi.GoalRef) (dshapi.GoalRef, error) {
	c.mu.Lock()
	state, err := c.load(ctx, sessionID)
	if err != nil {
		c.mu.Unlock()
		if goals.IsNotFound(err) {
			return dshapi.GoalRef{}, &dshapi.GoalError{
				Code:    dshapi.GoalCodeNotFound,
				Message: "no goal exists in this session",
				Details: map[string]any{},
			}
		}
		return dshapi.GoalRef{}, err
	}
	if err := consoleGoalRefMatches(state, ref); err != nil {
		c.mu.Unlock()
		return dshapi.GoalRef{}, err
	}
	cleared := consoleGoalRef(state.Goal)
	if err := c.store.Delete(ctx, sessionID); err != nil {
		c.mu.Unlock()
		return dshapi.GoalRef{}, err
	}
	update := c.commitLocked(sessionID, goals.State{})
	observers := c.observersLocked()
	c.mu.Unlock()
	notifyGoal(observers, update)
	return cleared, nil
}

// mutate runs one load-modify-save under this store's lock, so two concurrent
// requests cannot interleave a compare-and-set: the ref one of them read is the
// state the other writes over. The lock is released before observers run, so a
// stream cannot stall a mutation.
func (c *consoleGoals) mutate(ctx context.Context, sessionID string, apply func(goals.State) (goals.State, error)) (goals.State, error) {
	c.mu.Lock()
	state, err := c.load(ctx, sessionID)
	if err != nil {
		if !goals.IsNotFound(err) {
			c.mu.Unlock()
			return goals.State{}, err
		}
		// A session that has never had a goal is the empty state, which is what
		// create needs and what every other mutation then refuses as "no goal".
		state = goals.State{}
	}
	next, err := apply(state)
	if err != nil {
		c.mu.Unlock()
		return goals.State{}, err
	}
	if err := c.store.Save(ctx, sessionID, next); err != nil {
		c.mu.Unlock()
		return goals.State{}, err
	}
	update := c.commitLocked(sessionID, next)
	observers := c.observersLocked()
	c.mu.Unlock()
	notifyGoal(observers, update)
	return next, nil
}

// commitLocked numbers one committed mutation and builds the update both live
// carriers send. Callers hold the lock.
func (c *consoleGoals) commitLocked(sessionID string, state goals.State) dshstream.GoalUpdate {
	return dshstream.GoalUpdate{
		SessionID:  sessionID,
		Projection: consoleGoalProjection(state),
		Seq:        c.nextSeqLocked(),
		Activation: consoleGoalActivation(state),
	}
}

// nextSeqLocked advances the sequence for one commit. It is a wall-clock-anchored
// monotone counter, not a session-log watermark: the harness records no goal
// event, but the console's history seed installs the projections block at the
// session cursor's watermark and discards a frame numbered at or below it, so a
// goal frame has to be numbered above every cursor a session can reach. The
// clock supplies that margin and survives a restart, where a small counter would
// restart below the cursors already served. Callers hold the lock.
func (c *consoleGoals) nextSeqLocked() int64 {
	now := time.Now().UnixMilli()
	if now > c.seq {
		c.seq = now
	} else {
		c.seq++
	}
	return c.seq
}

// observersLocked copies the subscriber set so the callbacks run without the
// lock. Callers hold the lock.
func (c *consoleGoals) observersLocked() []func(dshstream.GoalUpdate) {
	observers := make([]func(dshstream.GoalUpdate), 0, len(c.observers))
	for _, observe := range c.observers {
		observers = append(observers, observe)
	}
	return observers
}

// notifyGoal delivers one update to every subscribed carrier. Callers must not
// hold the store's lock: a subscriber writes to a socket.
func notifyGoal(observers []func(dshstream.GoalUpdate), update dshstream.GoalUpdate) {
	for _, observe := range observers {
		observe(update)
	}
}

// Updates delivers later goal mutations until the returned function is called.
// The control stream and the forwarded-event stream both subscribe here.
func (c *consoleGoals) Updates(observe func(dshstream.GoalUpdate)) (unsubscribe func()) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.observers[id] = observe
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.observers, id)
		c.mu.Unlock()
	}
}

// Projection reports one session's goal cell for the follow snapshot: the
// projection, or nil for the null arm a session with no goal serves. A read that
// fails is reported as no goal and logged, because a snapshot cannot carry an
// error and inventing a goal would be worse than an empty dock.
func (c *consoleGoals) Projection(sessionID string) *dshstream.GoalProjection {
	state, err := c.load(context.Background(), sessionID)
	if err != nil {
		if !goals.IsNotFound(err) {
			slog.Warn("console goal projection is unavailable",
				"session", sessionID, "error", err)
		}
		return nil
	}
	return consoleGoalProjection(state)
}

// load reads one session's goal state. A missing document is ErrNotFound, which
// callers treat as "no goal" rather than as a failure.
func (c *consoleGoals) load(ctx context.Context, sessionID string) (goals.State, error) {
	return c.store.Load(ctx, sessionID)
}

// consoleGoalRefMatches is the compare-and-set check the state machine does not
// take: a mutation names the exact id and revision it read, and a mismatch is
// refused rather than applied to whatever is current.
func consoleGoalRefMatches(state goals.State, ref dshapi.GoalRef) error {
	if state.Goal == nil {
		return &dshapi.GoalError{
			Code:    dshapi.GoalCodeNotFound,
			Message: "no goal exists in this session",
			Details: map[string]any{},
		}
	}
	details := map[string]any{"goalId": state.Goal.ID, "revision": state.Goal.Revision}
	if state.Goal.ID != ref.ID {
		return &dshapi.GoalError{
			Code:    dshapi.GoalCodeNotFound,
			Message: fmt.Sprintf("goal %q is not the current goal %q", ref.ID, state.Goal.ID),
			Details: details,
		}
	}
	if state.Goal.Revision != ref.Revision {
		return &dshapi.GoalError{
			Code:    dshapi.GoalCodeStaleRevision,
			Message: fmt.Sprintf("goal %s is at revision %d, not %d", ref.ID, state.Goal.Revision, ref.Revision),
			Details: details,
		}
	}
	return nil
}

// consoleGoalError maps one lifecycle rejection onto the reference's own code.
// The mapping is total: a code this host does not recognize becomes the
// transition refusal, which is the family every lifecycle rule belongs to.
func consoleGoalError(err error) error {
	var goalErr *goals.Error
	if !errors.As(err, &goalErr) {
		return err
	}
	code := dshapi.GoalCodeInvalidTransition
	switch goalErr.Code {
	case goals.CodeRevisionMismatch:
		code = dshapi.GoalCodeStaleRevision
	case goals.CodeDuplicateGoal:
		code = dshapi.GoalCodeAlreadyExists
	case goals.CodeInvalidTransition, goals.CodeRoundBudget:
		// The reference has one transition code, and these are its transitions:
		// an illegal phase move, a resume whose round budget is spent, or an edit
		// that lowers the cap below the rounds already started. The last is
		// stricter than the reference, whose edit accepts any positive cap -- the
		// refusal is the framework's and its message says which numbers collide.
		code = dshapi.GoalCodeInvalidTransition
	}
	return &dshapi.GoalError{Code: code, Message: goalErr.Message}
}

// consoleGoalRef projects a goal onto the compare-and-set identity the console
// sends back.
func consoleGoalRef(goal *goals.Goal) dshapi.GoalRef {
	return dshapi.GoalRef{ID: goal.ID, Revision: goal.Revision}
}

// consoleGoalActivation reports the process-local continuation state the
// reference derives in a live process. This host derives it from the durable
// state instead: a goal is armed while it is active and its round budget is not
// exhausted, which is the same answer for every reader and survives a restart.
func consoleGoalActivation(state goals.State) string {
	if state.Armed() {
		return "armed"
	}
	return "disarmed"
}

// consoleGoalView projects the framework's state onto the console's GoalView.
func consoleGoalView(state goals.State) dshapi.GoalView {
	view := dshapi.GoalView{
		RoundsStarted: state.RoundsStarted,
		Activation:    consoleGoalActivation(state),
	}
	goal := state.Goal
	if goal == nil {
		return view
	}
	view.ID = goal.ID
	view.Revision = goal.Revision
	view.Objective = goal.Objective
	view.Phase = string(goal.Phase)
	view.MaxGoalRounds = goal.MaxGoalRounds
	view.CreatedAt = consoleEpochMilli(state.CreatedAt)
	view.UpdatedAt = consoleEpochMilli(state.UpdatedAt)
	if goal.BlockedReason != nil {
		view.BlockedReason = &dshapi.GoalBlockedReason{
			Code:    goal.BlockedReason.Code,
			Message: goal.BlockedReason.Message,
		}
	}
	return view
}

// consoleGoalProjection projects the framework's state onto the `goal`
// projection cell.
func consoleGoalProjection(state goals.State) *dshstream.GoalProjection {
	if state.Goal == nil {
		return nil
	}
	goal := state.Goal
	projection := &dshstream.GoalProjection{
		Goal: dshstream.GoalSnapshot{
			ID:            goal.ID,
			Revision:      goal.Revision,
			Objective:     goal.Objective,
			Phase:         string(goal.Phase),
			MaxGoalRounds: goal.MaxGoalRounds,
		},
		RoundsStarted: state.RoundsStarted,
		CreatedAt:     consoleEpochMilli(state.CreatedAt),
		UpdatedAt:     consoleEpochMilli(state.UpdatedAt),
	}
	if goal.BlockedReason != nil {
		projection.Goal.BlockedReason = &dshstream.GoalBlockedReason{
			Code:    goal.BlockedReason.Code,
			Message: goal.BlockedReason.Message,
		}
	}
	return projection
}

// consoleEpochMilli renders one timestamp as epoch milliseconds, the unit the
// client's GoalView carries. An unset timestamp reports 0 rather than the year
// 1 wrapped as a negative number.
func consoleEpochMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

// goalIdentifier mints a session-unique goal id. The console never chooses one,
// so the host does, and the id is what every following mutation compare-and-sets
// against; the collision check mirrors the goal tools' own so a create and a tool
// call can never mint the same identity.
func goalIdentifier(now time.Time, state goals.State) string {
	base := fmt.Sprintf("goal-%d", now.UnixNano())
	if !goalIDSeen(state, base) {
		return base
	}
	for suffix := 1; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !goalIDSeen(state, candidate) {
			return candidate
		}
	}
}

// goalIDSeen reports whether a session has already used one goal identity. The
// framework keeps every identity a session has created, so a second create in the
// same nanosecond is the only collision here -- and the reason the base is tested
// before any suffix is added, because a suffix on a free identity is a name
// nothing asked for.
func goalIDSeen(state goals.State, candidate string) bool {
	for _, seen := range state.SeenGoalIDs {
		if seen == candidate {
			return true
		}
	}
	return false
}

// goalStorePath is the directory the console's goal state lives in: the same one
// the command line and the goal tools write, so one session's goal is the same
// goal whichever surface created it.
func goalStorePath(checkpointDir string) string {
	return filepath.Join(checkpointDir, "goals")
}
