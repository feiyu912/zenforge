// Package jobs manages long-running commands for the shell tool: a job is
// a command that keeps running while the agent continues, with bounded
// output the agent polls by offset and can interrupt. It is the Go
// equivalent of the reference's unified execution model, where one
// interface starts a command in the background, feeds it input, reads its
// output, and stops it, instead of blocking the whole turn on a dev server
// or a watch loop.
//
// The manager owns process lifetime and buffering; the tools in
// tools/jobs own the model-facing schema. Nothing here is persisted: a job
// belongs to the process that started it, and a resumed run sees the jobs
// its checkpoint described only through its own tool metadata.
package jobs

import (
	"fmt"
	"strings"
	"time"
)

// Status is a job's lifecycle state.
type Status string

const (
	// StatusRunning means the process is alive.
	StatusRunning Status = "running"
	// StatusExited means the process finished on its own.
	StatusExited Status = "exited"
	// StatusFailed means the process could not be started or the manager
	// lost track of it.
	StatusFailed Status = "failed"
	// StatusKilled means the job was stopped by Kill or by its timeout.
	StatusKilled Status = "killed"
)

// Terminal reports whether no further output can arrive.
func (s Status) Terminal() bool {
	return s == StatusExited || s == StatusFailed || s == StatusKilled
}

// Spec describes one command to run.
type Spec struct {
	// Command is the shell command line.
	Command string `json:"command"`
	// CWD is the working directory; it must be absolute when set.
	CWD string `json:"cwd,omitempty"`
	// Env is the process environment. Empty inherits the manager's.
	Env []string `json:"-"`
	// Stdin is optional input written before the process is read from.
	Stdin string `json:"stdin,omitempty"`
	// Timeout bounds the job's lifetime. Zero uses the manager default; a
	// negative timeout disables the limit.
	Timeout time.Duration `json:"timeout,omitempty"`
	// MaxOutputBytes caps each stream; zero uses the manager default.
	MaxOutputBytes int `json:"maxOutputBytes,omitempty"`
}

// Validate rejects a spec that cannot be run.
func (s Spec) Validate() error {
	if strings.TrimSpace(s.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if s.CWD != "" && !strings.HasPrefix(s.CWD, "/") {
		return fmt.Errorf("cwd %q must be absolute", s.CWD)
	}
	return nil
}

// Job is a snapshot of one managed command.
type Job struct {
	// ID is the manager-assigned identifier.
	ID string `json:"id"`
	// Spec is the spec the job was started from.
	Spec Spec `json:"spec"`
	// Status is the lifecycle state.
	Status Status `json:"status"`
	// StartedAt and EndedAt bracket the job's life.
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	// ExitCode is set once the process exited.
	ExitCode *int `json:"exitCode,omitempty"`
	// Error explains a failed or killed job.
	Error string `json:"error,omitempty"`
	// PID is the process id while running.
	PID int `json:"pid,omitempty"`
	// StdoutTotal and StderrTotal are the bytes produced on each stream.
	StdoutTotal int64 `json:"stdoutTotal"`
	StderrTotal int64 `json:"stderrTotal"`
	// StdoutDropped and StderrDropped report whether either stream lost
	// bytes to the ring buffer, so a reader is never misled about having
	// seen everything.
	StdoutDropped bool `json:"stdoutDropped,omitempty"`
	StderrDropped bool `json:"stderrDropped,omitempty"`
	// Duration is how long the job ran, or has been running.
	Duration time.Duration `json:"duration"`
}

// Running reports whether the job is still alive.
func (j Job) Running() bool { return j.Status == StatusRunning }

// Summary is a one-line description for tool output.
func (j Job) Summary() string {
	state := string(j.Status)
	if j.ExitCode != nil {
		state = fmt.Sprintf("%s (%d)", state, *j.ExitCode)
	}
	return fmt.Sprintf("%s %s [%s] %.1fs", j.ID, strings.TrimSpace(j.Spec.Command), state, j.Duration.Seconds())
}

// ErrNotFound reports an unknown job id.
type ErrNotFound struct{ ID string }

func (e *ErrNotFound) Error() string { return fmt.Sprintf("job %s not found", e.ID) }

// IsNotFound reports whether err is an unknown-job error.
func IsNotFound(err error) bool {
	var notFound *ErrNotFound
	return errorsAs(err, &notFound)
}
