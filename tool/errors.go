package tool

import "errors"

var (
	ErrToolNotFound     = errors.New("tool not found")
	ErrDuplicateTool    = errors.New("duplicate tool")
	ErrInvalidTool      = errors.New("invalid tool")
	ErrInvalidArguments = errors.New("invalid arguments")
	ErrTimeout          = errors.New("tool timeout")
	ErrBudgetExceeded   = errors.New("tool budget exceeded")
	ErrOutputTooLarge   = errors.New("tool output too large")
)
