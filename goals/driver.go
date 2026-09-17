package goals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Ralph report statuses.
const (
	StatusContinue = "continue"
	StatusComplete = "complete"
	StatusBlocked  = "blocked"
)

// DefaultMaxHandoffChars bounds one round's report, matching the
// reference's maxHandoffChars.
const DefaultMaxHandoffChars = 16384

// Report is the bounded structured handoff from one fresh round to the
// next. It is the only thing that crosses rounds: the workspace is the
// long-term memory, and the next round starts with a clean context.
type Report struct {
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Evidence  []string `json:"evidence"`
	NextSteps []string `json:"nextSteps"`
	Blocker   string   `json:"blocker"`
}

// Validate checks the reference's report rules exactly: normalized
// strings, status-specific requirements, and the handoff size cap.
func (r Report) Validate(maxHandoffChars int) error {
	if maxHandoffChars <= 0 {
		maxHandoffChars = DefaultMaxHandoffChars
	}
	if !normalizedText(r.Summary) {
		return fmt.Errorf("ralph round report summary must be non-empty and normalized")
	}
	if !normalizedList(r.Evidence) {
		return fmt.Errorf("ralph round report evidence must contain only non-empty normalized strings")
	}
	if !normalizedList(r.NextSteps) {
		return fmt.Errorf("ralph round report nextSteps must contain only non-empty normalized strings")
	}
	if r.Blocker != strings.TrimSpace(r.Blocker) {
		return fmt.Errorf("ralph round report blocker must be a normalized string")
	}
	switch r.Status {
	case StatusContinue:
		if len(r.NextSteps) == 0 || r.Blocker != "" {
			return fmt.Errorf("a continuing ralph report needs nextSteps and an empty blocker")
		}
	case StatusComplete:
		if len(r.Evidence) == 0 || len(r.NextSteps) != 0 || r.Blocker != "" {
			return fmt.Errorf("a complete ralph report needs evidence, no nextSteps, and an empty blocker")
		}
	case StatusBlocked:
		if !normalizedText(r.Blocker) {
			return fmt.Errorf("a blocked ralph report needs a concrete blocker")
		}
	default:
		return fmt.Errorf("ralph round report status %q is invalid", r.Status)
	}
	serialized, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(serialized) > maxHandoffChars {
		return fmt.Errorf("ralph round report exceeds maxHandoffChars (%d > %d)", len(serialized), maxHandoffChars)
	}
	return nil
}

func normalizedText(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func normalizedList(values []string) bool {
	for _, value := range values {
		if !normalizedText(value) {
			return false
		}
	}
	return true
}

// Outcome describes how a driver loop ended.
type Outcome struct {
	Status  string
	Rounds  int
	Goal    *Goal
	Reports []Report
	Reason  string
}

// DriverOptions configures a goal-driven loop.
type DriverOptions struct {
	// MaxRounds bounds the loop; zero selects the goal's own budget.
	MaxRounds int
	// MaxHandoffChars bounds each round report.
	MaxHandoffChars int
}

// RoundFunc runs one round. A goal-driven round receives the admitted
// goal state; a Ralph round additionally receives the previous report
// (empty on the first round) and must return a report.
type RoundFunc func(ctx context.Context, round int, state State, previous Report, report Report) (Report, error)

// ValidateReport checks a round's report.
func ValidateReport(report Report, maxHandoffChars int) error {
	return report.Validate(maxHandoffChars)
}

// RunRalph executes fresh-agent rounds toward one immutable objective:
// each round gets only the previous bounded report, and the loop stops on
// complete, blocked, or the round limit.
func RunRalph(ctx context.Context, objective string, options DriverOptions, runRound RoundFunc) (Outcome, error) {
	if strings.TrimSpace(objective) == "" {
		return Outcome{}, fmt.Errorf("ralph objective is required")
	}
	maxRounds := options.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxGoalRounds
	}
	outcome := Outcome{Status: StatusContinue}
	var previous Report
	for round := 1; round <= maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		report, err := runRound(ctx, round, State{}, previous, Report{})
		if err != nil {
			return outcome, err
		}
		if err := report.Validate(options.MaxHandoffChars); err != nil {
			return outcome, fmt.Errorf("round %d: %w", round, err)
		}
		outcome.Rounds = round
		outcome.Reports = append(outcome.Reports, report)
		switch report.Status {
		case StatusComplete:
			outcome.Status = StatusComplete
			return outcome, nil
		case StatusBlocked:
			outcome.Status = StatusBlocked
			outcome.Reason = report.Blocker
			return outcome, nil
		}
		previous = report
	}
	outcome.Status = StatusContinue
	outcome.Reason = fmt.Sprintf("round limit %d reached", maxRounds)
	return outcome, nil
}

// RunGoal advances one persisted goal round by round. The driver owns the
// lifecycle: it admits each round, records the report's evidence, and
// only reports blocked after the same blocking condition has persisted
// for at least MinBlockedRounds consecutive rounds.
func RunGoal(ctx context.Context, store Store, sessionID string, options DriverOptions, runRound RoundFunc) (Outcome, error) {
	if store == nil {
		return Outcome{}, fmt.Errorf("goal store is required")
	}
	state, err := store.Load(ctx, sessionID)
	if err != nil {
		return Outcome{}, err
	}
	if !state.Armed() {
		return Outcome{Status: string(state.Phase()), Goal: state.Goal}, nil
	}
	maxRounds := options.MaxRounds
	if maxRounds <= 0 || maxRounds > state.Goal.MaxGoalRounds {
		maxRounds = state.Goal.MaxGoalRounds
	}
	outcome := Outcome{Status: StatusContinue, Goal: state.Goal}
	for state.RoundsStarted < maxRounds {
		round := state.RoundsStarted + 1
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		admitted, err := AdmitRound(state, round, time.Time{})
		if err != nil {
			return outcome, err
		}
		state = admitted
		if err := store.Save(ctx, sessionID, state); err != nil {
			return outcome, err
		}
		report, err := runRound(ctx, round, state, Report{}, Report{})
		if err != nil {
			return outcome, err
		}
		if err := report.Validate(options.MaxHandoffChars); err != nil {
			return outcome, fmt.Errorf("round %d: %w", round, err)
		}
		outcome.Rounds = round
		outcome.Reports = append(outcome.Reports, report)

		switch report.Status {
		case StatusComplete:
			completed, err := Complete(state, time.Time{})
			if err != nil {
				return outcome, err
			}
			if err := store.Save(ctx, sessionID, completed); err != nil {
				return outcome, err
			}
			outcome.Status = StatusComplete
			outcome.Goal = completed.Goal
			return outcome, nil
		case StatusBlocked:
			// Block enforces the minimum persistence itself: it records the
			// observation and rejects the transition until the same
			// condition has survived MinBlockedRounds rounds, so the counter
			// lives in durable state rather than in this loop.
			blocked, blockErr := Block(state, BlockedReason{Code: blockedCode(report.Blocker), Message: report.Blocker}, time.Time{})
			if blockErr != nil {
				if IsCode(blockErr, CodeBlockedRounds) {
					state = blocked
					if err := store.Save(ctx, sessionID, state); err != nil {
						return outcome, err
					}
					outcome.Reason = blockErr.Error()
					continue
				}
				return outcome, blockErr
			}
			if err := store.Save(ctx, sessionID, blocked); err != nil {
				return outcome, err
			}
			outcome.Status = StatusBlocked
			outcome.Reason = report.Blocker
			outcome.Goal = blocked.Goal
			return outcome, nil
		}
	}
	outcome.Status = StatusContinue
	outcome.Reason = fmt.Sprintf("round budget of %d exhausted", maxRounds)
	outcome.Goal = state.Goal
	return outcome, nil
}

// Phase reports the current phase, or an empty phase when no goal exists.
func (s State) Phase() Phase {
	if s.Goal == nil {
		return ""
	}
	return s.Goal.Phase
}

// blockedCode derives a lower-kebab-case code from a blocker message so a
// host can route on it without asking the model for structured input.
func blockedCode(message string) string {
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
		code = "blocked"
	}
	if code[0] < 'a' || code[0] > 'z' {
		code = "blocked-" + code
	}
	if len(code) > 40 {
		code = strings.Trim(code[:40], "-")
	}
	return code
}
