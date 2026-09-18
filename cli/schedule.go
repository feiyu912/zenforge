package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/schedule"
)

// scheduleFileName is where durable schedules live inside the checkpoint
// directory, so an install has one place for everything it keeps.
const scheduleFileName = "schedules.json"

// scheduleFilePath resolves which schedules file a command works on: the
// explicit flag first, then this install's checkpoint directory.
func scheduleFilePath(opts *options, override string) string {
	if path := strings.TrimSpace(override); path != "" {
		return path
	}
	if opts == nil {
		return ""
	}
	if strings.TrimSpace(opts.checkpointDir) == "" {
		return ""
	}
	return filepath.Join(opts.checkpointDir, scheduleFileName)
}

// optionValueFlags lists every option flag that takes a separate value, read
// from the flag set itself: a flag whose value is not a bool flag needs its
// next token. Asking the set means a new option cannot silently break argument
// splitting the way a hand-kept list can.
var optionValueFlags = sync.OnceValue(func() map[string]bool {
	fs := flag.NewFlagSet("options", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts options
	bindOptions(fs, &opts)
	names := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if boolFlag, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
			return
		}
		names[f.Name] = true
	})
	return names
})

// scheduleValueFlags are the flags that take a separate value, which is what
// splitting the line has to know to leave the value with its flag.
func scheduleValueFlags(name string) bool {
	switch name {
	case "schedule-file", "spec", "task", "id":
		return true
	}
	return optionValueFlags()[name]
}

// splitScheduleArgs separates the verb, the flags, and the positionals so they
// can appear in any order. The verb is the first token that is not a flag.
func splitScheduleArgs(args []string) (verb string, flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		token := args[i]
		if strings.HasPrefix(token, "-") {
			flagArgs = append(flagArgs, token)
			name := strings.TrimLeft(token, "-")
			if !strings.Contains(name, "=") && scheduleValueFlags(name) && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if verb == "" {
			verb = token
			continue
		}
		positional = append(positional, token)
	}
	return verb, flagArgs, positional
}

// scheduleCommand manages durable schedules: tasks that run again after the
// process that added them is gone. `schedule run-due` is the half that a timer
// (cron, launchd, a systemd timer) calls; it runs whatever is due and exits.
func scheduleCommand(ctx context.Context, args []string, ioStreams IO) error {
	verb, flagArgs, positional := splitScheduleArgs(args)
	switch verb {
	case "add":
		return scheduleAdd(flagArgs, positional, ioStreams)
	case "list":
		return scheduleList(flagArgs, positional, ioStreams)
	case "remove":
		return scheduleRemove(flagArgs, positional, ioStreams)
	case "run-due":
		return scheduleRunDue(ctx, flagArgs, positional, ioStreams)
	case "":
		return invalidUsage(fmt.Errorf("schedule needs a verb: add, list, remove, or run-due"))
	default:
		return invalidUsage(fmt.Errorf("unknown schedule verb %q: want add, list, remove, or run-due", verb))
	}
}

// scheduleFlags builds the option set for one verb. It returns a pointer
// because the flag set writes into it: a copy made before Parse would carry
// the configuration layers but none of the flags.
func scheduleFlags(name string, flagArgs []string, ioStreams IO) (*options, *flag.FlagSet, error) {
	opts, err := optionsFromArgs(flagArgs)
	if err != nil {
		return nil, nil, err
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	bindOptions(fs, &opts)
	return &opts, fs, nil
}

func scheduleAdd(flagArgs, positional []string, ioStreams IO) error {
	opts, fs, err := scheduleFlags("schedule add", flagArgs, ioStreams)
	if err != nil {
		return err
	}
	specText := fs.String("spec", "", "when to run: 'every 1h' or a cron line like '0 3 * * *'")
	task := fs.String("task", "", "the task to run")
	scheduleFile := fs.String("schedule-file", "", "schedules file (default: <checkpoint dir>/"+scheduleFileName+")")
	jsonOut := fs.Bool("json", false, "print the new schedule as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return invalidUsage(err)
	}
	if len(positional) > 0 {
		return invalidUsage(fmt.Errorf("schedule add takes no arguments, got %q", strings.Join(positional, " ")))
	}
	file, err := schedule.Open(scheduleFilePath(opts, *scheduleFile))
	if err != nil {
		return err
	}
	// The workspace is the standard --workspace option, recorded with the
	// schedule so a firing runs where it was added rather than wherever the
	// timer happens to start it.
	entry, err := file.Add(schedule.Entry{Spec: *specText, Task: *task, Workspace: opts.workspace}, time.Now())
	if err != nil {
		return invalidUsage(err)
	}
	if *jsonOut {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(ioStreams.Stdout, string(encoded))
		return err
	}
	_, err = fmt.Fprintf(ioStreams.Stdout, "added schedule %s: %s, next run at %s\n",
		entry.ID, entry.Spec, entry.NextRunAt.Format(time.RFC3339))
	return err
}

func scheduleList(flagArgs, positional []string, ioStreams IO) error {
	opts, fs, err := scheduleFlags("schedule list", flagArgs, ioStreams)
	if err != nil {
		return err
	}
	scheduleFile := fs.String("schedule-file", "", "schedules file (default: <checkpoint dir>/"+scheduleFileName+")")
	jsonOut := fs.Bool("json", false, "print the schedules as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return invalidUsage(err)
	}
	if len(positional) > 0 {
		return invalidUsage(fmt.Errorf("schedule list takes no arguments, got %q", strings.Join(positional, " ")))
	}
	file, err := schedule.Open(scheduleFilePath(opts, *scheduleFile))
	if err != nil {
		return err
	}
	entries := file.List()
	if *jsonOut {
		if entries == nil {
			entries = []schedule.Entry{}
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(ioStreams.Stdout, string(encoded))
		return err
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(ioStreams.Stdout, "no schedules found")
		return err
	}
	for _, entry := range entries {
		last := "never ran"
		if entry.LastRunAt != nil {
			last = fmt.Sprintf("last %s at %s", entry.LastStatus, entry.LastRunAt.Format(time.RFC3339))
			if entry.Missed > 0 {
				last = fmt.Sprintf("%s, %d missed", last, entry.Missed)
			}
		}
		line := fmt.Sprintf("%s  %s  next %s  %s", entry.ID, entry.Spec, entry.NextRunAt.Format(time.RFC3339), last)
		if entry.Workspace != "" {
			line += "  in " + entry.Workspace
		}
		if _, err := fmt.Fprintln(ioStreams.Stdout, line); err != nil {
			return err
		}
	}
	return nil
}

func scheduleRemove(flagArgs, positional []string, ioStreams IO) error {
	opts, fs, err := scheduleFlags("schedule remove", flagArgs, ioStreams)
	if err != nil {
		return err
	}
	id := fs.String("id", "", "the schedule to remove")
	scheduleFile := fs.String("schedule-file", "", "schedules file (default: <checkpoint dir>/"+scheduleFileName+")")
	jsonOut := fs.Bool("json", false, "print the removed schedule as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return invalidUsage(err)
	}
	removeID := strings.TrimSpace(*id)
	switch {
	case removeID != "" && len(positional) > 0:
		return invalidUsage(fmt.Errorf("schedule remove takes an id once, not both --id and %q", strings.Join(positional, " ")))
	case removeID == "" && len(positional) == 1:
		removeID = positional[0]
	case removeID == "" && len(positional) == 0:
		return invalidUsage(fmt.Errorf("schedule remove needs an id"))
	case len(positional) > 1:
		return invalidUsage(fmt.Errorf("schedule remove takes one id, got %q", strings.Join(positional, " ")))
	}
	file, err := schedule.Open(scheduleFilePath(opts, *scheduleFile))
	if err != nil {
		return err
	}
	entry, err := file.Remove(removeID)
	if err != nil {
		return invalidUsage(err)
	}
	if *jsonOut {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(ioStreams.Stdout, string(encoded))
		return err
	}
	_, err = fmt.Fprintf(ioStreams.Stdout, "removed schedule %s (%s)\n", entry.ID, entry.Spec)
	return err
}

// scheduleResult is one firing's outcome, which is what --json reports.
type scheduleResult struct {
	RunID     string `json:"runId"`
	Schedule  string `json:"scheduleId"`
	Spec      string `json:"spec"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	NextRunAt string `json:"nextRunAt,omitempty"`
	Missed    int    `json:"missed,omitempty"`
	Removed   bool   `json:"removed,omitempty"`
}

func scheduleRunDue(ctx context.Context, flagArgs, positional []string, ioStreams IO) error {
	opts, fs, err := scheduleFlags("schedule run-due", flagArgs, ioStreams)
	if err != nil {
		return err
	}
	scheduleFile := fs.String("schedule-file", "", "schedules file (default: <checkpoint dir>/"+scheduleFileName+")")
	dryRun := fs.Bool("dry-run", false, "report what is due without running it")
	jsonOut := fs.Bool("json", false, "print one JSON result per schedule instead of streaming the runs")
	if err := fs.Parse(flagArgs); err != nil {
		return invalidUsage(err)
	}
	if len(positional) > 0 {
		return invalidUsage(fmt.Errorf("schedule run-due takes no arguments, got %q", strings.Join(positional, " ")))
	}
	file, err := schedule.Open(scheduleFilePath(opts, *scheduleFile))
	if err != nil {
		return err
	}
	due := file.Due(time.Now())
	if *dryRun {
		if len(due) == 0 {
			_, err := fmt.Fprintln(ioStreams.Stdout, "no schedules are due")
			return err
		}
		for _, entry := range due {
			if _, err := fmt.Fprintf(ioStreams.Stdout, "due: %s  %s  was due at %s\n",
				entry.ID, entry.Spec, entry.NextRunAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	}
	if len(due) == 0 {
		if *jsonOut {
			_, err := fmt.Fprintln(ioStreams.Stdout, "[]")
			return err
		}
		_, err := fmt.Fprintln(ioStreams.Stdout, "no schedules are due")
		return err
	}

	results := make([]scheduleResult, 0, len(due))
	failed := 0
	for _, entry := range due {
		// Every firing gets its own options: its own workspace, its own MCP
		// processes and stores, so nothing accumulates across firings.
		firing := *opts
		firing.closers = nil
		if entry.Workspace != "" {
			firing.workspace = entry.Workspace
			firing.shellWorkingDir = entry.Workspace
		}
		firingStreams := ioStreams
		if *jsonOut {
			// A machine-readable summary and a stream of model events on the
			// same stdout do not mix: the run's trace lives in its checkpoints.
			firingStreams.Stdout = io.Discard
		}
		runID, status, runErr := runScheduledTask(ctx, &firing, entry, firingStreams)
		drainClosers(&firing, ioStreams)

		result := scheduleResult{RunID: runID, Schedule: entry.ID, Spec: entry.Spec, Status: status}
		if runErr != nil {
			result.Error = runErr.Error()
		}
		if status != "completed" {
			failed++
		}
		updated, exhausted, recordErr := file.RecordRun(entry.ID, runID, status, time.Now())
		if recordErr != nil {
			return recordErr
		}
		result.Removed = exhausted
		if !exhausted {
			result.NextRunAt = updated.NextRunAt.Format(time.RFC3339)
			result.Missed = updated.Missed
		}
		results = append(results, result)
		if !*jsonOut {
			line := fmt.Sprintf("ran schedule %s: %s", entry.ID, status)
			if exhausted {
				line += "; its spec can never match again, so it was removed"
			} else {
				line += fmt.Sprintf("; next run at %s", updated.NextRunAt.Format(time.RFC3339))
				if updated.Missed > 0 {
					line += fmt.Sprintf(" (%d missed windows were skipped)", updated.Missed)
				}
			}
			if runErr != nil {
				line += ": " + runErr.Error()
			}
			if _, err := fmt.Fprintln(ioStreams.Stdout, line); err != nil {
				return err
			}
		}
	}
	if *jsonOut {
		encoded, err := json.Marshal(results)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(ioStreams.Stdout, string(encoded)); err != nil {
			return err
		}
	}
	if failed > 0 {
		// A timer invocation has to be able to notice: the runs happened and
		// are recorded, and the exit status is what cron or launchd reports.
		return fmt.Errorf("%d of %d due schedules failed", failed, len(due))
	}
	return nil
}

// runScheduledTask runs one firing and returns its run id and its status. The
// run id is chosen here and handed to the agent, so the schedule can record
// which run a firing produced even when the run fails.
func runScheduledTask(ctx context.Context, opts *options, entry schedule.Entry, ioStreams IO) (string, string, error) {
	runID := zenforge.NewRunID()
	agent, err := buildAgent(ctx, opts, ioStreams)
	if err != nil {
		return runID, "failed", err
	}
	events, err := agent.Stream(ctx, zenforge.Task{Input: entry.Task, RunID: runID})
	if err != nil {
		return runID, statusForRunError(ctx, err), err
	}
	if err := renderStream(ioStreams.Stdout, events); err != nil {
		return runID, statusForRunError(ctx, err), err
	}
	return runID, "completed", nil
}

func statusForRunError(ctx context.Context, err error) string {
	if err == nil {
		return "completed"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "failed"
}
