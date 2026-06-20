package sandbox

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	ErrSandboxUnavailable  = ErrorCode("sandbox_unavailable")
	ErrEnvironmentNotFound = ErrorCode("environment_not_found")
	ErrSessionOpenFailed   = ErrorCode("session_open_failed")
	ErrExecuteFailed       = ErrorCode("sandbox_execute_failed")
	ErrTimeout             = ErrorCode("sandbox_timeout")
	ErrClosed              = ErrorCode("sandbox_closed")
)

type ErrorCode string

func (e ErrorCode) Error() string {
	return string(e)
}

func Code(err error) ErrorCode {
	var code ErrorCode
	if errors.As(err, &code) {
		return code
	}
	return ""
}

type Sandbox interface {
	Open(ctx context.Context, req OpenRequest) (*Session, error)
	Execute(ctx context.Context, session *Session, req ExecuteRequest) (ExecuteResult, error)
	Close(ctx context.Context, session *Session) error
}

type OpenRequest struct {
	RunID         string            `json:"runId"`
	SubtaskID     string            `json:"subtaskId,omitempty"`
	EnvironmentID string            `json:"environmentId,omitempty"`
	WorkingDir    string            `json:"workingDir,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Mounts        []Mount           `json:"mounts,omitempty"`
	Metadata      map[string]any    `json:"metadata,omitempty"`
}

type Session struct {
	ID            string         `json:"id"`
	RunID         string         `json:"runId"`
	SubtaskID     string         `json:"subtaskId,omitempty"`
	EnvironmentID string         `json:"environmentId,omitempty"`
	WorkingDir    string         `json:"workingDir,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

type ExecuteRequest struct {
	Command  string            `json:"command"`
	CWD      string            `json:"cwd,omitempty"`
	Timeout  time.Duration     `json:"timeout,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Metadata map[string]any    `json:"metadata,omitempty"`
}

type ExecuteResult struct {
	ExitCode         int            `json:"exitCode"`
	Stdout           string         `json:"stdout,omitempty"`
	Stderr           string         `json:"stderr,omitempty"`
	WorkingDirectory string         `json:"workingDirectory,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type Mount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode,omitempty"`
}

func SessionKey(runID, subtaskID string) string {
	runID = strings.TrimSpace(runID)
	subtaskID = strings.TrimSpace(subtaskID)
	if runID == "" {
		return ""
	}
	if subtaskID == "" {
		return "run-" + runID
	}
	return "run-" + runID + "-" + subtaskID
}
