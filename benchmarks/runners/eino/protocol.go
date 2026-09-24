package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// This file implements the frozen runner protocol documented in
// benchmarks/README.md: the BENCH_* environment, the BENCH_RESULT JSON shape,
// and the four status values with their exit codes.
//
// A runner must not print a report itself; diagnostics go to stderr.

// Status values. Exactly these four strings are legal in BENCH_RESULT.status.
const (
	StatusCompleted   = "completed"
	StatusPaused      = "paused"
	StatusUnsupported = "unsupported"
	StatusFailed      = "failed"
)

// Exit codes paired with the statuses above.
const (
	ExitCompleted   = 0
	ExitFailed      = 1
	ExitPaused      = 75
	ExitUnsupported = 78
)

// Task ids.
const (
	TaskEditFile       = "edit-file"
	TaskApproveCommand = "approve-command"
	TaskDurableTask    = "durable-task"
)

// Phases.
const (
	PhaseRun    = "run"
	PhaseResume = "resume"
)

// Approval answers.
const (
	ApprovalApprove = "approve"
	ApprovalReject  = "reject"
)

// Framework reports the exact Eino version this binary was built against.
var frameworkName = "eino " + einoVersion()

func einoVersion() string {
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, d := range info.Deps {
			if d.Path == "github.com/cloudwego/eino" {
				return strings.TrimPrefix(d.Version, "v")
			}
		}
	}
	return "unknown"
}

// Result is the exact JSON shape the harness reads from BENCH_RESULT.
type Result struct {
	Task      string `json:"task"`
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
	Framework string `json:"framework"`
}

// ExitCode maps a status to its documented process exit code.
func (r Result) ExitCode() int {
	switch r.Status {
	case StatusCompleted:
		return ExitCompleted
	case StatusPaused:
		return ExitPaused
	case StatusUnsupported:
		return ExitUnsupported
	default:
		return ExitFailed
	}
}

// NewResult fills the fields every result carries.
func NewResult(task, phase, status, detail string) Result {
	return Result{
		Task:      task,
		Phase:     phase,
		Status:    status,
		Detail:    oneLine(detail),
		Framework: frameworkName,
	}
}

// Config is the runner configuration read from the documented environment.
type Config struct {
	BaseURL      string
	APIKey       string
	Model        string
	Task         string
	Workspace    string
	StateDir     string
	Phase        string
	Approval     string
	RequirePause bool
	ResultPath   string
	// Query is the frozen task instruction the harness passes in BENCH_QUERY. It
	// is sent verbatim as the user message; see userQuery in agent.go.
	Query string
	// QueryFromEnv records whether Query came from the environment or from this
	// runner's fallback, so the difference is never silent.
	QueryFromEnv bool
}

// CheckpointID is the durable key this runner uses inside BENCH_STATE_DIR.
// It is derived from the task id so that the resume process, which receives
// the same BENCH_TASK, addresses the same checkpoint without extra input.
func (c Config) CheckpointID() string {
	return "zenforge-bench-" + c.Task
}

// LoadConfig reads and validates the BENCH_* environment. It returns an error
// naming the first missing or malformed variable, so a harness mistake is
// reported instead of silently defaulting.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{
		BaseURL:      strings.TrimSpace(getenv("BENCH_BASE_URL")),
		APIKey:       getenv("BENCH_API_KEY"),
		Model:        strings.TrimSpace(getenv("BENCH_MODEL")),
		Task:         strings.TrimSpace(getenv("BENCH_TASK")),
		Workspace:    strings.TrimSpace(getenv("BENCH_WORKSPACE")),
		StateDir:     strings.TrimSpace(getenv("BENCH_STATE_DIR")),
		Phase:        strings.TrimSpace(getenv("BENCH_PHASE")),
		Approval:     strings.TrimSpace(getenv("BENCH_APPROVAL")),
		RequirePause: getenv("BENCH_REQUIRE_PAUSE") == "1",
		ResultPath:   strings.TrimSpace(getenv("BENCH_RESULT")),
	}
	// BENCH_QUERY is the frozen task instruction. It is used verbatim, and only
	// leading/trailing whitespace is stripped; nothing else about it is altered.
	c.Query = strings.TrimSpace(getenv("BENCH_QUERY"))
	c.QueryFromEnv = c.Query != ""

	switch c.Task {
	case TaskEditFile, TaskApproveCommand, TaskDurableTask:
	default:
		return Config{}, fmt.Errorf("BENCH_TASK must be one of %q, %q, %q; got %q",
			TaskEditFile, TaskApproveCommand, TaskDurableTask, c.Task)
	}

	if c.Phase == "" {
		c.Phase = PhaseRun
	}
	switch c.Phase {
	case PhaseRun, PhaseResume:
	default:
		return Config{}, fmt.Errorf("BENCH_PHASE must be %q or %q; got %q", PhaseRun, PhaseResume, c.Phase)
	}

	if c.Approval == "" {
		c.Approval = ApprovalApprove
	}
	switch c.Approval {
	case ApprovalApprove, ApprovalReject:
	default:
		return Config{}, fmt.Errorf("BENCH_APPROVAL must be %q or %q; got %q",
			ApprovalApprove, ApprovalReject, c.Approval)
	}

	for _, f := range []struct{ name, value string }{
		{"BENCH_BASE_URL", c.BaseURL},
		{"BENCH_MODEL", c.Model},
		{"BENCH_WORKSPACE", c.Workspace},
		{"BENCH_STATE_DIR", c.StateDir},
		{"BENCH_RESULT", c.ResultPath},
	} {
		if f.value == "" {
			return Config{}, fmt.Errorf("%s is required", f.name)
		}
	}
	for _, f := range []struct{ name, value string }{
		{"BENCH_WORKSPACE", c.Workspace},
		{"BENCH_STATE_DIR", c.StateDir},
		{"BENCH_RESULT", c.ResultPath},
	} {
		if !filepath.IsAbs(f.value) {
			return Config{}, fmt.Errorf("%s must be an absolute path; got %q", f.name, f.value)
		}
	}

	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return Config{}, fmt.Errorf("creating BENCH_STATE_DIR: %w", err)
	}
	if st, err := os.Stat(c.Workspace); err != nil {
		return Config{}, fmt.Errorf("BENCH_WORKSPACE: %w", err)
	} else if !st.IsDir() {
		return Config{}, fmt.Errorf("BENCH_WORKSPACE %q is not a directory", c.Workspace)
	}

	return c, nil
}

// WriteResult writes the result JSON to path, replacing any previous file.
func WriteResult(path string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, b, 0o644)
}

// oneLine keeps detail free-form but guaranteed single-line and bounded.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.Join(strings.Fields(s), " ")
	const max = 400
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
