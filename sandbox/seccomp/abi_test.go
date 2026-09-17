package seccomp

import "testing"

// expectedSyscalls is a second, independent statement of the Linux syscall
// ABI, written out by hand so the tables in filter.go are checked against
// something other than themselves.
//
// This is not busywork. A wrong syscall number in a security filter is a
// silent hole: the program denies the wrong syscall and lets the intended
// one through, and nothing in the run fails loudly. The numbers differ per
// architecture, which is exactly why the program checks the architecture
// first, so both tables have to be right independently.
var expectedSyscalls = map[string]map[string]int{
	// x86_64 (arch/x86/entry/syscalls/syscall_64.tbl).
	"amd64": {
		"socket": 41, "connect": 42, "accept": 43, "sendto": 44,
		"recvfrom": 45, "sendmsg": 46, "recvmsg": 47, "shutdown": 48,
		"bind": 49, "listen": 50, "getsockname": 51, "getpeername": 52,
		"socketpair": 53, "setsockopt": 54, "getsockopt": 55,
		"accept4": 288, "recvmmsg": 299, "sendmmsg": 307,
		"ptrace": 101, "process_vm_readv": 310, "process_vm_writev": 311,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
	},
	// arm64 (include/uapi/asm-generic/unistd.h): the socket family is a
	// contiguous block starting at 198, which is why the two tables look
	// nothing alike.
	"arm64": {
		"socket": 198, "connect": 203, "accept": 202, "sendto": 206,
		"recvfrom": 207, "sendmsg": 211, "recvmsg": 212, "shutdown": 210,
		"bind": 200, "listen": 201, "getsockname": 204, "getpeername": 205,
		"socketpair": 199, "setsockopt": 208, "getsockopt": 209,
		"accept4": 242, "recvmmsg": 243, "sendmmsg": 269,
		"ptrace": 117, "process_vm_readv": 270, "process_vm_writev": 271,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
	},
}

// TestSyscallTablesMatchTheLinuxABI checks every entry in both directions:
// a wrong number, a missing name, and an unexpected extra name are all
// failures. An extra name matters because a policy that denies it would
// silently skip the rule on this architecture.
func TestSyscallTablesMatchTheLinuxABI(t *testing.T) {
	for name, arch := range map[string]Arch{"amd64": archAMD64, "arm64": archARM64} {
		expected := expectedSyscalls[name]
		if len(expected) != len(arch.Syscalls) {
			t.Fatalf("%s has %d syscalls, the ABI has %d", name, len(arch.Syscalls), len(expected))
		}
		for syscall, number := range expected {
			got, ok := arch.Syscalls[syscall]
			if !ok {
				t.Fatalf("%s is missing the syscall %q (should be %d)", name, syscall, number)
			}
			if got != number {
				t.Fatalf("%s %s = %d, want %d", name, syscall, got, number)
			}
		}
		for syscall := range arch.Syscalls {
			if _, ok := expected[syscall]; !ok {
				t.Fatalf("%s has an unexpected syscall %q", name, syscall)
			}
		}
	}
}

// TestAuditArchValues pins the values the kernel reports in
// seccomp_data.arch. They are AUDIT_ARCH_64BIT | AUDIT_ARCH_LE | EM_*:
// 0x80000000 | 0x40000000 | 62 for x86_64 and | 183 for arm64. The
// architecture check is load-bearing -- without it a filter built from the
// wrong syscall table fails open -- so the constants it compares against
// cannot be approximate.
func TestAuditArchValues(t *testing.T) {
	if archAMD64.AuditArch != 0xc000003e {
		t.Fatalf("amd64 audit arch = %#x, want 0xc000003e", archAMD64.AuditArch)
	}
	if archARM64.AuditArch != 0xc00000b7 {
		t.Fatalf("arm64 audit arch = %#x, want 0xc00000b7", archARM64.AuditArch)
	}
	if archAMD64.AuditArch == archARM64.AuditArch {
		t.Fatal("both architectures report the same audit value")
	}
}

// TestSocketDomainAndActionConstants pins the remaining numeric constants:
// AF_UNIX is 1, SECCOMP_RET_ALLOW is 0x7fff0000, SECCOMP_RET_KILL_PROCESS is
// 0x80000000, and a denial is SECCOMP_RET_ERRNO (0x00050000) carrying the
// errno in the low 16 bits.
func TestSocketDomainAndActionConstants(t *testing.T) {
	if AFUnix != 1 {
		t.Fatalf("AFUnix = %d, want 1", AFUnix)
	}
	if RetAllow != 0x7fff0000 {
		t.Fatalf("RetAllow = %#x, want 0x7fff0000", RetAllow)
	}
	if RetKillProcess != 0x80000000 {
		t.Fatalf("RetKillProcess = %#x, want 0x80000000", RetKillProcess)
	}
	if got := RetErrno(1); got != 0x00050001 {
		t.Fatalf("RetErrno(1) = %#x, want 0x00050001", got)
	}
	// The errno is masked into 16 bits, so a bogus large value cannot land
	// in the action bits and turn a denial into an allow.
	if got := RetErrno(0x1_0001); got&0xffff0000 != 0x00050000 {
		t.Fatalf("RetErrno did not mask the errno: %#x", got)
	}
}

// TestOnlySupportedArchitecturesResolve documents the deliberate refusal: a
// hand-copied table for an architecture this project cannot build for would
// be a guess, and this is a security filter.
func TestOnlySupportedArchitecturesResolve(t *testing.T) {
	for _, name := range []string{"amd64", "x86_64", "arm64", "aarch64", "ARM64", " amd64 "} {
		if _, err := archByName(name); err != nil {
			t.Fatalf("archByName(%q) returned error: %v", name, err)
		}
	}
	for _, name := range []string{"", "386", "arm", "riscv64", "ppc64le", "mips"} {
		if _, err := archByName(name); err == nil {
			t.Fatalf("archByName(%q) was accepted", name)
		}
	}
}
