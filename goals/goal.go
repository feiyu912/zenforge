// Package goals implements a persisted same-session objective: the goal
// snapshot, its revision-checked lifecycle transitions, the round budget,
// and the fresh-agent Ralph loop that iterates toward one immutable
// objective. The lifecycle rules are ported from DSH's goal domain
// (dsh-goal): a create must be a fresh active revision-one goal with zero
// rounds, every later operation must advance the revision by exactly one
// while preserving the counters, and only specific phase transitions are
// legal.
package goals

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Phase is a goal's lifecycle phase.
type Phase string

// Phases mirroring the reference.
const (
	PhaseActive   Phase = "active"
	PhasePaused   Phase = "paused"
	PhaseBlocked  Phase = "blocked"
	PhaseComplete Phase = "complete"
)

// DefaultMaxGoalRounds is the round budget used when a caller does not
// choose one.
const DefaultMaxGoalRounds = 8

// MinBlockedRounds is the reference's minimum persistence before a goal
// may be reported blocked: the same blocking condition must survive this
// many consecutive rounds.
const MinBlockedRounds = 3

// BlockedReason explains why a goal cannot continue. Code is
// lower-kebab-case so a host can route on it; Message is non-empty and
// trimmed.
type BlockedReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var kebabCode = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// Validate checks the reason's shape.
func (r BlockedReason) Validate() error {
	if !kebabCode.MatchString(r.Code) {
		return fmt.Errorf("goal blockedReason.code must be lower-kebab-case")
	}
	if strings.TrimSpace(r.Message) == "" {
		return fmt.Errorf("goal blockedReason.message must be non-empty")
	}
	if r.Message != strings.TrimSpace(r.Message) {
		return fmt.Errorf("goal blockedReason.message must be normalized")
	}
	return nil
}

// Goal is one revision of a persisted objective.
type Goal struct {
	ID            string         `json:"id"`
	Revision      int            `json:"revision"`
	Objective     string         `json:"objective"`
	MaxGoalRounds int            `json:"maxGoalRounds"`
	Phase         Phase          `json:"phase"`
	BlockedReason *BlockedReason `json:"blockedReason,omitempty"`
}

// Validate checks the snapshot's shape.
func (g Goal) Validate() error {
	if strings.TrimSpace(g.ID) == "" {
		return fmt.Errorf("goal id is required")
	}
	if g.Revision < 1 {
		return fmt.Errorf("goal revision must be at least 1")
	}
	if strings.TrimSpace(g.Objective) == "" {
		return fmt.Errorf("goal objective is required")
	}
	if g.MaxGoalRounds < 1 {
		return fmt.Errorf("goal maxGoalRounds must be positive")
	}
	switch g.Phase {
	case PhaseActive, PhasePaused, PhaseBlocked, PhaseComplete:
	default:
		return fmt.Errorf("goal phase %q is invalid", g.Phase)
	}
	if g.Phase == PhaseBlocked {
		if g.BlockedReason == nil {
			return fmt.Errorf("a blocked goal requires a blockedReason")
		}
		if err := g.BlockedReason.Validate(); err != nil {
			return err
		}
		return nil
	}
	if g.BlockedReason != nil {
		return fmt.Errorf("goal blockedReason is only valid while blocked")
	}
	return nil
}

// State is the durable goal projection for one session: the current goal,
// the admitted round count, the timestamps, and every goal id the session
// has ever used (so an id cannot be reused).
type State struct {
	Goal          *Goal     `json:"goal,omitempty"`
	RoundsStarted int       `json:"roundsStarted"`
	CreatedAt     time.Time `json:"createdAt,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt,omitempty"`
	SeenGoalIDs   []string  `json:"seenGoalIds,omitempty"`
	// BlockedStreak counts the consecutive rounds in which the same
	// blocking condition was reported, and BlockedRound is the round that
	// last advanced the streak. Together they enforce the rule that a goal
	// may only be reported blocked after MinBlockedRounds rounds.
	BlockedStreak int    `json:"blockedStreak,omitempty"`
	BlockedRound  int    `json:"blockedRound,omitempty"`
	LastBlocker   string `json:"lastBlocker,omitempty"`
}

// Revision returns the current goal's revision, or 0 when there is none.
func (s State) Revision() int {
	if s.Goal == nil {
		return 0
	}
	return s.Goal.Revision
}

// Armed reports whether another round may start now.
func (s State) Armed() bool {
	return s.Goal != nil && s.Goal.Phase == PhaseActive && s.RoundsStarted < s.Goal.MaxGoalRounds
}

// Error is a lifecycle rejection with a stable code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Stable error codes.
const (
	CodeInvalidTransition = "invalid-transition"
	CodeRoundBudget       = "round-budget-exhausted"
	CodeNotBlocked        = "not-blocked"
	CodeBlockedRounds     = "blocked-rounds-remaining"
	CodeRevisionMismatch  = "revision-mismatch"
	CodeDuplicateGoal     = "duplicate-goal-id"
	CodeInvalidDefinition = "invalid-definition"
)

func lifecycleError(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsCode reports whether err is a goal lifecycle error with the given code.
func IsCode(err error, code string) bool {
	var goalErr *Error
	return errors.As(err, &goalErr) && goalErr.Code == code
}

// CreateOptions describes a new goal.
type CreateOptions struct {
	ID            string
	Objective     string
	MaxGoalRounds int
	Now           time.Time
}

// Create starts a fresh revision-one active goal with zero rounds. The
// session may only do this when it has no goal or the previous goal is
// complete, and an id may never be reused.
func Create(state State, options CreateOptions) (State, error) {
	if state.Goal != nil && state.Goal.Phase != PhaseComplete {
		return state, lifecycleError(CodeInvalidTransition, "goal create requires the current goal to be complete or absent")
	}
	for _, seen := range state.SeenGoalIDs {
		if seen == options.ID {
			return state, lifecycleError(CodeDuplicateGoal, "goal id %q was already used in this session", options.ID)
		}
	}
	maxRounds := options.MaxGoalRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxGoalRounds
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	goal := &Goal{
		ID:            options.ID,
		Revision:      1,
		Objective:     strings.TrimSpace(options.Objective),
		MaxGoalRounds: maxRounds,
		Phase:         PhaseActive,
	}
	if err := goal.Validate(); err != nil {
		return state, lifecycleError(CodeInvalidDefinition, "%v", err)
	}
	next := state
	next.Goal = goal
	next.RoundsStarted = 0
	next.BlockedStreak = 0
	next.BlockedRound = 0
	next.LastBlocker = ""
	next.CreatedAt = now
	next.UpdatedAt = now
	next.SeenGoalIDs = append(append([]string(nil), state.SeenGoalIDs...), goal.ID)
	return next, nil
}

// EditOptions replaces the editable definition fields.
type EditOptions struct {
	Objective     string
	MaxGoalRounds int
	Now           time.Time
}

// Edit changes the objective or the round budget without touching the
// phase, the blocked reason, or the counters.
func Edit(state State, options EditOptions) (State, error) {
	next, err := nextRevision(state, "edit")
	if err != nil {
		return state, err
	}
	if objective := strings.TrimSpace(options.Objective); objective != "" {
		next.Goal.Objective = objective
	}
	if options.MaxGoalRounds > 0 {
		next.Goal.MaxGoalRounds = options.MaxGoalRounds
	}
	if next.Goal.MaxGoalRounds < next.RoundsStarted {
		return state, lifecycleError(CodeInvalidTransition, "goal maxGoalRounds %d is below the %d rounds already started", next.Goal.MaxGoalRounds, next.RoundsStarted)
	}
	if err := next.Goal.Validate(); err != nil {
		return state, lifecycleError(CodeInvalidDefinition, "%v", err)
	}
	next.UpdatedAt = stamp(options.Now, state.UpdatedAt)
	return next, nil
}

// Pause moves an active goal to paused.
func Pause(state State, now time.Time) (State, error) {
	if state.Goal == nil || state.Goal.Phase != PhaseActive {
		return state, lifecycleError(CodeInvalidTransition, "goal pause requires an active goal")
	}
	next, err := nextRevision(state, "pause")
	if err != nil {
		return state, err
	}
	next.Goal.Phase = PhasePaused
	next.UpdatedAt = stamp(now, state.UpdatedAt)
	return next, nil
}

// Resume moves a paused or blocked goal back to active, provided the
// round budget is not exhausted.
func Resume(state State, now time.Time) (State, error) {
	if state.Goal == nil {
		return state, lifecycleError(CodeInvalidTransition, "goal resume requires a goal")
	}
	switch state.Goal.Phase {
	case PhaseActive, PhasePaused, PhaseBlocked:
	default:
		return state, lifecycleError(CodeInvalidTransition, "goal resume requires an active, paused, or blocked goal")
	}
	if state.RoundsStarted >= state.Goal.MaxGoalRounds {
		return state, lifecycleError(CodeRoundBudget, "goal resume rejected: the round budget of %d is exhausted", state.Goal.MaxGoalRounds)
	}
	next, err := nextRevision(state, "resume")
	if err != nil {
		return state, err
	}
	next.Goal.Phase = PhaseActive
	next.Goal.BlockedReason = nil
	next.BlockedStreak = 0
	next.BlockedRound = 0
	next.LastBlocker = ""
	next.UpdatedAt = stamp(now, state.UpdatedAt)
	return next, nil
}

// Complete marks a goal complete from any non-complete phase.
func Complete(state State, now time.Time) (State, error) {
	if state.Goal == nil {
		return state, lifecycleError(CodeInvalidTransition, "goal complete requires a goal")
	}
	if state.Goal.Phase == PhaseComplete {
		return state, lifecycleError(CodeInvalidTransition, "goal is already complete")
	}
	next, err := nextRevision(state, "complete")
	if err != nil {
		return state, err
	}
	next.Goal.Phase = PhaseComplete
	next.Goal.BlockedReason = nil
	next.UpdatedAt = stamp(now, state.UpdatedAt)
	return next, nil
}

// Block marks an active goal blocked, but only after the same blocking
// condition has been reported in at least MinBlockedRounds consecutive
// rounds. Repeating the same condition within one round does not count
// twice, so a model cannot manufacture the evidence by calling the tool
// repeatedly. Rounds that report a different condition restart the count.
func Block(state State, reason BlockedReason, now time.Time) (State, error) {
	if state.Goal == nil || state.Goal.Phase != PhaseActive {
		return state, lifecycleError(CodeInvalidTransition, "goal block requires an active goal")
	}
	if err := reason.Validate(); err != nil {
		return state, lifecycleError(CodeInvalidDefinition, "%v", err)
	}
	streak := nextBlockStreak(state, reason.Message)
	if streak < MinBlockedRounds {
		// Record the observation so the next round continues the count.
		pending := state
		pending.BlockedStreak = streak
		pending.LastBlocker = reason.Message
		if pending.RoundsStarted != state.BlockedRound {
			pending.BlockedRound = state.RoundsStarted
		}
		pending.UpdatedAt = stamp(now, state.UpdatedAt)
		return pending, lifecycleError(CodeBlockedRounds,
			"goal block rejected: the same blocking condition has persisted for %d of the %d consecutive rounds required", streak, MinBlockedRounds)
	}
	next, err := nextRevision(state, "block")
	if err != nil {
		return state, err
	}
	next.Goal.Phase = PhaseBlocked
	next.Goal.BlockedReason = &reason
	next.BlockedStreak = streak
	next.LastBlocker = reason.Message
	next.UpdatedAt = stamp(now, state.UpdatedAt)
	return next, nil
}

// nextBlockStreak advances the consecutive-blocked counter for one
// observation of the same condition, counting at most once per round.
func nextBlockStreak(state State, message string) int {
	if message != state.LastBlocker {
		return 1
	}
	if state.RoundsStarted == state.BlockedRound {
		return state.BlockedStreak
	}
	return state.BlockedStreak + 1
}

// AdmitRound records the start of the next round of the active goal. The
// round number must be exactly RoundsStarted+1 and within the budget,
// mirroring the reference's round-admission check.
func AdmitRound(state State, round int, now time.Time) (State, error) {
	if state.Goal == nil || state.Goal.Phase != PhaseActive {
		return state, lifecycleError(CodeInvalidTransition, "a goal round requires an active goal")
	}
	if round != state.RoundsStarted+1 {
		return state, lifecycleError(CodeRevisionMismatch, "goal round %d is not the next admitted round (%d)", round, state.RoundsStarted+1)
	}
	if round > state.Goal.MaxGoalRounds {
		return state, lifecycleError(CodeRoundBudget, "goal round %d exceeds the round budget of %d", round, state.Goal.MaxGoalRounds)
	}
	next := state
	next.RoundsStarted = round
	next.UpdatedAt = stamp(now, state.UpdatedAt)
	return next, nil
}

// nextRevision clones the state and advances the goal's revision by one,
// preserving the counters and the creation time exactly as the reference
// fold requires.
func nextRevision(state State, operation string) (State, error) {
	if state.Goal == nil {
		return state, lifecycleError(CodeInvalidTransition, "goal %s requires a goal", operation)
	}
	if state.Goal.Revision < 1 {
		return state, lifecycleError(CodeRevisionMismatch, "goal %s requires a valid revision", operation)
	}
	if err := state.Goal.Validate(); err != nil {
		return state, lifecycleError(CodeInvalidDefinition, "%v", err)
	}
	next := state
	cloned := *state.Goal
	cloned.Revision = state.Goal.Revision + 1
	next.Goal = &cloned
	if reason := cloned.BlockedReason; reason != nil {
		copied := *reason
		next.Goal.BlockedReason = &copied
	}
	next.SeenGoalIDs = append([]string(nil), state.SeenGoalIDs...)
	return next, nil
}

// stamp returns a non-decreasing update time.
func stamp(now, previous time.Time) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.Before(previous) {
		return previous
	}
	return now
}
