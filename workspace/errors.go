package workspace

import (
	"errors"
	"fmt"
	"io/fs"
)

var (
	ErrPathEscape = errors.New("workspace path escapes root")
	// ErrPathNotFound wraps fs.ErrNotExist so hosts can classify a
	// missing path with errors.Is(err, fs.ErrNotExist) as well as with
	// the workspace sentinel.
	ErrPathNotFound    = fmt.Errorf("workspace path not found: %w", fs.ErrNotExist)
	ErrReadTooLarge    = errors.New("workspace read too large")
	ErrWriteTooLarge   = errors.New("workspace write too large")
	ErrBinaryFile      = errors.New("workspace binary file")
	ErrUnsupportedFile = errors.New("workspace unsupported file type")
	ErrInvalidPattern  = errors.New("workspace invalid grep pattern")
)
