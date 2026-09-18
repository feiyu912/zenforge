//go:build !unix

package jobs

import (
	"errors"
	"os"
	"os/exec"
)

// startTerminal has no implementation off Unix: this platform has no
// pseudo-terminal device for the manager to own, and pretending otherwise
// would run the command on pipes while reporting a terminal.
func startTerminal(*exec.Cmd, int, int) (*os.File, error) {
	return nil, errors.New("terminal jobs are not supported on this platform")
}

// terminateProcessGroup is a no-op off Unix: the platform has no process
// groups to signal, and the context cancel still stops the direct child.
func terminateProcessGroup(int) error { return nil }
