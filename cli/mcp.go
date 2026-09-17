package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/feiyu912/zenforge/adapters/mcp"
)

// mcpServerCommand exposes ZenForge to another agent as an MCP server over
// stdio.
//
// The tools it serves are read-only on purpose: a tool call arrives from
// another process with no approval prompt in front of it, so the server starts
// with what it can offer without an operator watching. A tool that starts a
// run needs the approval path wired first, and shipping it before that would
// mean a remote caller could start an agent on this machine unattended.
func mcpServerCommand(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("mcp-server", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	configPath := fs.String("config", opts.configPath, "config file path")
	checkpointType := fs.String("checkpoint-type", opts.checkpointType, "event/checkpoint store type: jsonl|sqlite")
	checkpointDir := fs.String("checkpoint-dir", opts.checkpointDir, "event/checkpoint directory")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	_ = configPath
	tools, err := mcpServerTools(ctx, *checkpointType, *checkpointDir)
	if err != nil {
		return err
	}
	server, err := mcp.NewServer(mcp.ServerConfig{
		Name:         "zenforge",
		Version:      Version,
		Instructions: "ZenForge is a coding agent harness. These tools inspect its recorded runs; they do not start new ones.",
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
