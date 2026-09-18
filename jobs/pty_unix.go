//go:build unix

package jobs

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// startTerminal starts a command on a new pseudo-terminal and returns the
// master, which carries input and output as one stream. The child becomes a
// session leader with the terminal as its controlling tty.
func startTerminal(command *exec.Cmd, rows, cols int) (*os.File, error) {
	return pty.StartWithSize(command, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// terminateProcessGroup kills a terminal job's whole process group. A PTY
// child starts a new session, so its process group id is its pid and the
// negative pid addresses the group: a foreground child the shell spawned
// would otherwise keep the terminal open after the shell itself was killed,
// and the job would not settle until the drain grace expired.
func terminateProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
