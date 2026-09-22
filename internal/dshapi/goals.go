package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// POST /api/goals/{get,create,edit,pause,resume,complete,clear}: the seven
// methods the console's goal dock and the host-side goal command use.
//
// The envelopes are the goal domain's own, read from the vendored console
// bundle rather than from the published typings: the client's generated remote
// map declares the wire names (`agentId`, `ref`, `request`) and the strict
// schemas each result is validated against
// (webui/dsh/plugins/api/remotes/client.js, `@deepseek-ai/dsh-goal#goals/*`).
// GoalEnvelopesMatchTheVendoredConsole in goals_test.go re-derives those shapes
// from the same bytes, so a console upgrade that widens or narrows a goal
// envelope fails a test instead of silently breaking the dock.
//
// Two asymmetries are upstream's and are kept deliberately:
//
//   - `get` answers the no-goal case with **no value at all**, not null. The
//     client reads `result.value` and tests `goal === undefined`; a JSON null
//     would reach the dock as a value and it would read `.id` off it.
//   - `create` takes `{objective, maxGoalRounds?}` and answers `{ref}`, while
//     every mutation takes a compare-and-set `{id, revision}` and answers the
//     whole GoalView. Goal ids are the host's to mint: the console never
//     chooses one.
//
// The lifecycle rules themselves are not re-implemented here. They live in the
// framework's `goals` package (goals.Create/Edit/Pause/Resume/Complete/Delete),
// which the driver, the CLI and the goal tools already share; this file is the
// adapter that translates between the console's envelopes and that state
// machine, including the reference's own error codes.

// The reference's goal error codes (GoalErrorCode). They are served verbatim as
// the result envelope's code, which is what the console renders next to the
// message: the codes are part of the goal domain's contract rather than this
// adapter's vocabulary.
const (
	GoalCodeAgentNotLive      = "GOAL_AGENT_NOT_LIVE"
	GoalCodeNotFound          = "GOAL_NOT_FOUND"
	GoalCodeAlreadyExists     = "GOAL_ALREADY_EXISTS"
	GoalCodeStaleRevision     = "GOAL_STALE_REVISION"
	GoalCodeInvalidObjective  = "GOAL_INVALID_OBJECTIVE"
	GoalCodeInvalidMaxRounds  = "GOAL_INVALID_MAX_ROUNDS"
	GoalCodeInvalidEdit       = "GOAL_INVALID_EDIT"
	GoalCodeInvalidTransition = "GOAL_INVALID_TRANSITION"
)

// GoalRef is the compare-and-set identity of one exact goal revision
// (@deepseek-ai/dsh-goal/client#GoalRef). Revision is positive and every
// durable mutation increments it.
type GoalRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

// GoalBlockedReason is GoalBlockReason: a stable lower-kebab-case code plus a
// non-empty explanation.
type GoalBlockedReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// GoalView is the current goal as a read or a mutation answers it
// (@deepseek-ai/dsh-goal/client#GoalView): the durable snapshot plus the replay
// counters and the process-local activation this host derives from them.
type GoalView struct {
	ID            string             `json:"id"`
	Revision      int                `json:"revision"`
	Objective     string             `json:"objective"`
	Phase         string             `json:"phase"`
	BlockedReason *GoalBlockedReason `json:"blockedReason,omitempty"`
	MaxGoalRounds int                `json:"maxGoalRounds"`
	RoundsStarted int                `json:"roundsStarted"`
	CreatedAt     int64              `json:"createdAt"`
	UpdatedAt     int64              `json:"updatedAt"`
	Activation    string             `json:"activation"`
}

// CreateGoalRequest is the input of `goals/create`. The omitted round cap is
// resolved by the host's own configuration, exactly as the reference resolves
// it from the service configuration.
type CreateGoalRequest struct {
	Objective     string `json:"objective"`
	MaxGoalRounds int    `json:"maxGoalRounds,omitempty"`
}

// CreateGoalResult is the wire-safe acknowledgement of a created goal. The
// whole view is deliberately not returned: the projection is how the console
// learns the goal, and the ref is what a following mutation needs.
type CreateGoalResult struct {
	Ref GoalRef `json:"ref"`
}

// EditGoalRequest is the fields an edit may change; at least one must be
// present.
type EditGoalRequest struct {
	Objective     *string `json:"objective,omitempty"`
	MaxGoalRounds *int    `json:"maxGoalRounds,omitempty"`
}

// GoalError is a refused goal operation carrying one of the reference's codes.
// The store returns it; the handler turns it into a result envelope whose code
// is the goal domain's own.
type GoalError struct {
	Code    string
	Message string
	Details map[string]any
}

// Error implements error.
func (e *GoalError) Error() string { return e.Message }

// GoalStore is where a session's goal lives: the state machine's durable input
// plus the compare-and-set mutations the console performs. It is injected like
// the other seams, because only the serve command knows where the state
// directory is and how the transport is told about a commit.
//
// Every method returns a *GoalError for a refusal the console can route on;
// anything else is an internal failure. A session with no current goal is not an
// error for Goal: it is the `false` arm, which the read answers with no value.
type GoalStore interface {
	// Goal reports the session's current goal. ok is false when the session has
	// none, which the read answers by omitting the value.
	Goal(ctx context.Context, sessionID string) (GoalView, bool, error)
	// Create admits a fresh goal for the session.
	Create(ctx context.Context, sessionID string, request CreateGoalRequest) (CreateGoalResult, error)
	// Edit changes the objective and/or the round cap of one exact revision.
	Edit(ctx context.Context, sessionID string, ref GoalRef, request EditGoalRequest) (GoalView, error)
	// Pause, Resume and Complete move one exact revision through the lifecycle.
	Pause(ctx context.Context, sessionID string, ref GoalRef) (GoalView, error)
	Resume(ctx context.Context, sessionID string, ref GoalRef) (GoalView, error)
	Complete(ctx context.Context, sessionID string, ref GoalRef) (GoalView, error)
	// Clear removes the current goal, answering the ref it cleared.
	Clear(ctx context.Context, sessionID string, ref GoalRef) (GoalRef, error)
}

// SetGoals installs the goal store the goals/* methods answer through. Nil
// leaves the namespace unserved, which is the honest answer for a host with no
// goal state directory.
func (h *Handler) SetGoals(store GoalStore) {
	h.goalsMu.Lock()
	h.goals = store
	h.goalsMu.Unlock()
}

func (h *Handler) goalStore() GoalStore {
	h.goalsMu.RLock()
	defer h.goalsMu.RUnlock()
	return h.goals
}

// goalsDependencyMissing is the honest answer when no store is installed.
func goalsDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: this host has no goal store; the serve command must install one with Handler.SetGoals",
		map[string]any{"dependency": "GoalStore"})
}

// goalFailure turns a store refusal into the result envelope the console reads.
// A *GoalError keeps the reference's own code; anything else is an internal
// failure, because an unrecognized error is not a fact about the goal.
func goalFailure(method, sessionID string, err error) *methodError {
	var goalErr *GoalError
	if errors.As(err, &goalErr) {
		details := goalErr.Details
		if details == nil {
			details = map[string]any{}
		}
		if _, named := details["sessionId"]; !named && sessionID != "" {
			details["sessionId"] = sessionID
		}
		return fail(goalErr.Code, fmt.Sprintf("%s: %s", method, goalErr.Message), details)
	}
	return fail(codeInternal, fmt.Sprintf("%s: %s", method, err.Error()), map[string]any{"sessionId": sessionID})
}

// goalSession reads the `agentId` every goals method is scoped by. The wire name
// is the reference's own (`scope: {context: "agent", wire: "agentId"}`), and the
// value is a session id here, because a session is this host's agent.
//
// A session this host does not serve is refused with the reference's
// GOAL_AGENT_NOT_LIVE rather than answered as an empty goal: the difference
// between "no goal" and "no such session" is one the operator can act on.
func (h *Handler) goalSession(ctx context.Context, method string, args map[string]json.RawMessage) (string, *methodError) {
	sessionID, present, failure := stringArg(args, "agentId")
	if failure != nil {
		return "", failure
	}
	if !present {
		return "", argumentRequired("agentId")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", argumentRequired("agentId")
	}
	if err := validateSessionID(sessionID); err != nil {
		return "", fail(codeArgumentsInvalid, err.Error(), map[string]any{"argument": "agentId"})
	}
	if !h.sessionKnown(ctx, sessionID) {
		return "", fail(GoalCodeAgentNotLive,
			fmt.Sprintf("%s: session %q is not one this host serves", method, sessionID),
			map[string]any{"sessionId": sessionID})
	}
	return sessionID, nil
}

// goalRefArg reads the compare-and-set `ref` every mutation but create takes.
// Only the two fields the reference declares are read; a third field is refused
// rather than ignored, because a mistyped ref is a mutation aimed at a goal the
// caller did not mean.
func goalRefArg(args map[string]json.RawMessage) (GoalRef, *methodError) {
	raw, present := args["ref"]
	if !present {
		return GoalRef{}, argumentRequired("ref")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return GoalRef{}, fail(codeArgumentsInvalid, `argument "ref" must be an object`,
			map[string]any{"argument": "ref"})
	}
	if failure := rejectUnknownArguments(fields, "id", "revision"); failure != nil {
		return GoalRef{}, failure
	}
	id, _, failure := stringArg(fields, "id")
	if failure != nil {
		return GoalRef{}, failure
	}
	revision, hasRevision, failure := intArg(fields, "revision")
	if failure != nil {
		return GoalRef{}, failure
	}
	if strings.TrimSpace(id) == "" {
		return GoalRef{}, argumentRequired("ref.id")
	}
	if !hasRevision {
		return GoalRef{}, argumentRequired("ref.revision")
	}
	if revision < 1 {
		return GoalRef{}, fail(codeArgumentsInvalid, "ref.revision must be at least 1",
			map[string]any{"argument": "ref.revision"})
	}
	return GoalRef{ID: strings.TrimSpace(id), Revision: int(revision)}, nil
}

// goalObjectiveArg reads an objective and reports whether it was present at all,
// so edit can tell "change nothing here" from "clear this field".
func goalObjectiveArg(args map[string]json.RawMessage) (string, bool, *methodError) {
	value, present, failure := stringArg(args, "objective")
	if failure != nil {
		return "", false, failure
	}
	if !present {
		return "", false, nil
	}
	return strings.TrimSpace(value), true, nil
}

// goalMaxRoundsArg reads a round cap and reports whether it was present.
func goalMaxRoundsArg(args map[string]json.RawMessage) (int, bool, *methodError) {
	value, present, failure := intArg(args, "maxGoalRounds")
	if failure != nil {
		return 0, false, failure
	}
	if !present {
		return 0, false, nil
	}
	if value < 1 {
		// The reference's own resolveMaxGoalRounds throws GOAL_INVALID_MAX_ROUNDS
		// for a cap that is not a positive safe integer, and the dock renders the
		// code it is given, so the goal domain's code is served rather than the
		// gateway's generic one.
		return 0, false, fail(GoalCodeInvalidMaxRounds, "maxGoalRounds must be a positive integer",
			map[string]any{"argument": "maxGoalRounds"})
	}
	return int(value), true, nil
}

// goalsGet answers POST /api/goals/get.
//
// A session with no current goal answers with no value at all. The client tests
// `goal === undefined` and then treats the projection as the authority, so an
// explicit null would be read as a goal object.
func (h *Handler) goalsGet(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "agentId"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.goalSession(ctx, "goals/get", args)
	if failure != nil {
		return nil, failure
	}
	store := h.goalStore()
	if store == nil {
		return nil, goalsDependencyMissing("goals/get")
	}
	view, ok, err := store.Goal(ctx, sessionID)
	if err != nil {
		return nil, goalFailure("goals/get", sessionID, err)
	}
	if !ok {
		return nil, nil
	}
	return view, nil
}

// goalsCreate answers POST /api/goals/create.
func (h *Handler) goalsCreate(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "agentId", "objective", "maxGoalRounds"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.goalSession(ctx, "goals/create", args)
	if failure != nil {
		return nil, failure
	}
	objective, present, failure := goalObjectiveArg(args)
	if failure != nil {
		return nil, failure
	}
	if !present {
		return nil, argumentRequired("objective")
	}
	if objective == "" {
		return nil, fail(GoalCodeInvalidObjective, "goals/create: objective must be non-empty",
			map[string]any{"sessionId": sessionID, "argument": "objective"})
	}
	maxRounds, _, failure := goalMaxRoundsArg(args)
	if failure != nil {
		return nil, failure
	}
	store := h.goalStore()
	if store == nil {
		return nil, goalsDependencyMissing("goals/create")
	}
	created, err := store.Create(ctx, sessionID, CreateGoalRequest{Objective: objective, MaxGoalRounds: maxRounds})
	if err != nil {
		return nil, goalFailure("goals/create", sessionID, err)
	}
	return created, nil
}

// goalsEdit answers POST /api/goals/edit. At least one editable field must be
// present; an empty edit is the reference's GOAL_INVALID_EDIT rather than a
// no-op that answers a successful view.
func (h *Handler) goalsEdit(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "agentId", "ref", "objective", "maxGoalRounds"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.goalSession(ctx, "goals/edit", args)
	if failure != nil {
		return nil, failure
	}
	ref, failure := goalRefArg(args)
	if failure != nil {
		return nil, failure
	}
	objective, hasObjective, failure := goalObjectiveArg(args)
	if failure != nil {
		return nil, failure
	}
	if hasObjective && objective == "" {
		return nil, fail(GoalCodeInvalidObjective, "goals/edit: objective must be non-empty",
			map[string]any{"sessionId": sessionID, "argument": "objective"})
	}
	maxRounds, hasMaxRounds, failure := goalMaxRoundsArg(args)
	if failure != nil {
		return nil, failure
	}
	if !hasObjective && !hasMaxRounds {
		return nil, fail(GoalCodeInvalidEdit, "goals/edit: an edit must change the objective or the round cap",
			map[string]any{"sessionId": sessionID})
	}
	request := EditGoalRequest{}
	if hasObjective {
		request.Objective = &objective
	}
	if hasMaxRounds {
		request.MaxGoalRounds = &maxRounds
	}
	store := h.goalStore()
	if store == nil {
		return nil, goalsDependencyMissing("goals/edit")
	}
	view, err := store.Edit(ctx, sessionID, ref, request)
	if err != nil {
		return nil, goalFailure("goals/edit", sessionID, err)
	}
	return view, nil
}

// goalsPause, goalsResume and goalsComplete share one shape: a session, a
// compare-and-set ref, and the whole resulting view. They are separate methods
// rather than one with an action because that is how the console calls them.
func (h *Handler) goalsPause(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.goalsTransition(ctx, "goals/pause", args, func(store GoalStore, sessionID string, ref GoalRef) (GoalView, error) {
		return store.Pause(ctx, sessionID, ref)
	})
}

func (h *Handler) goalsResume(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.goalsTransition(ctx, "goals/resume", args, func(store GoalStore, sessionID string, ref GoalRef) (GoalView, error) {
		return store.Resume(ctx, sessionID, ref)
	})
}

func (h *Handler) goalsComplete(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.goalsTransition(ctx, "goals/complete", args, func(store GoalStore, sessionID string, ref GoalRef) (GoalView, error) {
		return store.Complete(ctx, sessionID, ref)
	})
}

// goalsTransition is the shared body of the three ref-only transitions.
func (h *Handler) goalsTransition(
	ctx context.Context,
	method string,
	args map[string]json.RawMessage,
	apply func(GoalStore, string, GoalRef) (GoalView, error),
) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "agentId", "ref"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.goalSession(ctx, method, args)
	if failure != nil {
		return nil, failure
	}
	ref, failure := goalRefArg(args)
	if failure != nil {
		return nil, failure
	}
	store := h.goalStore()
	if store == nil {
		return nil, goalsDependencyMissing(method)
	}
	view, err := apply(store, sessionID, ref)
	if err != nil {
		return nil, goalFailure(method, sessionID, err)
	}
	return view, nil
}

// goalsClear answers POST /api/goals/clear. The answer is the ref that was
// cleared, which is what lets a client tell which revision it removed.
func (h *Handler) goalsClear(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "agentId", "ref"); failure != nil {
		return nil, failure
	}
	sessionID, failure := h.goalSession(ctx, "goals/clear", args)
	if failure != nil {
		return nil, failure
	}
	ref, failure := goalRefArg(args)
	if failure != nil {
		return nil, failure
	}
	store := h.goalStore()
	if store == nil {
		return nil, goalsDependencyMissing("goals/clear")
	}
	cleared, err := store.Clear(ctx, sessionID, ref)
	if err != nil {
		return nil, goalFailure("goals/clear", sessionID, err)
	}
	return cleared, nil
}
