// Command zenforge-runner is the benchmark's ZenForge runner: the reference
// implementation the other three runners are compared against.
//
// It is a subprocess entry point, not a library, because the benchmark's
// protocol is a subprocess protocol: the harness sets BENCH_* variables in the
// environment and reads one JSON result file back. That keeps this runner and
// the Python and Eino runners on equal footing -- none of them can reach into
// the harness, and the harness cannot special-case any of them.
//
// It uses only ZenForge's public SDK. The task loop is the real agent loop:
// the same model adapter, tool dispatch, approval pause and checkpoint store a
// deployment would use. Nothing here is tuned to win the comparison; where the
// SDK's native behavior costs more than another framework's (a verbose prompt,
// a checkpoint at every boundary) that cost is left visible, because a
// benchmark that flatters its own framework measures nothing.
//
// Two pieces of the runner are worth reading closely:
//
//   - durable-task's first phase is started with no approval broker, exactly as
//     a deployment that must not guess on an operator's behalf would start it.
//     The tool that needs a decision returns approval.RequiredResult together
//     with approval.ErrRequired, the run checkpoints the waiting request and
//     stops without a terminal event, and this process exits 75 (paused). The
//     second process resumes the same checkpoint with a broker configured and
//     finishes the task.
//   - The three tools are named read_file, write_file and run_shell because
//     every framework in the comparison must expose the same tool surface. The
//     read and write tools are thin wrappers over the SDK's local workspace;
//     run_shell delegates to the SDK's shell tool, which owns policy review and
//     the approval request. That delegation is why a shell command that needs
//     approval pauses and resumes through the SDK's own path rather than a
//     benchmark-only one.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	"github.com/feiyu912/zenforge/workspace/local"

	"github.com/feiyu912/zenforge/benchmarks/tasks"
)

const (
	// programName prefixes every diagnostic on stderr.
	programName = "zenforge-runner"
	// frameworkName is what the result JSON reports. It names the framework,
	// not a version: the runner is built from the same source tree the
	// benchmark measures, so a version string here would be a second source of
	// truth that could disagree with the checkout.
	frameworkName = "zenforge"
	// maxSteps bounds a run. It is generous: the scripts need at most six
	// steps, and the bound must not be what ends a task, because a run that
	// exhausts MaxSteps is finalized rather than paused.
	maxSteps = 12
	// shellTimeout bounds one command. The scripted commands are trivial; the
	// bound exists so a framework bug cannot hang the benchmark.
	shellTimeout = 10 * time.Second

	// The sysexits.h exit codes the contract fixes.
	exitCompleted   = 0
	exitFailed      = 1
	exitPaused      = 75
	exitUnsupported = 78

	// The statuses the contract's result JSON uses. They are repeated here
	// rather than imported from the harness's protocol package so this binary
	// stays a standalone runner that shares nothing with the harness but the
	// documented environment and result file.
	statusCompleted   = "completed"
	statusPaused      = "paused"
	statusUnsupported = "unsupported"
	statusFailed      = "failed"
)

// config is one invocation, read entirely from the documented environment.
type config struct {
	task string
	// query is the task's frozen user message. The runner sends it verbatim;
	// inventing its own sentence would make the prompt-byte cost metric depend
	// on who wrote the shortest prompt instead of on the framework.
	query        string
	phase        string
	workspace    string
	stateDir     string
	resultPath   string
	baseURL      string
	apiKey       string
	model        string
	approval     string
	requirePause bool
}

// result is the JSON written to BENCH_RESULT.
type result struct {
	Task      string `json:"task"`
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
	Framework string `json:"framework"`
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, programName+":", err)
		os.Exit(exitFailed)
	}
	status, detail := execute(context.Background(), cfg)
	if err := writeResult(cfg, status, detail); err != nil {
		fmt.Fprintln(os.Stderr, programName+":", err)
		os.Exit(exitFailed)
	}
	if status != statusCompleted {
		fmt.Fprintln(os.Stderr, programName+":", detail)
	}
	os.Exit(exitForStatus(status))
}

// loadConfig reads the runner protocol's environment and refuses an
// invocation it cannot honor, rather than running a task the harness did not
// ask for.
func loadConfig() (config, error) {
	cfg := config{
		task:       strings.TrimSpace(os.Getenv("BENCH_TASK")),
		query:      strings.TrimSpace(os.Getenv("BENCH_QUERY")),
		phase:      envOr("BENCH_PHASE", tasks.PhaseRun),
		workspace:  strings.TrimSpace(os.Getenv("BENCH_WORKSPACE")),
		stateDir:   strings.TrimSpace(os.Getenv("BENCH_STATE_DIR")),
		resultPath: strings.TrimSpace(os.Getenv("BENCH_RESULT")),
		baseURL:    strings.TrimSpace(os.Getenv("BENCH_BASE_URL")),
		apiKey:     strings.TrimSpace(os.Getenv("BENCH_API_KEY")),
		model:      strings.TrimSpace(os.Getenv("BENCH_MODEL")),
		approval:   envOr("BENCH_APPROVAL", tasks.ApprovalApprove),
	}
	cfg.requirePause = strings.TrimSpace(os.Getenv("BENCH_REQUIRE_PAUSE")) == "1"

	required := map[string]string{
		"BENCH_TASK":      cfg.task,
		"BENCH_QUERY":     cfg.query,
		"BENCH_WORKSPACE": cfg.workspace,
		"BENCH_STATE_DIR": cfg.stateDir,
		"BENCH_RESULT":    cfg.resultPath,
		"BENCH_BASE_URL":  cfg.baseURL,
		"BENCH_API_KEY":   cfg.apiKey,
		"BENCH_MODEL":     cfg.model,
	}
	for name, value := range required {
		if value == "" {
			return config{}, fmt.Errorf("%s is required", name)
		}
	}
	if _, ok := tasks.Lookup(cfg.task); !ok {
		return config{}, fmt.Errorf("unknown BENCH_TASK %q", cfg.task)
	}
	switch cfg.phase {
	case tasks.PhaseRun, tasks.PhaseResume:
	default:
		return config{}, fmt.Errorf("unknown BENCH_PHASE %q", cfg.phase)
	}
	switch cfg.approval {
	case tasks.ApprovalApprove, tasks.ApprovalReject:
	default:
		return config{}, fmt.Errorf("unknown BENCH_APPROVAL %q", cfg.approval)
	}
	// The protocol says these are absolute. Accepting a relative path would
	// make the runner's answer depend on its own working directory, which the
	// harness does not promise.
	for name, value := range map[string]string{
		"BENCH_WORKSPACE": cfg.workspace,
		"BENCH_STATE_DIR": cfg.stateDir,
		"BENCH_RESULT":    cfg.resultPath,
	} {
		if !filepath.IsAbs(value) {
			return config{}, fmt.Errorf("%s must be an absolute path, got %q", name, value)
		}
	}
	return cfg, nil
}

// execute runs the requested phase and reports the status the harness must map
// to an exit code.
func execute(ctx context.Context, cfg config) (string, string) {
	modelClient, err := provider.New(provider.Config{
		Protocol: provider.OpenAI,
		APIKey:   cfg.apiKey,
		BaseURL:  cfg.baseURL,
		Model:    cfg.model,
	})
	if err != nil {
		return statusFailed, fmt.Sprintf("build model adapter: %v", err)
	}
	agent, err := buildAgent(cfg, modelClient)
	if err != nil {
		return statusFailed, err.Error()
	}
	if cfg.phase == tasks.PhaseResume {
		return resumePhase(ctx, agent, cfg)
	}
	return runPhase(ctx, agent, cfg)
}

// runPhase works the task in this process. A durable task is expected to stop
// at an approval the harness deliberately did not answer; any other ending
// would mean the task did not exercise the pause it exists to measure.
func runPhase(ctx context.Context, agent *zenforge.Agent, cfg config) (string, string) {
	outcome, err := agent.Run(ctx, zenforge.Task{
		RunID: runID(cfg),
		Input: cfg.query,
	})
	if errors.Is(err, approval.ErrRequired) {
		if !cfg.requirePause {
			return statusFailed, "the run paused for an approval the harness did not require"
		}
		return statusPaused, "paused with the waiting approval checkpointed and no terminal event"
	}
	if err != nil {
		return statusFailed, fmt.Sprintf("run: %v", err)
	}
	if cfg.requirePause {
		return statusFailed, "the run completed instead of pausing durably, so nothing is left to resume"
	}
	finishedRunID := cfg.task
	if outcome != nil && outcome.RunID != "" {
		finishedRunID = outcome.RunID
	}
	return statusCompleted, fmt.Sprintf("finished run %s", finishedRunID)
}

// resumePhase loads the checkpoint the paused process left and finishes the
// run. The approval broker this process is built with answers the waiting
// request, so the pending tool call executes here rather than in the process
// that stopped.
func resumePhase(ctx context.Context, agent *zenforge.Agent, cfg config) (string, string) {
	events, err := agent.Resume(ctx, runID(cfg))
	if err != nil {
		return statusFailed, fmt.Sprintf("resume %s: %v", runID(cfg), err)
	}
	var terminal error
	for event := range events {
		switch event.Type {
		case zenforge.EventRunError:
			terminal = errors.New(payloadText(event.Payload["error"]))
		case zenforge.EventRunCancelled:
			terminal = fmt.Errorf("run cancelled: %s", payloadText(event.Payload["error"]))
		}
	}
	if terminal != nil {
		return statusFailed, fmt.Sprintf("resumed run: %v", terminal)
	}
	return statusCompleted, fmt.Sprintf("resumed run %s and finished it", runID(cfg))
}

// buildAgent assembles the same agent for both phases. Resume must re-register
// the identical tools by name: the checkpoint holds a pending call, and the
// tool that answers it is looked up on the resuming agent.
func buildAgent(cfg config, modelClient zenforge.Model) (*zenforge.Agent, error) {
	workspaceClient, err := local.New(local.Config{Root: cfg.workspace, CreateParentDir: true})
	if err != nil {
		return nil, fmt.Errorf("open workspace %s: %v", cfg.workspace, err)
	}
	readFile, err := tools.New(tasks.ToolReadFile,
		"Read a file from the workspace and return its contents.",
		func(ctx context.Context, in readFileInput) (readFileOutput, error) {
			data, err := workspaceClient.Read(ctx, in.Path)
			if err != nil {
				return readFileOutput{}, err
			}
			return readFileOutput{Path: in.Path, Content: string(data), Bytes: len(data)}, nil
		})
	if err != nil {
		return nil, err
	}
	writeFile, err := tools.New(tasks.ToolWriteFile,
		"Write content to a file in the workspace, replacing anything already there.",
		func(ctx context.Context, in writeFileInput) (writeFileOutput, error) {
			if err := workspaceClient.Write(ctx, in.Path, []byte(in.Content)); err != nil {
				return writeFileOutput{}, err
			}
			return writeFileOutput{Path: in.Path, Bytes: len(in.Content)}, nil
		})
	if err != nil {
		return nil, err
	}
	shell, err := shelltool.New(shelltool.Config{
		Policy: policy.ShellPolicy{
			WorkingDir:      cfg.workspace,
			RequireApproval: true,
			MaxTimeout:      shellTimeout,
			MaxOutputBytes:  1 << 20,
		},
		Backend: shelltool.ShellBackendLocal,
	})
	if err != nil {
		return nil, fmt.Errorf("build shell tool: %v", err)
	}
	return zenforge.New(zenforge.Config{
		Model: modelClient,
		Instructions: "Work the task with the tools you are given. Use read_file and write_file for " +
			"workspace files and run_shell for commands. Every run_shell call needs the operator's " +
			"approval and may pause the run until the decision arrives.",
		Tools: []zenforge.Tool{
			readFile,
			writeFile,
			runShell{inner: shell},
		},
		Events:      eventlogjsonl.New(cfg.stateDir),
		Checkpoints: checkpointjsonl.New(cfg.stateDir),
		Approval:    approvalBroker(cfg),
		MaxSteps:    maxSteps,
	}), nil
}

// approvalBroker decides how this process answers an approval request.
//
// A phase the harness requires to pause gets no broker at all. That is not a
// limitation being worked around: it is the public-API way to leave a genuinely
// resumable run, and it is the same choice a deployment makes when no operator
// is reachable. Every other phase answers from BENCH_APPROVAL.
func approvalBroker(cfg config) approval.Broker {
	if cfg.phase == tasks.PhaseRun && cfg.requirePause {
		return nil
	}
	switch cfg.approval {
	case tasks.ApprovalReject:
		return approval.AlwaysDeny("the benchmark operator rejected this command")
	default:
		return approval.AlwaysAllow()
	}
}

// runID is the durable identity both phases agree on. It is derived from the
// task, not generated, because the resuming process has no other way to name
// the checkpoint its predecessor wrote: the harness restarts a process, not a
// conversation.
func runID(cfg config) string { return "bench-" + cfg.task }

// readFileInput is the read_file tool's schema.
type readFileInput struct {
	Path string `json:"path" jsonschema:"required,description=Workspace-relative path of the file to read"`
}

type readFileOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
}

// writeFileInput is the write_file tool's schema.
type writeFileInput struct {
	Path    string `json:"path" jsonschema:"required,description=Workspace-relative path of the file to write"`
	Content string `json:"content" jsonschema:"required,description=Exact content to write to the file"`
}

type writeFileOutput struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// runShell is the run_shell tool: the contract's one-argument command tool in
// front of the SDK's shell tool.
//
// The SDK's shell tool takes a `description` alongside the command and asks for
// approval itself through its policy review. Wrapping it keeps that behavior
// exactly -- the approval request, the decision metadata a retry carries, and
// the pause all remain the SDK's own -- while presenting the single argument
// every runner in the comparison exposes. The one visible seam is cosmetic: the
// approval request the operator sees names the SDK's tool ("shell") rather than
// this wrapper.
type runShell struct {
	inner tool.Tool
}

func (t runShell) Name() string { return tasks.ToolRunShell }

func (t runShell) Description() string {
	return "Run a shell command in the workspace. The operator approves each command before it runs."
}

func (t runShell) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to run in the workspace",
			},
		},
		"required":             []any{"command"},
		"additionalProperties": false,
	}
}

func (t runShell) Call(ctx context.Context, raw json.RawMessage, call tool.Context) (tool.Result, error) {
	var in runShellInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
				fmt.Errorf("%w: %v", tool.ErrInvalidArguments, err)
		}
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return tool.Result{Error: tool.ErrInvalidArguments.Error(), ExitCode: 1},
			fmt.Errorf("%w: command is required", tool.ErrInvalidArguments)
	}
	arguments, err := json.Marshal(map[string]any{
		"command":     command,
		"description": "Benchmark task command: " + command,
	})
	if err != nil {
		return tool.Result{Error: err.Error(), ExitCode: 1}, err
	}
	return t.inner.Call(ctx, arguments, call)
}

type runShellInput struct {
	Command string `json:"command"`
}

// writeResult writes the protocol's result JSON. A runner that cannot write it
// has failed whatever it did before, which is why the caller treats a write
// error as the process's own failure.
func writeResult(cfg config, status, detail string) error {
	payload := result{
		Task:      cfg.task,
		Phase:     cfg.phase,
		Status:    status,
		Detail:    oneLine(detail),
		Framework: frameworkName,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.resultPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cfg.resultPath, append(data, '\n'), 0o644)
}

// oneLine keeps detail a single line, as the protocol says it is.
func oneLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
}

func exitForStatus(status string) int {
	switch status {
	case statusCompleted:
		return exitCompleted
	case statusPaused:
		return exitPaused
	case statusUnsupported:
		return exitUnsupported
	default:
		return exitFailed
	}
}

func payloadText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
