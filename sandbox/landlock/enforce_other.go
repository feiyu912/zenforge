//go:build !linux

package landlock

import (
	"fmt"
	"runtime"
)

// ErrUnsupported reports a kernel without Landlock support. Landlock is
// Linux only, so on every other platform it is always this error.
var ErrUnsupported = fmt.Errorf("landlock is not supported on %s", runtime.GOOS)

// ABIVersion reports that Landlock is unavailable off Linux.
func ABIVersion() (int, error) {
	return 0, ErrUnsupported
}

// Available reports that Landlock is unavailable off Linux. Planning a
// ruleset still works everywhere, so a policy can be validated on any host.
func Available() error {
	return ErrUnsupported
}

// Restrict refuses off Linux: applying a Landlock ruleset is a Linux-only
// kernel operation.
func Restrict(Ruleset) error {
	return ErrUnsupported
}

// Exec refuses off Linux.
func Exec(Ruleset, string, []string, []string) error {
	return ErrUnsupported
}
