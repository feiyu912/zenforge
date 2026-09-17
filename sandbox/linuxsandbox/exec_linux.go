//go:build linux

package linuxsandbox

import "syscall"

// defaultExec replaces the helper process with the command.
func defaultExec(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env)
}
