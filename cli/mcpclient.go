package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
	"github.com/feiyu912/zenforge/tool"
)

const (
	// mcpStartupTimeout bounds one server's initialize + tools/list
	// handshake. A server that never answers is not a server this client can
	// wait on: without a bound, one wedged process would stop the whole CLI
	// before the run even starts.
	mcpStartupTimeout = 30 * time.Second
	// mcpToolCallTimeout is the declared budget for one remote tool call,
	// matching the reference's default per-server tool-call timeout. It
	// travels as a tool declaration (tool.TimeoutDeclarer), so the existing
	// timeout policy arms it and the model never sees it.
	mcpToolCallTimeout = 60 * time.Second
)

// mcpServerSpec is one configured MCP server in the order it will be started.
type mcpServerSpec struct {
	Name     string
	Command  string
	Args     []string
	Env      []string
	Deferred bool
	// StartupTimeout and ToolCallTimeout are this server's overrides for the
	// two bounds above. Zero means the default applies.
	StartupTimeout  time.Duration
	ToolCallTimeout time.Duration
}

// startupTimeout is the bound to use for this server's handshake.
func (s mcpServerSpec) startupTimeout() time.Duration {
	if s.StartupTimeout > 0 {
		return s.StartupTimeout
	}
	return mcpStartupTimeout
}

// toolCallTimeout is the budget to declare for this server's tools.
func (s mcpServerSpec) toolCallTimeout() time.Duration {
	if s.ToolCallTimeout > 0 {
		return s.ToolCallTimeout
	}
	return mcpToolCallTimeout
}

// buildMCPTools starts every configured MCP server and adapts its tools.
//
// A server that cannot be started, or whose handshake fails, fails the
// command. The reference harness logs and carries on (its server list is
// often user-level and shared), but this configuration is the operator's own
// file, like the hook file and the sandbox backend: silently running without
// a tool set that the file asked for is the failure mode this repository
// treats as unacceptable.
func buildMCPTools(ctx context.Context, opts *options, ioStreams IO) ([]tool.Tool, error) {
	if opts == nil || len(opts.mcpServers) == 0 {
		return nil, nil
	}
	var adapted []tool.Tool
	exposed := map[string]string{}
	for _, spec := range opts.mcpServers {
		client, err := mcp.NewStdioClient(ctx, mcp.StdioConfig{
			Command: spec.Command,
			Args:    spec.Args,
			Env:     spec.Env,
			Stderr:  ioStreams.Stderr,
		})
		if err != nil {
			return nil, fmt.Errorf("start mcp server %s: %w", spec.Name, err)
		}
		// Registered immediately: whatever happens next, the command's
		// deferred drain owns this process.
		opts.addCloser("mcp server "+spec.Name, client.Close)

		startupCtx, cancel := context.WithTimeout(ctx, spec.startupTimeout())
		if err := client.Initialize(startupCtx, mcp.InitializeParams{ClientInfo: mcpClientInfo()}); err != nil {
			cancel()
			return nil, fmt.Errorf("initialize mcp server %s: %w", spec.Name, err)
		}
		serverTools, err := mcp.ToolsWithOptions(startupCtx, client, mcp.ServerOptions{
			Server:          spec.Name,
			Deferred:        spec.Deferred,
			ToolCallTimeout: spec.toolCallTimeout(),
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("list tools from mcp server %s: %w", spec.Name, err)
		}
		for _, serverTool := range serverTools {
			// The registry rejects a duplicate name at run time, which would
			// surface as a failed run; two servers whose sanitized names
			// collapse onto one exposed name is a configuration problem, so
			// it fails here instead. The registry matches names
			// case-insensitively, so this check does too.
			key := strings.ToLower(serverTool.Name())
			if previous, ok := exposed[key]; ok {
				return nil, fmt.Errorf(
					"mcp servers %s and %s both expose the tool name %q",
					previous, spec.Name, serverTool.Name(),
				)
			}
			exposed[key] = spec.Name
		}
		adapted = append(adapted, serverTools...)
	}
	return adapted, nil
}

// mcpClientInfo identifies this client in the initialize handshake, using the
// real build version rather than the adapter's fallback.
func mcpClientInfo() mcp.Implementation {
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "unknown"
	}
	return mcp.Implementation{Name: "zenforge", Version: version}
}
