//go:build !linux

package linuxsandbox

import (
	"fmt"
	"runtime"
)

// defaultExec refuses off Linux. The helper never gets this far in
// practice, because landlock.ABIVersion already fails, but the error makes
// the reason explicit rather than panicking.
func defaultExec(string, []string, []string) error {
	return fmt.Errorf("linux sandbox helper cannot run on %s", runtime.GOOS)
}
