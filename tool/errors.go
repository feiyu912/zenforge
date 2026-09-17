package tool

import (
	"errors"
	"fmt"
)

var (
	ErrToolNotFound     = errors.New("tool not found")
	ErrDuplicateTool    = errors.New("duplicate tool")
	ErrInvalidTool      = errors.New("invalid tool")
	ErrInvalidArguments = errors.New("invalid arguments")
	ErrTimeout          = errors.New("tool timeout")
	// TimeoutCode is the structured result code carried when a
	// cooperative tool-call deadline expires, mirroring the DSH
	// TOOL_TIMEOUT classification.
	TimeoutCode       = "TOOL_TIMEOUT"
	ErrBudgetExceeded = errors.New("tool budget exceeded")
	ErrOutputTooLarge = errors.New("tool output too large")
)

type retryableError struct {
	err error
}

func (e retryableError) Error() string {
	return e.err.Error()
}

func (e retryableError) Unwrap() error {
	return e.err
}

func (retryableError) Retryable() bool {
	return true
}

// MarkRetryable opts an error into Retry middleware. Nil remains nil.
func MarkRetryable(err error) error {
	if err == nil {
		return nil
	}
	return retryableError{err: fmt.Errorf("transient tool error: %w", err)}
}
