//go:build linux

package seccomp

import "testing"

// checkHostSyscallTable compares this architecture's table against the
// numbers golang.org/x/sys/unix compiles for it. On Linux that is an
// authoritative check of the real ABI -- the kernel headers as vendored by
// the Go project -- rather than a copy of the table being tested, so a CI
// run on either supported architecture verifies the numbers that actually
// reach the kernel.
func checkHostSyscallTable(t *testing.T, arch Arch, host map[string]int) {
	t.Helper()
	if len(host) != len(expectedSyscalls[arch.Name]) {
		t.Fatalf("%s: the host table has %d syscalls, the ABI has %d", arch.Name, len(host), len(expectedSyscalls[arch.Name]))
	}
	for syscall, number := range host {
		got, ok := arch.Syscalls[syscall]
		if !ok {
			t.Fatalf("%s is missing the syscall %q (the host says %d)", arch.Name, syscall, number)
		}
		if got != number {
			t.Fatalf("%s %s = %d, but golang.org/x/sys/unix says %d", arch.Name, syscall, got, number)
		}
	}
}

// TestErrnoAndDomainMatchTheLibc pins the two constants most likely to be
// misremembered against the values the runtime itself uses.
func TestErrnoAndDomainMatchTheLibc(t *testing.T) {
	if got := hostErrnoEPERM(); ErrnoEPERM != got {
		t.Fatalf("ErrnoEPERM = %d, but the libc says %d", ErrnoEPERM, got)
	}
	if got := hostAFUnix(); AFUnix != got {
		t.Fatalf("AFUnix = %d, but the libc says %d", AFUnix, got)
	}
}
