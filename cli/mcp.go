package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
	"github.com/feiyu912/zenforge/commands"
)

// mcpServerCommand exposes ZenForge to another agent as an MCP server over
// stdio.
//
// Without a grant the tools it serves are read-only: a tool call arrives from
// another process with no approval prompt in front of it, so the server starts
// with what it can offer without an operator watching. `--allow-run` is the
// operator's grant to start runs here, and it is the only way a run-starting
// tool exists at all; what a served run may then do is decided by the same
// flags any other run is configured with, so `--approve always` is the
// difference between "this run can touch its workspace" and "this run may ask
// for anything".
func mcpServerCommand(ctx context.Context, args []string, ioStreams IO) error {
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("mcp-server", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	bindOptions(fs, &opts)
	allowRun := fs.Bool("allow-run", false, "expose a tool that starts a ZenForge run in this server's workspace")
	runTimeout := fs.Duration("run-timeout", defaultMCPRunTimeout, "bound on one served run")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	if *runTimeout <= 0 {
		return invalidUsage(errors.New("--run-timeout must be positive"))
	}
	// A server can be asked to serve runs, and a served run opens the same
	// resources any other run does, so the drain is registered before the
	// first agent is built.
	defer drainClosers(&opts, ioStreams)

	// The registry is the server's memory of the runs it started, and it is
	// bound to this command's context: a detached run outlives the call that
	// asked for it but not the server itself.
	registry := newServedRunRegistry(ctx)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		registry.shutdown(stopCtx)
	}()

	tools, err := mcpServerTools(ctx, opts.checkpointType, opts.checkpointDir, registry)
	if err != nil {
		return err
	}
	instructions := "ZenForge is a coding agent harness. These tools inspect its recorded runs; they do not start new ones."
	if *allowRun {
		runTool, err := newMCPRunTool(ctx, &opts, ioStreams, *runTimeout, registry)
		if err != nil {
			return err
		}
		tools = append(tools, runTool, newMCPRunCancelTool(opts.checkpointType, opts.checkpointDir, registry))
		instructions = "ZenForge is a coding agent harness. These tools inspect its recorded runs, and zenforge_run starts one in the workspace this server was configured with; zenforge_run can also detach a run, whose state zenforge_run_status then reports and zenforge_run_cancel stops."
		if opts.approve != "always" {
			// Say it once, before serving: a served run has no operator at a
			// keyboard, so a prompt goes to the MCP client when that client
			// advertised elicitation, and is refused otherwise. The operator
			// should hear which of those applies from the server rather than
			// from a refusal inside a remote run.
			if opts.approve == "prompt" {
				_, _ = fmt.Fprintf(
					ioStreams.Stderr,
					"warning: served runs have no operator at a keyboard, so a tool that needs approval is asked of the MCP client when it advertises elicitation and refused otherwise; pass --approve always to allow such tools without asking\n",
				)
			} else {
				_, _ = fmt.Fprintf(
					ioStreams.Stderr,
					"warning: served runs will refuse tools that need approval because --approve never disables approval\n",
				)
			}
		}
	}
	// Resources and prompts are read-only surfaces, so they need no operator
	// grant: they expose what this install already recorded and the command
	// definitions the workspace already has, and neither can change anything.
	prompts, err := mcpServerPrompts(opts)
	if err != nil {
		return err
	}
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:         "zenforge",
		Version:      Version,
		Instructions: instructions,
		Tools:        tools,
		Resources:    mcpServerResources(opts.checkpointType, opts.checkpointDir),
		Prompts:      prompts,
		// This server's tool, resource and prompt sets are fixed by its flags
		// at startup: nothing here adds or removes a tool while it serves, so
		// listChanged stays false and a client is right to cache the lists.
		// The protocol layer's Notify* methods are deliberately left unused.
		DynamicLists: false,
	})
	if err != nil {
		return err
	}
	return server.Serve(ctx, ioStreams.Stdin, ioStreams.Stdout)
}

// mcpServerTools builds the read-only tool set. It is separate from the
// command so a test can call a tool without a transport.
func mcpServerTools(ctx context.Context, storeType, path string, registry *servedRunRegistry) ([]mcp.ServerTool, error) {
	return []mcp.ServerTool{
		newMCPRunStatusTool(ctx, storeType, path, registry),
		{
			Name:        "zenforge_runs",
			Description: "List the runs this ZenForge install recorded: id, status, timestamps, and the task each one was given. Read-only.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of runs to return, newest first. Defaults to 20.",
					},
				},
			},
			ReadOnly: true,
			Handler: func(ctx context.Context, arguments json.RawMessage) (mcp.CallResult, error) {
				var input struct {
					Limit int `json:"limit"`
				}
				if len(arguments) > 0 {
					if err := json.Unmarshal(arguments, &input); err != nil {
						return mcp.CallResult{}, fmt.Errorf("invalid arguments: %w", err)
					}
				}
				limit := input.Limit
				if limit <= 0 {
					limit = 20
				}
				summaries, closeStore, err := listRuns(ctx, storeType, path)
				if err != nil {
					return mcp.CallResult{}, err
				}
				defer func() { _ = closeStore() }()
				if len(summaries) > limit {
					summaries = summaries[:limit]
				}
				encoded, err := json.Marshal(summaries)
				if err != nil {
					return mcp.CallResult{}, err
				}
				return mcp.CallResult{
					Content: []mcp.Content{{Type: "text", Text: string(encoded)}},
					StructuredContent: map[string]any{
						"runs":  summaries,
						"count": len(summaries),
					},
				}, nil
			},
		},
		{
			Name:        "zenforge_version",
			Description: "Report the ZenForge version serving this connection. Read-only.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			ReadOnly:    true,
			Handler: func(ctx context.Context, arguments json.RawMessage) (mcp.CallResult, error) {
				return mcp.CallResult{
					Content:           []mcp.Content{{Type: "text", Text: strings.TrimSpace(Version)}},
					StructuredContent: map[string]any{"version": strings.TrimSpace(Version)},
				}, nil
			},
		},
	}, nil
}

// Resource URIs the server exposes. They are a second way to read the store
// zenforge_runs already reads, not a second store: a resource handler calls
// listRuns on every read, so it can never disagree with the tool.
const (
	mcpRunsIndexURI  = "zenforge://runs"
	mcpRunsURIPrefix = "zenforge://runs/"
	// mcpRunsInstanceURI is the registered template. The {runId} segment is
	// what the protocol layer matches a concrete run URI against, which is why
	// one resource serves every run without enumerating ids at startup.
	mcpRunsInstanceURI = mcpRunsURIPrefix + "{runId}"
)

// mcpServerResources builds the read-only resource set. It is separate from
// the command so a test can read a resource without a transport.
func mcpServerResources(storeType, path string) []mcp.ServerResource {
	return []mcp.ServerResource{
		{
			URI:         mcpRunsIndexURI,
			Name:        "Recorded runs",
			Description: "An index of the runs this ZenForge install recorded, as JSON: id, status, phase, step, and when each was saved.",
			MimeType:    "application/json",
			Handler: func(ctx context.Context, uri string) ([]mcp.ResourceContent, error) {
				summaries, closeStore, err := listRuns(ctx, storeType, path)
				if err != nil {
					return nil, err
				}
				defer func() { _ = closeStore() }()
				encoded, err := json.Marshal(summaries)
				if err != nil {
					return nil, err
				}
				return []mcp.ResourceContent{{URI: uri, MimeType: "application/json", Text: string(encoded)}}, nil
			},
		},
		{
			URI:         mcpRunsInstanceURI,
			Name:        "Recorded run",
			Description: "One recorded run summary by id, as JSON: replace {runId} in the URI with an id zenforge://runs lists.",
			MimeType:    "application/json",
			Handler: func(ctx context.Context, uri string) ([]mcp.ResourceContent, error) {
				runID := strings.TrimPrefix(uri, mcpRunsURIPrefix)
				summaries, closeStore, err := listRuns(ctx, storeType, path)
				if err != nil {
					return nil, err
				}
				defer func() { _ = closeStore() }()
				for _, summary := range summaries {
					if summary.RunID != runID {
						continue
					}
					encoded, err := json.Marshal(summary)
					if err != nil {
						return nil, err
					}
					return []mcp.ResourceContent{{URI: uri, MimeType: "application/json", Text: string(encoded)}}, nil
				}
				// The spec's resource-not-found error, so a client can tell
				// "no such run" from "the read broke".
				return nil, fmt.Errorf("no recorded run %q: %w", runID, mcp.ErrResourceNotFound)
			},
		},
	}
}

// mcpServerPrompts turns the workspace's command definitions into prompts. A
// catalog with no commands produces no prompts at all, so the server omits the
// prompts capability rather than advertising an empty set.
func mcpServerPrompts(opts options) ([]mcp.ServerPrompt, error) {
	catalog, err := buildCatalog(opts)
	if err != nil {
		return nil, err
	}
	if catalog == nil || catalog.Len() == 0 {
		return nil, nil
	}
	prompts := make([]mcp.ServerPrompt, 0, catalog.Len())
	for _, command := range catalog.Commands() {
		prompts = append(prompts, serverPromptForCommand(command, opts))
	}
	return prompts, nil
}

// serverPromptForCommand turns one command into a prompt. The prompt renders
// the same task text /name produces, through the same expander, with one
// deliberate difference: inline shell is never run. prompts/get arrives from
// another process with no approval in front of it, so a run-bash command must
// not become an execution primitive; clearing AllowBash on the copy leaves any
// !`...` expression verbatim and keeps only the bounded, workspace-confined
// @file reads.
func serverPromptForCommand(command commands.Command, opts options) mcp.ServerPrompt {
	arguments := promptArguments(command.ArgumentHint)
	if len(arguments) == 0 && commandUsesArguments(command.Body) {
		// A command with no argument-hint can still use $ARGUMENTS or $1..$9;
		// the placeholder in the template is the declaration, so the prompt
		// takes one raw argument rather than none.
		arguments = []mcp.PromptArgument{{Name: "arguments", Description: "Arguments for /" + command.Name}}
	}
	return mcp.ServerPrompt{
		Name:        command.Name,
		Description: commands.Describe(command),
		Arguments:   arguments,
		Handler: func(ctx context.Context, values map[string]string) (mcp.PromptResult, error) {
			renderable := command
			renderable.AllowBash = false
			expanded, err := commands.Expand(renderable, promptArgumentText(arguments, values), commands.ExpandOptions{
				Workspace: opts.workspace,
			})
			if err != nil {
				return mcp.PromptResult{}, err
			}
			return mcp.PromptResult{
				Messages: []mcp.PromptMessage{{
					Role:    mcp.PromptRoleUser,
					Content: mcp.Content{Type: "text", Text: expanded},
				}},
			}, nil
		},
	}
}

// promptArguments reads the argument names out of a command's argument-hint.
// The hint is the shell-like form the catalog already renders in listings
// ("<path>", "[reason]"), and its placeholder names are the positions $1..$9
// already expect, so no new declaration format is introduced. A hint that is
// free prose with no placeholder becomes one optional argument named
// "arguments", which is the raw string $ARGUMENTS uses.
func promptArguments(hint string) []mcp.PromptArgument {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return nil
	}
	arguments := make([]mcp.PromptArgument, 0, 1)
	for index := 0; index < len(hint); {
		open := hint[index]
		var closing byte
		switch open {
		case '<':
			closing = '>'
		case '[':
			closing = ']'
		default:
			index++
			continue
		}
		end := strings.IndexByte(hint[index+1:], closing)
		if end < 0 {
			break
		}
		name := strings.TrimSpace(hint[index+1 : index+1+end])
		if name != "" {
			arguments = append(arguments, mcp.PromptArgument{
				Name:     name,
				Required: open == '<',
			})
		}
		index += end + 2
	}
	if len(arguments) == 0 {
		arguments = append(arguments, mcp.PromptArgument{Name: "arguments", Description: hint})
	}
	return arguments
}

// promptArgumentText rebuilds the argument string the command's expander
// expects, in declared order. A value containing whitespace is quoted so it
// stays one positional argument: without that, $1 would silently become the
// first word of a phrase.
func promptArgumentText(arguments []mcp.PromptArgument, values map[string]string) string {
	parts := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		value := strings.TrimSpace(values[argument.Name])
		if value == "" {
			continue
		}
		if strings.ContainsAny(value, " \t\n") {
			value = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, " ")
}

// commandUsesArguments reports whether a command body substitutes arguments.
// The scan honours "$$", which the expander treats as an escaped literal
// dollar, so a body that only prints "$5" is not mistaken for one that takes
// five positional arguments.
func commandUsesArguments(body string) bool {
	for index := 0; index < len(body); index++ {
		if body[index] != '$' {
			continue
		}
		if index+1 < len(body) && body[index+1] == '$' {
			index++
			continue
		}
		if strings.HasPrefix(body[index:], "$ARGUMENTS") {
			return true
		}
		if index+1 < len(body) && body[index+1] >= '1' && body[index+1] <= '9' {
			return true
		}
	}
	return false
}
