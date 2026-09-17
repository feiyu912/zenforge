//go:build linux && arm64

package seccomp

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestHostSyscallTableIsTheKernelABI(t *testing.T) {
	checkHostSyscallTable(t, archARM64, map[string]int{
		"socket": unix.SYS_SOCKET, "connect": unix.SYS_CONNECT, "accept": unix.SYS_ACCEPT, "sendto": unix.SYS_SENDTO,
		"recvfrom": unix.SYS_RECVFROM, "sendmsg": unix.SYS_SENDMSG, "recvmsg": unix.SYS_RECVMSG, "shutdown": unix.SYS_SHUTDOWN,
		"bind": unix.SYS_BIND, "listen": unix.SYS_LISTEN, "getsockname": unix.SYS_GETSOCKNAME, "getpeername": unix.SYS_GETPEERNAME,
		"socketpair": unix.SYS_SOCKETPAIR, "setsockopt": unix.SYS_SETSOCKOPT, "getsockopt": unix.SYS_GETSOCKOPT,
		"accept4": unix.SYS_ACCEPT4, "recvmmsg": unix.SYS_RECVMMSG, "sendmmsg": unix.SYS_SENDMMSG,
		"ptrace": unix.SYS_PTRACE, "process_vm_readv": unix.SYS_PROCESS_VM_READV, "process_vm_writev": unix.SYS_PROCESS_VM_WRITEV,
		"io_uring_setup": unix.SYS_IO_URING_SETUP, "io_uring_enter": unix.SYS_IO_URING_ENTER, "io_uring_register": unix.SYS_IO_URING_REGISTER,
	})
}

func TestHostAuditArchIsTheKernelABI(t *testing.T) {
	if archARM64.AuditArch != unix.AUDIT_ARCH_AARCH64 {
		t.Fatalf("arm64 audit arch = %#x, but the headers say %#x", archARM64.AuditArch, unix.AUDIT_ARCH_AARCH64)
	}
}

func hostErrnoEPERM() int { return int(unix.EPERM) }

func hostAFUnix() int { return unix.AF_UNIX }
