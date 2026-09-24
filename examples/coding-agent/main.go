// Command coding-agent is a workspace-editing agent with a human in the loop:
// it reads a file, changes it, runs a shell command to verify the change, and
// asks the operator to approve every write and every command the policy has not
// allowlisted.
//
// The transcript on stdout is one greppable line per observable step:
//
//	approval: <tool> <decision>
//	tool: <name>
//	write: <path>
//	shell: <command>
//	answer: <text>
//
// The approval prompt itself -- the numbered choices the operator answers -- is
// printed on stderr by the CLI broker, so a caller can separate the machine
// readable transcript from the interaction.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalcli "github.com/feiyu912/zenforge/approval/cli"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

const instructions = "You are a coding agent with exactly one workspace. Inspect before you edit: read a " +
	"file with workspace_read before you change it, because a write to a path this run has not read is " +
	"refused. Make the smallest change that satisfies the task and state why in the write's description. " +
	"Then verify the change by running a shell command. The policy allowlists the project's Go checks " +
	"(go build ./... and go test ./...), so they run without a prompt; every write, and every command " +
	"outside that allowlist, is sent to the operator for approval before it runs."

func main() {
	task := flag.String("task", "Read greeting.txt, correct the greeting, and verify the change.", "task for the agent")
	workspaceFlag := flag.String("workspace", ".", "workspace root the agent may read and edit")
	runDir := flag.String("run-dir", envOrDefault("ZENFORGE_RUN_DIR", ".zenforge/coding-agent"),
		"directory for the JSONL event log and checkpoints")
	flag.Parse()

	modelClient, err := provider.FromEnv()
	if err != nil {
		fatal(err)
	}
	workspaceRoot, err := filepath.Abs(*workspaceFlag)
	if err != nil {
		fatal(err)
	}

	workspace, err := workspacelocal.New(workspacelocal.Config{
		Root:          workspaceRoot,
		MaxReadBytes:  1_000_000,
		MaxWriteBytes: 1_000_000,
	})
	if err != nil {
		fatal(err)
	}

	// Reads are confined to the workspace and no write root is pre-authorized,
	// so every write is an explicit operator decision. A path that escapes the
	// policy roots (an absolute path, or one climbing out with "..") is refused
	// outright rather than offered for approval, and the workspace refuses it
	// again at the filesystem layer.
	filePolicy := policy.FilePolicy{
		ReadRoots:       []string{"."},
		RequireApproval: true,
	}
	workspaceTools, err := workspacetools.Tools(workspacetools.Config{
		Workspace:              workspace,
		Snapshots:              workspacetools.NewSnapshotStore(),
		RequireReadBeforeWrite: true,
		Policy:                 filePolicy,
	})
	if err != nil {
		fatal(err)
	}

	// The allowlist is the policy's no-prompt tier. The local backend means the
	// example needs nothing installed: only a POSIX shell, which the agent
	// already requires.
	shell := shelltool.Must(shelltool.Config{
		Backend: shelltool.ShellBackendLocal,
		Policy: policy.ShellPolicy{
			WorkingDir:      workspaceRoot,
			AllowCommands:   []string{"go build ./...", "go test ./..."},
			RequireApproval: true,
			MaxTimeout:      30 * time.Second,
			MaxOutputBytes:  1 << 20,
		},
	})

	observed := make([]tool.Tool, 0, len(workspaceTools)+1)
	for _, workspaceTool := range workspaceTools {
		observed = append(observed, observe(workspaceTool))
	}
	observed = append(observed, observe(shell))

	agent := zenforge.New(zenforge.Config{
		Model:        modelClient,
		Instructions: instructions,
		Tools:        observed,
		Approval:     transcriptBroker{inner: approvalcli.New(os.Stdin, os.Stderr)},
		Events:       eventlogjsonl.New(*runDir),
		Checkpoints:  checkpointjsonl.New(*runDir),
		MaxSteps:     12,
	})

	result, err := agent.Run(context.Background(), zenforge.Task{Input: *task})
	if err != nil {
		fatal(err)
	}
	fmt.Printf("answer: %s\n", result.Output)
}

// observedTool renders one transcript line per tool call. "tool:" records that
// the agent in this run reached for the tool, and the write/shell detail line
// is printed only once the call really executed: a call still waiting for an
// approval, and a call the operator rejected, both leave the workspace
// untouched and must not be rendered as if something happened.
type observedTool struct {
	tool.Tool
}

func observe(base tool.Tool) tool.Tool { return observedTool{Tool: base} }

func (t observedTool) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	result, err := t.Tool.Call(ctx, raw, call)
	if _, pending := approval.RequestFromResult(result); pending {
		return result, err
	}
	fmt.Printf("tool: %s\n", t.Name())
	if result.Error != approval.ErrorRejected {
		if detail := detailLine(t.Name(), raw); detail != "" {
			fmt.Println(detail)
		}
	}
	return result, err
}

// TimeoutBudget and ReadOnly are forwarded so wrapping a tool does not change
// the runtime metadata the agent reads from it.
func (t observedTool) TimeoutBudget() time.Duration {
	if declarer, ok := t.Tool.(tool.TimeoutDeclarer); ok {
		return declarer.TimeoutBudget()
	}
	return 0
}

func (t observedTool) ReadOnly() bool {
	if declarer, ok := t.Tool.(tool.ReadOnlyDeclarer); ok {
		return declarer.ReadOnly()
	}
	return false
}

// detailLine names the mutation a mutating tool performed, and the command a
// shell call ran, from the tool's own arguments.
func detailLine(name string, raw json.RawMessage) string {
	switch name {
	case "workspace_write", "workspace_edit":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &in); err != nil || in.Path == "" {
			return ""
		}
		return "write: " + in.Path
	case "shell":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(raw, &in); err != nil || in.Command == "" {
			return ""
		}
		return "shell: " + in.Command
	default:
		return ""
	}
}

// transcriptBroker wraps the CLI approval broker so the operator's decision is
// part of the transcript. The prompt and its numbered choices go to stderr;
// this line records what the operator answered, for the tool that asked.
type transcriptBroker struct {
	inner approval.Broker
}

func (b transcriptBroker) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	decision, err := b.inner.Request(ctx, req)
	if err != nil {
		return decision, err
	}
	fmt.Printf("approval: %s %s\n", req.ToolName, decision.Action)
	return decision, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "coding-agent:", err)
	os.Exit(1)
}
