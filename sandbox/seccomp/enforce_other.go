//go:build !linux

package seccomp

import (
	"fmt"
	"runtime"
)

// ErrUnsupported reports a kernel without seccomp filter support. Seccomp
// is Linux only, so on every other platform it is always this error.
var ErrUnsupported = fmt.Errorf("seccomp is not supported on %s", runtime.GOOS)

// Available reports that seccomp is unavailable off Linux. Planning a
// filter still works everywhere, so a policy can be validated on any host.
func Available() error {
	return ErrUnsupported
}

// Apply refuses off Linux: installing a seccomp filter is a Linux-only
// kernel operation.
func Apply(Filter) error {
	return ErrUnsupported
}

// Exec refuses off Linux.
func Exec(Filter, string, []string, []string) error {
	return ErrUnsupported
}
