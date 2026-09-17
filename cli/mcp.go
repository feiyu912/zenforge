package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/adapters/mcp"
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

	tools, err := mcpServerTools(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		return err
	}
	instructions := "ZenForge is a coding agent harness. These tools inspect its recorded runs; they do not start new ones."
	if *allowRun {
		runTool, err := newMCPRunTool(ctx, &opts, ioStreams, *runTimeout)
		if err != nil {
			return err
		}
		tools = append(tools, runTool)
		instructions = "ZenForge is a coding agent harness. These tools inspect its recorded runs, and zenforge_run starts one in the workspace this server was configured with."
		if opts.approve != "always" {
			// Say it once, before serving: a served run that needs a human
			// cannot ask one, and the operator should hear that from the
			// server rather than from a refusal inside a remote run.
			_, _ = fmt.Fprintf(
				ioStreams.Stderr,
				"warning: served runs cannot prompt for approval, so tools that need one will be refused; pass --approve always to allow them\n",
			)
		}
	}
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:         "zenforge",
		Version:      Version,
		Instructions: instructions,
		Tools:        tools,
	})
	if err != nil {
		return err
	}
	return server.Serve(ctx, ioStreams.Stdin, ioStreams.Stdout)
}

// mcpServerTools builds the read-only tool set. It is separate from the
// command so a test can call a tool without a transport.
func mcpServerTools(ctx context.Context, storeType, path string) ([]mcp.ServerTool, error) {
	return []mcp.ServerTool{
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
