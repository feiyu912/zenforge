package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/commands"
	"github.com/feiyu912/zenforge/schedule"
	"github.com/feiyu912/zenforge/tool"
)

// resolveCommand expands a leading "/name args" into a task. Input that is
// not a command invocation is returned unchanged, so ordinary tasks that
// merely start with a slash (a path, a URL) keep working; an invocation of a
// name that is not in the catalog is an error with the catalog attached,
// because silently treating "/review" as a literal task is a confusing way
// to lose a review.
func resolveCommand(catalog *commands.Catalog, input string, opts options) (string, error) {
	trimmed := strings.TrimLeft(input, " \t\n")
	if !strings.HasPrefix(trimmed, "/") || catalog == nil || catalog.Len() == 0 {
		return input, nil
	}
	name, args, _ := strings.Cut(strings.TrimPrefix(trimmed, "/"), " ")
	name = strings.TrimSpace(name)
	if name == "" {
		return input, nil
	}
	command, ok := catalog.Get(name)
	if !ok {
		if !looksLikeCommandName(name) {
			return input, nil
		}
		listing := catalog.List()
		if listing == "" {
			listing = "(no commands are defined)"
		}
		return "", fmt.Errorf("unknown command /%s; available commands:\n%s", name, listing)
	}
	expanded, err := commands.Expand(command, args, commands.ExpandOptions{
		Workspace: opts.workspace,
		Bash:      commandShell(opts, command),
	})
	if err != nil {
		return "", fmt.Errorf("command /%s: %w", command.Name, err)
	}
	return expanded, nil
}

// looksLikeCommandName distinguishes "/review" from "/usr/local/bin/thing":
// a command name has no path separators and no dots.
func looksLikeCommandName(name string) bool {
	if strings.ContainsAny(name, "/\\.@") {
		return false
	}
	if strings.HasPrefix(name, "-") {
		return false
	}
	return true
}

// commandShell runs an inline !`cmd` expression for a command that opted in.
// The expression goes through the same shell policy as any other tool call,
// so allowlists, timeouts, sinks, and sandbox escalation still apply: a
// checked-in command file cannot escalate its own privileges.
func commandShell(opts options, command commands.Command) func(string) (string, error) {
	return func(expression string) (string, error) {
		if opts.noShell {
			return "", fmt.Errorf("the shell tool is disabled (--no-shell)")
		}
		shell, err := buildShellTool(opts)
		if err != nil {
			return "", err
		}
		timeout := opts.shellTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		arguments, err := json.Marshal(map[string]any{
			"command":     expression,
			"description": "inline shell for /" + command.Name,
		})
		if err != nil {
			return "", err
		}
		result, err := shell.Call(ctx, arguments, tool.Context{RunID: "command"})
		if err != nil {
			return "", err
		}
		output, _ := result.Structured["output"].(string)
		if result.ExitCode != 0 {
			return "", fmt.Errorf("exit code %d: %s", result.ExitCode, strings.TrimSpace(output))
		}
		return output, nil
	}
}

// buildCatalog loads the commands directory, defaulting to the workspace's
// .zenforge/commands when the flag is not set.
func buildCatalog(opts options) (*commands.Catalog, error) {
	dir := strings.TrimSpace(opts.commandsDir)
	if dir == "" {
		if strings.TrimSpace(opts.workspace) == "" {
			return nil, nil
		}
		dir = opts.workspace + "/" + commands.DefaultDir
	}
	return commands.Load(dir)
}

// runSchedule repeats a task on a schedule until the context ends. Each
// firing creates a fresh run through the same agent, and a firing that is
// still running when the next one is due is skipped rather than stacked: an
// unattended schedule must not build a queue of overlapping runs.
func runSchedule(ctx context.Context, opts options, spec schedule.Spec, task string, ioStreams IO) error {
	return runScheduleWith(ctx, spec, ioStreams, func(runCtx context.Context) error {
		return streamTask(runCtx, opts, task, ioStreams)
	})
}

// runScheduleWith is the schedule loop with its task injected, so the loop
// (skipping, error tolerance, cancellation) is testable without a model.
func runScheduleWith(ctx context.Context, spec schedule.Spec, ioStreams IO, task func(context.Context) error) error {
	next := spec.Next(time.Now())
	if next.IsZero() {
		return fmt.Errorf("schedule %q never matches", spec.Describe())
	}
	for {
		ioStreams.Stdout.Write([]byte(fmt.Sprintf("scheduled %s: next run at %s\n", spec.Describe(), next.Format(time.RFC3339))))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		started := time.Now()
		if err := task(ctx); err != nil {
			// One failed firing does not end the schedule: an unattended
			// schedule that dies on the first error is worse than useless.
			ioStreams.Stderr.Write([]byte(fmt.Sprintf("scheduled run failed: %v\n", err)))
		}
		if ctx.Err() != nil {
			return nil
		}
		next = spec.Next(started)
		if next.IsZero() {
			return fmt.Errorf("schedule %q never matches again", spec.Describe())
		}
	}
}
