package workspace

import "errors"

var (
	ErrPathEscape     = errors.New("workspace path escapes root")
	ErrPathNotFound   = errors.New("workspace path not found")
	ErrReadTooLarge   = errors.New("workspace read too large")
	ErrWriteTooLarge  = errors.New("workspace write too large")
	ErrBinaryFile     = errors.New("workspace binary file")
	ErrInvalidPattern = errors.New("workspace invalid grep pattern")
)
