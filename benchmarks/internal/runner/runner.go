// Package runner implements the benchmark's subprocess protocol: it builds the
// documented environment for one framework runner, starts it, maps its exit
// code to a status, reads and validates the result JSON it was required to
// write, captures its stderr, and measures wall-clock.
//
// The protocol is the benchmark's language boundary. A runner may be a Go
// program in this module, a Python program in another directory, or an
// independent Go module, because the only things that cross between it and the
// harness are environment variables in and one JSON file out. That is why this
// package has no knowledge of any framework: it starts a command and reads a
// file.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Status is a runner's outcome for one process.
type Status string

const (
	// StatusCompleted means the runner finished its work (exit 0).
	StatusCompleted Status = "completed"
	// StatusPaused means the runner stopped durably with work left, and a
	// later process with BENCH_PHASE=resume can finish it (exit 75).
	StatusPaused Status = "paused"
	// StatusUnsupported means the framework cannot do what the task requires
	// (exit 78). The harness records the cell as unsupported, not as a
	// failure, because a missing capability is a result of the comparison.
	StatusUnsupported Status = "unsupported"
	// StatusFailed means the runner tried and did not finish (any other exit
	// code, or a result file that does not hold together).
	StatusFailed Status = "failed"
	// StatusUnavailable means the harness could not even start the runner: no
	// interpreter, no binary, no module. It is not the framework's fault, so
	// it is reported separately, with the command that installs it.
	StatusUnavailable Status = "unavailable"
)

// Exit codes are the sysexits.h values the contract fixes.
const (
	ExitCompleted   = 0
	ExitPaused      = 75
	ExitUnsupported = 78
	ExitFailed      = 1
)

// StatusForExitCode maps a runner process's exit code to a status. Every code
// other than the three the contract names is a failure, including a signal
// (which the OS reports as -1).
func StatusForExitCode(code int) Status {
	switch code {
	case ExitCompleted:
		return StatusCompleted
	case ExitPaused:
		return StatusPaused
	case ExitUnsupported:
		return StatusUnsupported
	default:
		return StatusFailed
	}
}

// Result is the JSON a runner writes to BENCH_RESULT.
type Result struct {
	Task      string `json:"task"`
	Phase     string `json:"phase"`
	Status    Status `json:"status"`
	Detail    string `json:"detail"`
	Framework string `json:"framework"`
}

// Spec is one runner process to start.
type Spec struct {
	// RunnerID is the registered runner the harness is running. It is carried
	// for diagnostics only; the protocol itself is framework-agnostic.
	RunnerID string
	// Task and Phase are written to BENCH_TASK and BENCH_PHASE, and the result
	// JSON must agree with them, so a runner cannot claim a phase it did not
	// run.
	Task  string
	Phase string
	// Command is the argv to start, Dir its working directory.
	Command []string
	Dir     string
	// BaseURL, APIKey and Model point the runner at the scripted endpoint. The
	// model id is identical for every framework.
	BaseURL string
	APIKey  string
	Model   string
	// Query is the task's frozen user message. Every runner sends exactly this
	// text, because prompt bytes are a reported cost metric: a runner that
	// invented its own task sentence could win the cost column by writing a
	// shorter one.
	Query string
	// Workspace and StateDir are fresh directories the harness owns.
	Workspace string
	StateDir  string
	// Approval is "approve" or "reject": how the runner must answer an
	// approval request.
	Approval string
	// RequirePause sets BENCH_REQUIRE_PAUSE=1 when the harness needs a durable
	// pause after this process.
	RequirePause bool
	// ResultPath is the absolute file the runner must write its result JSON to.
	ResultPath string
	// Install is the command that installs this runner's dependencies. It is
	// named when the runner turns out to be unavailable.
	Install string
	// ExtraEnv is appended after the protocol variables, for a runner that
	// needs something else (an interpreter path, for instance).
	ExtraEnv []string
	// StderrLimit bounds how much of the runner's stderr the harness keeps.
	// Zero selects a sane default; the tail is what matters when a run fails.
	StderrLimit int
}

// Outcome is what happened to one runner process.
type Outcome struct {
	// Phase is the phase this process was asked to run, echoed back so a
	// report can place the outcome without re-deriving it from the spec.
	Phase     string
	Status    Status
	Detail    string
	Framework string
	ExitCode  int
	WallClock time.Duration
	Stderr    string
	// Install is the install command the harness names when the runner is
	// unavailable.
	Install string
	// Result is the parsed BENCH_RESULT, when there was one.
	Result *Result
}

// Environment is the protocol environment for this spec, as the contract
// documents it. It is exported so a caller can log or test exactly what a
// runner was told.
func (s Spec) Environment() []string {
	env := []string{
		"BENCH_BASE_URL=" + s.BaseURL,
		"BENCH_API_KEY=" + s.APIKey,
		"BENCH_MODEL=" + s.Model,
		"BENCH_TASK=" + s.Task,
		"BENCH_QUERY=" + s.Query,
		"BENCH_WORKSPACE=" + s.Workspace,
		"BENCH_STATE_DIR=" + s.StateDir,
		"BENCH_PHASE=" + s.Phase,
		"BENCH_APPROVAL=" + s.Approval,
		"BENCH_RESULT=" + s.ResultPath,
	}
	if s.RequirePause {
		env = append(env, "BENCH_REQUIRE_PAUSE=1")
	}
	return append(env, s.ExtraEnv...)
}

// Run starts one runner process and reports its outcome. A missing executable
// is StatusUnavailable with the spec's install command; every other start
// failure is StatusFailed.
func Run(ctx context.Context, spec Spec) Outcome {
	outcome := Outcome{Status: StatusFailed, Install: spec.Install, Phase: spec.Phase}
	if len(spec.Command) == 0 {
		outcome.Status = StatusUnavailable
		outcome.Detail = "runner registration has no command"
		return outcome
	}
	path, err := exec.LookPath(spec.Command[0])
	if err != nil {
		outcome.Status = StatusUnavailable
		outcome.Detail = fmt.Sprintf("%s is not installed or not on PATH", spec.Command[0])
		return outcome
	}

	// A stale result from an earlier phase must never be mistaken for this
	// phase's answer.
	if spec.ResultPath != "" {
		_ = os.Remove(spec.ResultPath)
	}

	stderr := newTailBuffer(spec.stderrLimit())
	command := exec.CommandContext(ctx, path, spec.Command[1:]...)
	command.Dir = spec.Dir
	command.Env = append(os.Environ(), spec.Environment()...)
	command.Stdout = io.Discard
	command.Stderr = stderr

	started := time.Now()
	runErr := command.Run()
	outcome.WallClock = time.Since(started)
	outcome.Stderr = stderr.String()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(runErr, &exitErr):
			exitCode = exitErr.ExitCode()
		case errors.Is(runErr, exec.ErrNotFound):
			outcome.Status = StatusUnavailable
			outcome.Detail = fmt.Sprintf("%s is not installed or not on PATH", spec.Command[0])
			return outcome
		default:
			// The process never started, or the context was cancelled. Either
			// way the runner did not produce an answer.
			outcome.Detail = fmt.Sprintf("start %s: %v", spec.Command[0], runErr)
			return outcome
		}
	}
	outcome.ExitCode = exitCode
	outcome.Status = StatusForExitCode(exitCode)

	result, err := readResult(spec.ResultPath)
	if err != nil {
		outcome.Status = StatusFailed
		outcome.Detail = fmt.Sprintf("exit %d, but %v", exitCode, err)
		return outcome
	}
	if result.Task != spec.Task || result.Phase != spec.Phase {
		outcome.Status = StatusFailed
		outcome.Detail = fmt.Sprintf("result names task %q phase %q, but this process ran task %q phase %q",
			result.Task, result.Phase, spec.Task, spec.Phase)
		return outcome
	}
	if result.Status != outcome.Status {
		outcome.Status = StatusFailed
		outcome.Detail = fmt.Sprintf("exit code %d means %s, but the result reports %s",
			exitCode, outcome.Status, result.Status)
		return outcome
	}
	outcome.Result = &result
	outcome.Framework = result.Framework
	outcome.Detail = strings.TrimSpace(result.Detail)
	if outcome.Detail == "" {
		outcome.Detail = fmt.Sprintf("exit %d", exitCode)
	}
	return outcome
}

func (s Spec) stderrLimit() int {
	if s.StderrLimit > 0 {
		return s.StderrLimit
	}
	return 64 << 10
}

// readResult reads and decodes the runner's result file.
func readResult(path string) (Result, error) {
	if strings.TrimSpace(path) == "" {
		return Result{}, errors.New("no BENCH_RESULT path was configured")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("BENCH_RESULT was not written (%v)", err)
	}
	var result Result
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("BENCH_RESULT is not valid result JSON: %v", err)
	}
	if strings.TrimSpace(result.Task) == "" || strings.TrimSpace(result.Phase) == "" || result.Status == "" {
		return Result{}, errors.New("BENCH_RESULT is missing task, phase, or status")
	}
	switch result.Status {
	case StatusCompleted, StatusPaused, StatusUnsupported, StatusFailed:
	default:
		return Result{}, fmt.Errorf("BENCH_RESULT reports unknown status %q", result.Status)
	}
	return result, nil
}

// tailBuffer keeps the first N bytes written and records that it truncated.
// Kept simple on purpose: the first lines of a Go panic or a Python traceback
// are the ones that name the cause, so the head is at least as useful as the
// tail.
type tailBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) <= remaining {
			b.buffer.Write(data)
		} else {
			b.buffer.Write(data[:remaining])
			b.truncated = true
		}
	} else {
		b.truncated = true
	}
	return len(data), nil
}

func (b *tailBuffer) String() string {
	if !b.truncated {
		return b.buffer.String()
	}
	return b.buffer.String() + "\n[stderr truncated]"
}
