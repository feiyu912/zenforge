// Package tasks holds the benchmark's three frozen tasks: the turn script the
// scripted endpoint replays, the tool set every runner must define, the
// workspace the harness seeds before a run, the artifact success is judged
// against, and the verifier that judges it.
//
// The scripts are JSON because they are the frozen contract: they are the same
// bytes for every framework, they are reviewable without reading Go, and a
// runner in another language can be developed against them directly. The
// metadata is Go because the harness needs it typed, and the verifier is Go
// because it reads the workspace and the endpoint's recorded call order.
//
// A task's success is never "the runner exited zero". It is the artifact on
// disk plus the conversation the endpoint observed, because a framework that
// announces a tool call without running the tool, or writes the artifact
// without ever reading its input, must not be able to claim the task.
package tasks

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/benchmarks/internal/scripted"
)

// The tool names every runner must define, identically. They are OpenAI-style
// names rather than any framework's native spelling on purpose: the comparison
// must not be decided by whose tool names the script happens to use.
const (
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
	ToolRunShell  = "run_shell"
)

// Approval modes the harness can run a task with.
const (
	ApprovalApprove = "approve"
	ApprovalReject  = "reject"
)

// Phase names the runner protocol uses.
const (
	PhaseRun    = "run"
	PhaseResume = "resume"
)

//go:embed edit-file.json approve-command.json durable-task.json
var scriptFiles embed.FS

// Artifact is one file success is judged against. Content is the exact
// expected bytes; the verifier compares them literally, because "roughly right"
// is not a task result.
type Artifact struct {
	Path        string
	Content     string
	Description string
}

// Metadata is everything the harness needs to run and judge one task.
type Metadata struct {
	ID      string
	Title   string
	Summary string
	// Script is the frozen turn script the endpoint replays.
	Script scripted.Script
	// Query is the exact user message every runner must send, taken from the
	// frozen script. Freezing it is what keeps the byte-cost metric fair: a
	// runner cannot shorten its own task sentence to look cheaper.
	Query string
	// Tools are the tool names this task requires. Every task requires all
	// three, so the comparison is not decided by one framework advertising a
	// smaller tool surface.
	Tools []string
	// Seed is written into a fresh workspace before the runner starts. It is
	// the input a read tool is expected to read.
	Seed map[string]string
	// Artifacts are the files the verifier checks.
	Artifacts []Artifact
	// CommandStdout is what the script's shell command prints to stdout. The
	// verifier requires it to have reached the model: a command whose effect is
	// on disk but whose output never came back was not really run by the loop.
	CommandStdout string
	// RequiresApproval marks a task whose script asks for a command the
	// operator must approve.
	RequiresApproval bool
	// Durable marks a task whose first process must leave resumable state and
	// whose second process finishes the work.
	Durable bool
	// Approvals are the approval modes the harness runs the task in. Most
	// tasks run once; approve-command runs twice because the frozen contract
	// makes both the approved and the rejected outcome part of success.
	Approvals []string
}

// EditFile is the read-then-write task: the simplest complete agent loop.
var EditFile = Metadata{
	ID:      "edit-file",
	Title:   "Read a file, then write an edited copy",
	Summary: "read input.txt, then write out.txt",
	Script:  scriptOf("edit-file.json"),
	Query:   queryOf("edit-file.json"),
	Tools:   []string{ToolReadFile, ToolWriteFile, ToolRunShell},
	Seed: map[string]string{
		"input.txt": "hello from the cross-framework benchmark\n",
	},
	Artifacts: []Artifact{{
		Path:        "out.txt",
		Content:     "hello from the cross-framework benchmark\n(edited)\n",
		Description: "the edited copy of input.txt",
	}},
	Approvals: []string{ApprovalApprove},
}

// ApproveCommand is the approval task: the script asks for a command that needs
// an operator decision. Run with approve, the command must run exactly once and
// leave its effect; run with reject, it must not have run at all. Both are part
// of one task row because the frozen contract ties them together.
var ApproveCommand = Metadata{
	ID:      "approve-command",
	Title:   "Run a command that needs approval",
	Summary: "call run_shell with a command that needs approval",
	Script:  scriptOf("approve-command.json"),
	Query:   queryOf("approve-command.json"),
	Tools:   []string{ToolReadFile, ToolWriteFile, ToolRunShell},
	Artifacts: []Artifact{{
		Path:        "approval.txt",
		Content:     "approved\n",
		Description: "the effect of the approved command, exactly once",
	}},
	CommandStdout:    "approved",
	RequiresApproval: true,
	Approvals:        []string{ApprovalApprove, ApprovalReject},
}

// DurableTask is the recovery task: two recorded steps, then a tool that needs
// approval. The first process must stop durably; a second, fresh process must
// resume the checkpoint and finish the artifact without losing the steps that
// were already recorded.
var DurableTask = Metadata{
	ID:      "durable-task",
	Title:   "Record two steps, pause durably, then resume and finish",
	Summary: "record two steps, then call a tool that requires approval",
	Script:  scriptOf("durable-task.json"),
	Query:   queryOf("durable-task.json"),
	Tools:   []string{ToolReadFile, ToolWriteFile, ToolRunShell},
	Artifacts: []Artifact{
		{
			Path:        "steps.txt",
			Content:     "step 1: read the input\nstep 2: draft the output\n",
			Description: "the two steps recorded before the pause",
		},
		{
			Path:        "out.txt",
			Content:     "durable task complete\n",
			Description: "the artifact written by the resumed process",
		},
		{
			Path:        "milestone.txt",
			Content:     "milestone\n",
			Description: "the effect of the command the resumed process was allowed to run",
		},
	},
	CommandStdout:    "milestone",
	RequiresApproval: true,
	Durable:          true,
	Approvals:        []string{ApprovalApprove},
}

// All is the frozen task set, in the order the report lists it.
var All = []Metadata{EditFile, ApproveCommand, DurableTask}

// scriptedCommand is the run_shell command a task's script issues, if any. It is
// read from the script rather than duplicated in a constant so the script
// stays the one frozen source of truth.
func scriptedCommand(metadata Metadata) string {
	for _, turn := range metadata.Script.Turns {
		for _, call := range turn.ToolCalls {
			if call.Name != ToolRunShell {
				continue
			}
			var arguments struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(call.Arguments, &arguments); err == nil {
				return arguments.Command
			}
		}
	}
	return ""
}

// Command is the shell command this task's script issues; empty for a task
// with no shell call.
func (m Metadata) Command() string { return scriptedCommand(m) }

// Resumable reports whether the harness must run the task in two processes.
func (m Metadata) Resumable() bool { return m.Durable }

// IDs lists every task id, in All order.
func IDs() []string {
	ids := make([]string, 0, len(All))
	for _, task := range All {
		ids = append(ids, task.ID)
	}
	return ids
}

// Lookup finds a task by id.
func Lookup(id string) (Metadata, bool) {
	for _, task := range All {
		if task.ID == strings.TrimSpace(id) {
			return task, true
		}
	}
	return Metadata{}, false
}

// Select resolves a task selection: "all", or a comma-separated list of ids.
// An unknown id is an error the harness reports rather than a silently dropped
// task, for the same reason an unavailable runner is.
func Select(spec string) ([]Metadata, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "all" {
		return append([]Metadata(nil), All...), nil
	}
	var selected []Metadata
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		task, ok := Lookup(id)
		if !ok {
			return nil, fmt.Errorf("unknown task %q: want one of %s", id, strings.Join(IDs(), ", "))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		selected = append(selected, task)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no tasks selected from %q", spec)
	}
	return selected, nil
}

// Seed writes the task's input files into a fresh workspace.
func (m Metadata) SeedWorkspace(workspace string) error {
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(m.Seed))
	for name := range m.Seed {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(workspace, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(m.Seed[name]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// parsedScripts holds the frozen turn scripts, decoded once at startup. A
// script that cannot be read or does not hold together is a compile-time
// mistake in this repository, so it panics rather than yielding a task that
// cannot be run.
var parsedScripts = loadScripts()

func loadScripts() map[string]scripted.Script {
	scripts := map[string]scripted.Script{}
	for _, name := range scriptNames {
		data, err := scriptFiles.ReadFile(name)
		if err != nil {
			panic(fmt.Sprintf("tasks: read %s: %v", name, err))
		}
		script, err := scripted.ParseScript(data)
		if err != nil {
			panic(fmt.Sprintf("tasks: parse %s: %v", name, err))
		}
		if strings.TrimSpace(script.Query) == "" {
			panic(fmt.Sprintf("tasks: %s has no frozen query; every task must name the exact user message its runners send", name))
		}
		if script.Task != strings.TrimSuffix(name, ".json") {
			panic(fmt.Sprintf("tasks: %s declares task %q", name, script.Task))
		}
		scripts[name] = script
	}
	return scripts
}

// scriptNames are the embedded frozen scripts, in report order.
var scriptNames = []string{"edit-file.json", "approve-command.json", "durable-task.json"}

// scriptOf returns a parsed frozen script.
func scriptOf(name string) scripted.Script { return parsedScripts[name] }

// queryOf returns a script's frozen query: the exact user message every runner
// must send for that task.
func queryOf(name string) string { return parsedScripts[name].Query }
