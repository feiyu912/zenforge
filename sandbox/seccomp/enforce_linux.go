//go:build linux

package seccomp

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// prSetNoNewPrivs and prSetSeccomp are PR_SET_NO_NEW_PRIVS and
// PR_SET_SECCOMP. A seccomp filter requires no-new-privileges so the
// process cannot escape it by gaining privileges through setuid or file
// capabilities.
const (
	prSetNoNewPrivs = 38
	prSetSeccomp    = 22
)

// seccompModeFilter is SECCOMP_MODE_FILTER.
const seccompModeFilter = 2

// ErrUnsupported reports a kernel without seccomp filter support.
var ErrUnsupported = errors.New("seccomp filter mode is not supported by this kernel")

// rawInstruction is struct sock_filter.
type rawInstruction struct {
	Code uint16
	JT   uint8
	JF   uint8
	K    uint32
}

// sockFprog is struct sock_fprog.
type sockFprog struct {
	Len    uint16
	_      [6]byte // padding to the pointer alignment
	Filter *rawInstruction
}

// Apply installs the filter on the calling thread. Every process spawned
// afterwards inherits it, so a caller that intends to run a command must
// apply the filter and then exec.
func Apply(filter Filter) error {
	if len(filter.Instructions) == 0 {
		return fmt.Errorf("seccomp: filter has no instructions")
	}
	if err := unix.Prctl(prSetNoNewPrivs, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	instructions := make([]rawInstruction, len(filter.Instructions))
	for index, instruction := range filter.Instructions {
		instructions[index] = rawInstruction{
			Code: instruction.Code,
			JT:   instruction.JT,
			JF:   instruction.JF,
			K:    instruction.K,
		}
	}
	program := sockFprog{Len: uint16(len(instructions)), Filter: &instructions[0]}
	if err := unix.Prctl(prSetSeccomp, seccompModeFilter, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_SECCOMP): %w", err)
	}
	return nil
}

// Available reports whether seccomp filter mode can be used.
//
// A seccomp filter cannot be removed once installed, so probing by
// installing one would permanently restrict (and permanently set
// no-new-privileges on) the calling process. The probe is therefore a read
// of the kernel's advertised seccomp actions, which has no side effects.
func Available() error {
	data, err := os.ReadFile("/proc/sys/kernel/seccomp/actions_avail")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if !strings.Contains(string(data), "errno") {
		return fmt.Errorf("%w: the kernel does not list the errno action", ErrUnsupported)
	}
	return nil
}

// Exec applies the filter and replaces the current process with the
// command. It never returns on success.
func Exec(filter Filter, path string, argv []string, env []string) error {
	if path == "" {
		return fmt.Errorf("seccomp: executable path is required")
	}
	if err := Apply(filter); err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}
