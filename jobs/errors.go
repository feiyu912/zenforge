package jobs

import "errors"

// errorsAs is a tiny indirection so job.go does not import errors twice.
func errorsAs(err error, target **ErrNotFound) bool {
	return errors.As(err, target)
}
