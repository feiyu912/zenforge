package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/goals"
)

// goal drives a persisted goal round by round. Round one starts the run;
// every later round resumes it, so the conversation is the goal's own
// context. The model owns the lifecycle through the goal tools, so the
// driver stops as soon as the stored goal is complete or blocked, and it
// never overrides a decision the model already made.
func goalCommand(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("goal", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	maxRounds := fs.Int("max-rounds", 0, "round budget for the created goal")
	runID := fs.String("run-id", "", "run id that owns the goal")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if objective == "" {
		return invalidUsage(errors.New("goal requires an objective"))
	}
	opts.goalsEnabled = true
	defer drainClosers(&opts, ioStreams)
	agent, err := buildAgent(ctx, &opts, ioStreams)
	if err != nil {
		return err
	}
	store := goals.NewFileStore(filepath.Join(opts.checkpointDir, "goals"))
	session := strings.TrimSpace(*runID)
	if session == "" {
		session = cliRunID()
	}
	state, err := goals.Create(goals.State{}, goals.CreateOptions{
		ID:            "goal-" + cliRunID(),
		Objective:     objective,
		MaxGoalRounds: *maxRounds,
	})
	if err != nil {
		return err
	}
	if err := store.Save(ctx, session, state); err != nil {
		return err
	}
	fmt.Fprintf(ioStreams.Stderr, "goal %s: %s (up to %d rounds)\n", state.Goal.ID, state.Goal.Objective, state.Goal.MaxGoalRounds)

	for round := 1; round <= state.Goal.MaxGoalRounds; round++ {
		current, err := store.Load(ctx, session)
		if err != nil {
			return err
		}
		if current.Goal == nil || current.Goal.Phase != goals.PhaseActive {
			return goalOutcome(ioStreams.Stdout, current)
		}
		admitted, err := goals.AdmitRound(current, current.RoundsStarted+1, time.Now())
		if err != nil {
			return goalOutcome(ioStreams.Stdout, current)
		}
		if err := store.Save(ctx, session, admitted); err != nil {
			return err
		}
		var events <-chan zenforge.Event
		if round == 1 {
			events, err = agent.Stream(ctx, zenforge.Task{RunID: session, Input: objective})
		} else {
			events, err = agent.Resume(ctx, session)
		}
		if err != nil {
			return err
		}
		if _, err := renderStreamCapturing(ioStreams.Stdout, events, renderEvent); err != nil {
			return err
		}
		after, err := store.Load(ctx, session)
		if err != nil {
			return err
		}
		switch after.Goal.Phase {
		case goals.PhaseComplete, goals.PhaseBlocked:
			return goalOutcome(ioStreams.Stdout, after)
		}
		fmt.Fprintf(ioStreams.Stdout, "\n[goal round %d/%d complete]\n", after.RoundsStarted, after.Goal.MaxGoalRounds)
	}
	final, err := store.Load(ctx, session)
	if err != nil {
		return err
	}
	fmt.Fprintf(ioStreams.Stdout, "\n[goal round budget of %d exhausted]\n", final.Goal.MaxGoalRounds)
	return goalOutcome(ioStreams.Stdout, final)
}

// goalOutcome reports the goal's final phase to stdout.
func goalOutcome(out io.Writer, state goals.State) error {
	if state.Goal == nil {
		return nil
	}
	switch state.Goal.Phase {
	case goals.PhaseComplete:
		fmt.Fprintf(out, "\ngoal complete after %d round(s)\n", state.RoundsStarted)
	case goals.PhaseBlocked:
		reason := ""
		if state.Goal.BlockedReason != nil {
			reason = state.Goal.BlockedReason.Message
		}
		fmt.Fprintf(out, "\ngoal blocked after %d round(s): %s\n", state.RoundsStarted, reason)
	}
	return nil
}

// ralph runs fresh-agent rounds toward one immutable objective. Each round
// is a new run with no prior conversation: the shared workspace is the
// long-term memory and only the previous round's bounded report crosses
// over. A round ends when the model emits its structured report.
func ralphCommand(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("ralph", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	rounds := fs.Int("rounds", 4, "maximum number of fresh rounds")
	maxHandoff := fs.Int("max-handoff-chars", goals.DefaultMaxHandoffChars, "maximum characters in one round's report")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if objective == "" {
		return invalidUsage(errors.New("ralph requires an objective"))
	}
	if *rounds < 1 {
		return invalidUsage(errors.New("ralph --rounds must be positive"))
	}
	defer drainClosers(&opts, ioStreams)
	agent, err := buildAgent(ctx, &opts, ioStreams)
	if err != nil {
		return err
	}
	loopID := cliRunID()
	reportDir := filepath.Join(opts.checkpointDir, "ralph", loopID)
	outcome, err := goals.RunRalph(ctx, objective, goals.DriverOptions{
		MaxRounds:       *rounds,
		MaxHandoffChars: *maxHandoff,
	}, func(ctx context.Context, round int, _ goals.State, previous goals.Report, _ goals.Report) (goals.Report, error) {
		events, err := agent.Stream(ctx, zenforge.Task{RunID: fmt.Sprintf("%s-round-%d", loopID, round), Input: ralphPrompt(objective, round, *rounds, previous)})
		if err != nil {
			return goals.Report{}, err
		}
		output, err := renderStreamCapturing(ioStreams.Stdout, events, renderEvent)
		if err != nil {
			return goals.Report{}, err
		}
		report, err := parseRoundReport(output)
		if err != nil {
			return goals.Report{}, fmt.Errorf("round %d: %w", round, err)
		}
		if err := writeRoundReport(reportDir, round, report); err != nil {
			return goals.Report{}, err
		}
		fmt.Fprintf(ioStreams.Stdout, "\n[ralph round %d/%d: %s]\n", round, *rounds, report.Status)
		return report, nil
	})
	if err != nil {
		return err
	}
	switch outcome.Status {
	case goals.StatusComplete:
		fmt.Fprintf(ioStreams.Stdout, "\nralph complete after %d round(s)\n", outcome.Rounds)
	case goals.StatusBlocked:
		fmt.Fprintf(ioStreams.Stdout, "\nralph blocked after %d round(s): %s\n", outcome.Rounds, outcome.Reason)
	default:
		fmt.Fprintf(ioStreams.Stdout, "\nralph stopped after %d round(s): %s\n", outcome.Rounds, outcome.Reason)
	}
	fmt.Fprintf(ioStreams.Stdout, "round reports: %s\n", reportDir)
	return nil
}

// ralphPrompt builds one round's prompt. The objective is immutable, and
// the previous report is the only carried-over context.
func ralphPrompt(objective string, round, maxRounds int, previous goals.Report) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Ralph round %d of at most %d. Work in the shared workspace; a fresh agent ran the previous rounds, so nothing but this report carries over.\n\n", round, maxRounds)
	fmt.Fprintf(&builder, "Objective (immutable): %s\n\n", objective)
	if previous.Status == "" {
		builder.WriteString("Previous round report: none; this is the first round.\n\n")
	} else {
		fmt.Fprintf(&builder, "Previous round report:\n%s\n\n", reportJSON(previous))
	}
	builder.WriteString("Do the highest-value work you can in this round, then end your reply with exactly one fenced json block containing a single report object:\n")
	builder.WriteString("```json\n{\"status\":\"continue\",\"summary\":\"what changed\",\"evidence\":[\"concrete evidence\"],\"nextSteps\":[\"what the next round should do\"],\"blocker\":\"\"}\n```\n")
	builder.WriteString("Use status continue with at least one nextSteps entry while useful work remains, complete only with concrete evidence and no nextSteps, and blocked only when no meaningful progress is possible without human input or an external change; blocker must be empty unless blocked. Every string must be non-empty and trimmed.\n")
	return builder.String()
}

// reportJSON renders a report as a compact JSON object.
func reportJSON(report goals.Report) string {
	data, err := json.Marshal(report)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// cliRunID returns a fresh run identifier for a CLI-driven run.
func cliRunID() string {
	return fmt.Sprintf("run_%d", time.Now().UnixNano())
}

// parseRoundReport extracts the last fenced json report from a round's
// output and validates it with the reference's report rules.
func parseRoundReport(output string) (goals.Report, error) {
	candidates := jsonObjects(output)
	for index := len(candidates) - 1; index >= 0; index-- {
		var report goals.Report
		if err := json.Unmarshal([]byte(candidates[index]), &report); err != nil {
			continue
		}
		if err := report.Validate(0); err != nil {
			continue
		}
		return report, nil
	}
	return goals.Report{}, errors.New("the round did not end with a valid structured report")
}

// jsonObjects returns every balanced top-level JSON object in text, in
// order, ignoring braces inside strings.
func jsonObjects(text string) []string {
	var objects []string
	depth := 0
	start := -1
	inString := false
	escaped := false
	for index, r := range text {
		if inString {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = index
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					objects = append(objects, text[start:index+1])
					start = -1
				}
			}
		}
	}
	return objects
}

// writeRoundReport persists one round's report under the loop directory.
func writeRoundReport(dir string, round int, report goals.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("round-%d.json", round)), append(data, '\n'), 0o644)
}
