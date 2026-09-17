// Package goaltools exposes the model-facing goal tools: create_goal,
// get_goal, and update_goal. They operate on a session's persisted goal
// through the goals package, which owns the lifecycle rules; the tools
// own only the schema, argument validation, and the model-facing text.
package goaltools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/goals"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

// Tool names, matching the reference.
const (
	CreateName = "create_goal"
	GetName    = "get_goal"
	UpdateName = "update_goal"
)

// SessionFunc resolves the session that owns the goal from a tool call.
// The default uses the call's run id, so a run is one goal session.
type SessionFunc func(call tool.Context) string

// Config configures the goal tools.
type Config struct {
	// Store persists goal state. Required.
	Store goals.Store
	// Session resolves the owning session; nil uses the run id.
	Session SessionFunc
	// Now overrides the clock (tests).
	Now func() time.Time
	// MaxRounds bounds a newly created goal when the model does not choose
	// a budget.
	MaxRounds int
}

func (c Config) session(call tool.Context) string {
	if c.Session != nil {
		if session := c.Session(call); session != "" {
			return session
		}
	}
	return call.RunID
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

// Tools builds the goal tools as one set: create, get, and update.
func Tools(config Config) ([]tool.Tool, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: goal store is nil", tool.ErrInvalidTool)
	}
	create, err := Create(config)
	if err != nil {
		return nil, err
	}
	get, err := Get(config)
	if err != nil {
		return nil, err
	}
	update, err := Update(config)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{create, get, update}, nil
}

type createInput struct {
	Objective     string `json:"objective" jsonschema:"required,description=The concrete completion objective inferred from the request"`
	MaxGoalRounds int    `json:"maxGoalRounds,omitempty" jsonschema:"description=Optional positive round cap"`
}

type createOutput struct {
	Goal   goalView `json:"goal"`
	Action string   `json:"action"`
}

// goalView is the model-visible snapshot.
type goalView struct {
	ID            string   `json:"id"`
	Revision      int      `json:"revision"`
	Objective     string   `json:"objective"`
	MaxGoalRounds int      `json:"maxGoalRounds"`
	Phase         string   `json:"phase"`
	BlockedReason string   `json:"blockedReason,omitempty"`
	RoundsStarted int      `json:"roundsStarted"`
	Armed         bool     `json:"armed"`
	NextAction    string   `json:"nextAction,omitempty"`
	SeenGoalIDs   []string `json:"seenGoalIds,omitempty"`
}

// view projects a state for the model.
func view(state goals.State) goalView {
	out := goalView{RoundsStarted: state.RoundsStarted, Armed: state.Armed()}
	if state.Goal != nil {
		out.ID = state.Goal.ID
		out.Revision = state.Goal.Revision
		out.Objective = state.Goal.Objective
		out.MaxGoalRounds = state.Goal.MaxGoalRounds
		out.Phase = string(state.Goal.Phase)
		if state.Goal.BlockedReason != nil {
			out.BlockedReason = state.Goal.BlockedReason.Code + ": " + state.Goal.BlockedReason.Message
		}
	}
	if !out.Armed && out.Phase == "" {
		out.NextAction = "create_goal"
	}
	return out
}

// Create builds create_goal.
func Create(config Config) (tool.Tool, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: goal store is nil", tool.ErrInvalidTool)
	}
	return tools.New(CreateName,
		"Create one persisted completion goal for this session. Infer the goal from the user's request; it becomes the durable objective that future rounds continue. Only use when the request is a long-running objective that should continue across rounds, not for a single turn.",
		func(ctx context.Context, in createInput, call tool.Context) (createOutput, error) {
			if strings.TrimSpace(in.Objective) == "" {
				return createOutput{}, fmt.Errorf("%w: objective is required", tool.ErrInvalidArguments)
			}
			session := config.session(call)
			state, err := loadOrEmpty(ctx, config.Store, session)
			if err != nil {
				return createOutput{}, err
			}
			maxRounds := in.MaxGoalRounds
			if maxRounds <= 0 {
				maxRounds = config.MaxRounds
			}
			next, err := goals.Create(state, goals.CreateOptions{
				ID:            newGoalID(config.now(), state),
				Objective:     in.Objective,
				MaxGoalRounds: maxRounds,
				Now:           config.now(),
			})
			if err != nil {
				return createOutput{}, toolError(err)
			}
			if err := config.Store.Save(ctx, session, next); err != nil {
				return createOutput{}, err
			}
			return createOutput{Goal: view(next), Action: "created"}, nil
		})
}

// Get builds get_goal.
func Get(config Config) (tool.Tool, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: goal store is nil", tool.ErrInvalidTool)
	}
	return tools.New(GetName,
		"Read the session's current goal: objective, phase, revision, round budget, and whether another round is armed. Call this before updating a goal so the exact id and revision are used.",
		func(ctx context.Context, _ struct{}, call tool.Context) (createOutput, error) {
			session := config.session(call)
			state, err := config.Store.Load(ctx, session)
			if err != nil {
				if goals.IsNotFound(err) {
					return createOutput{Goal: view(goals.State{}), Action: "none"}, nil
				}
				return createOutput{}, err
			}
			return createOutput{Goal: view(state), Action: "read"}, nil
		})
}

// Update builds update_goal. Exactly one lifecycle action is passed, and
// the goal id and revision must match the current state, mirroring the
// reference's revision-checked updates.
func Update(config Config) (tool.Tool, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("%w: goal store is nil", tool.ErrInvalidTool)
	}
	return tools.New(UpdateName,
		"Update the session's goal with exactly one action: edit (objective or round cap), pause, resume, complete, or blocked with a concrete reason. Pass goalId and revision from get_goal; blocked is rejected until the same blocking condition has persisted for the minimum number of rounds.",
		func(ctx context.Context, in updateInput, call tool.Context) (createOutput, error) {
			session := config.session(call)
			state, err := config.Store.Load(ctx, session)
			if err != nil {
				if goals.IsNotFound(err) {
					return createOutput{}, fmt.Errorf("%w: no goal exists in this session", tool.ErrInvalidArguments)
				}
				return createOutput{}, err
			}
			if state.Goal == nil {
				return createOutput{}, fmt.Errorf("%w: no goal exists in this session", tool.ErrInvalidArguments)
			}
			if in.GoalID != "" && in.GoalID != state.Goal.ID {
				return createOutput{}, fmt.Errorf("%w: goalId %q does not match the current goal %q", tool.ErrInvalidArguments, in.GoalID, state.Goal.ID)
			}
			if in.Revision > 0 && in.Revision != state.Goal.Revision {
				return createOutput{}, fmt.Errorf("%w: revision %d does not match the current revision %d", tool.ErrInvalidArguments, in.Revision, state.Goal.Revision)
			}
			now := config.now()
			var next goals.State
			switch strings.ToLower(strings.TrimSpace(in.Action)) {
			case "edit":
				next, err = goals.Edit(state, goals.EditOptions{Objective: in.Objective, MaxGoalRounds: in.MaxGoalRounds, Now: now})
			case "pause":
				next, err = goals.Pause(state, now)
			case "resume":
				next, err = goals.Resume(state, now)
			case "complete", "completed":
				next, err = goals.Complete(state, now)
			case "blocked", "block":
				if strings.TrimSpace(in.Reason) == "" {
					return createOutput{}, fmt.Errorf("%w: blocked requires a reason", tool.ErrInvalidArguments)
				}
				code := in.ReasonCode
				if strings.TrimSpace(code) == "" {
					code = deriveCode(in.Reason)
				}
				next, err = goals.Block(state, goals.BlockedReason{Code: code, Message: in.Reason}, now)
			default:
				return createOutput{}, fmt.Errorf("%w: action must be edit, pause, resume, complete, or blocked", tool.ErrInvalidArguments)
			}
			if err != nil {
				// A rejected blocked report still records the observation,
				// so the count of consecutive rounds persists across calls.
				if goals.IsCode(err, goals.CodeBlockedRounds) {
					if saveErr := config.Store.Save(ctx, session, next); saveErr != nil {
						return createOutput{}, saveErr
					}
				}
				return createOutput{}, toolError(err)
			}
			if err := config.Store.Save(ctx, session, next); err != nil {
				return createOutput{}, err
			}
			return createOutput{Goal: view(next), Action: strings.ToLower(in.Action)}, nil
		})
}

type updateInput struct {
	Action        string `json:"action" jsonschema:"required,description=One of edit pause resume complete blocked"`
	GoalID        string `json:"goalId,omitempty" jsonschema:"description=The id from get_goal"`
	Revision      int    `json:"revision,omitempty" jsonschema:"description=The revision from get_goal"`
	Objective     string `json:"objective,omitempty" jsonschema:"description=Replacement objective for edit"`
	MaxGoalRounds int    `json:"maxGoalRounds,omitempty" jsonschema:"description=Replacement round cap for edit"`
	Reason        string `json:"reason,omitempty" jsonschema:"description=The concrete blocking condition when blocked"`
	ReasonCode    string `json:"reasonCode,omitempty" jsonschema:"description=Optional lower-kebab-case code for the blocked reason"`
}

// toolError maps a lifecycle rejection to a readable, classified tool
// error so the model can correct its call.
func toolError(err error) error {
	var goalErr *goals.Error
	if errors.As(err, &goalErr) {
		return fmt.Errorf("%w: %s", tool.ErrInvalidArguments, goalErr.Message)
	}
	return err
}

// loadOrEmpty loads a session state, treating a missing state as empty.
func loadOrEmpty(ctx context.Context, store goals.Store, session string) (goals.State, error) {
	state, err := store.Load(ctx, session)
	if err == nil {
		return state, nil
	}
	if goals.IsNotFound(err) {
		return goals.State{}, nil
	}
	return goals.State{}, err
}

// newGoalID builds a stable, session-unique goal id.
func newGoalID(now time.Time, state goals.State) string {
	base := fmt.Sprintf("goal-%d", now.UnixNano())
	if len(state.SeenGoalIDs) == 0 {
		return base
	}
	for suffix := 1; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		duplicate := false
		for _, seen := range state.SeenGoalIDs {
			if seen == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			return candidate
		}
	}
}

// deriveCode builds a lower-kebab-case code from a blocker message.
func deriveCode(message string) string {
	var builder strings.Builder
	previousDash := true
	for _, r := range strings.ToLower(message) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		default:
			if !previousDash {
				builder.WriteByte('-')
				previousDash = true
			}
		}
		if builder.Len() >= 32 {
			break
		}
	}
	code := strings.Trim(builder.String(), "-")
	if code == "" {
		return "blocked"
	}
	if code[0] < 'a' || code[0] > 'z' {
		code = "blocked-" + code
	}
	return code
}
