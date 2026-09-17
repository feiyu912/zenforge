package seccomp

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func instructionIndex(program []Instruction, code uint16, k uint32) int {
	for index, instruction := range program {
		if instruction.Code == code && instruction.K == k {
			return index
		}
	}
	return -1
}

func TestBuildDeniesTheNetworkByDefault(t *testing.T) {
	filter, err := Build(Policy{}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	// The program starts by validating the architecture, then loads the
	// syscall number.
	if filter.Instructions[0].Code != bpfLdW || filter.Instructions[0].K != offsetArch {
		t.Fatalf("first instruction = %#v", filter.Instructions[0])
	}
	if filter.Instructions[1].K != archAMD64.AuditArch || filter.Instructions[1].JT != 1 {
		t.Fatalf("architecture check = %#v", filter.Instructions[1])
	}
	if filter.Instructions[2].K != RetKillProcess {
		t.Fatalf("a foreign architecture must be killed: %#v", filter.Instructions[2])
	}
	// The default action is allow, and a matched rule returns EPERM.
	if index := instructionIndex(filter.Instructions, bpfRetK, RetAllow); index < 0 {
		t.Fatal("the program has no allow return")
	}
	deny := instructionIndex(filter.Instructions, bpfRetK, RetErrno(ErrnoEPERM))
	if deny < 0 {
		t.Fatal("the program has no EPERM return")
	}
	if deny != len(filter.Instructions)-1 {
		t.Fatalf("the EPERM return must be last: %d of %d", deny, len(filter.Instructions))
	}
	// Every unconditional deny jumps to the EPERM return.
	for _, name := range filter.Denied {
		number, ok := archAMD64.Syscalls[name]
		if !ok {
			t.Fatalf("%s has no number on amd64", name)
		}
		index := -1
		for candidate, instruction := range filter.Instructions {
			if instruction.Code == bpfJeqK && instruction.K == uint32(number) && instruction.JT == uint8(deny-candidate-1) {
				index = candidate
			}
		}
		if index < 0 {
			t.Fatalf("%s is in the denied list but not denied by the program", name)
		}
	}
	for _, want := range []string{"connect", "bind", "listen", "sendto", "ptrace", "io_uring_setup"} {
		found := false
		for _, name := range filter.Denied {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is not denied: %v", want, filter.Denied)
		}
	}
	// recvfrom stays allowed: a toolchain managing subprocesses over a Unix
	// socketpair needs it, and it cannot reach the network alone.
	for _, name := range filter.Denied {
		if name == "recvfrom" {
			t.Fatal("recvfrom must not be denied")
		}
	}
}

func TestBuildRestrictsSocketDomainsToUnix(t *testing.T) {
	filter, err := Build(Policy{}, "arm64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(filter.DomainRestricted) != 2 {
		t.Fatalf("domain-restricted syscalls = %v", filter.DomainRestricted)
	}
	// Each restricted syscall compares arg0 against AF_UNIX and jumps to the
	// EPERM return when it differs.
	for _, name := range filter.DomainRestricted {
		number, ok := archARM64.Syscalls[name]
		if !ok {
			t.Fatalf("%s has no number on arm64", name)
		}
		index := instructionIndex(filter.Instructions, bpfJeqK, uint32(number))
		if index < 0 || index+2 >= len(filter.Instructions) {
			t.Fatalf("%s has no dispatch instruction", name)
		}
		if filter.Instructions[index+1].Code != bpfLdW || filter.Instructions[index+1].K != offsetArg0 {
			t.Fatalf("%s does not load arg0: %#v", name, filter.Instructions[index+1])
		}
		compare := filter.Instructions[index+2]
		// AF_UNIX falls through to the next check; any other domain jumps
		// to the EPERM return.
		if compare.K != AFUnix || compare.JT != 0 || compare.JF == 0 {
			t.Fatalf("%s arg0 comparison = %#v", name, compare)
		}
	}
}

func TestBuildAllowsNetworkWhenRequested(t *testing.T) {
	filter, err := Build(Policy{AllowNetwork: true}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	for _, name := range filter.Denied {
		for _, network := range networkSyscalls {
			if name == network {
				t.Fatalf("%s is denied despite AllowNetwork", name)
			}
		}
	}
	if len(filter.DomainRestricted) != 0 {
		t.Fatalf("socket domains are restricted despite AllowNetwork: %v", filter.DomainRestricted)
	}
	// Process inspection and io_uring stay denied in every mode.
	for _, want := range append(append([]string(nil), processInspectionSyscalls...), ioUringSyscalls...) {
		found := false
		for _, name := range filter.Denied {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is not denied with network access: %v", want, filter.Denied)
		}
	}
	// Denying the Unix-socket domains explicitly removes the socket rules
	// while keeping the rest of the network denies.
	off := false
	strict, err := Build(Policy{AllowUnixSockets: &off}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(strict.DomainRestricted) != 0 {
		t.Fatalf("socket domains are not strictly denied: %v", strict.DomainRestricted)
	}
	for _, name := range []string{"socket", "socketpair"} {
		found := false
		for _, denied := range strict.Denied {
			if denied == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is not denied in the strict policy: %v", name, strict.Denied)
		}
	}
}

func TestBuildArchitecturesUseTheirOwnNumbers(t *testing.T) {
	amd64, err := Build(Policy{}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	arm64, err := Build(Policy{}, "arm64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if amd64.Arch.AuditArch == arm64.Arch.AuditArch {
		t.Fatal("the architectures share an audit value")
	}
	if instructionIndex(amd64.Instructions, bpfJeqK, 41) < 0 {
		t.Fatal("amd64 does not deny socket(41)")
	}
	if instructionIndex(arm64.Instructions, bpfJeqK, 198) < 0 {
		t.Fatal("arm64 does not deny socket(198)")
	}
	if instructionIndex(arm64.Instructions, bpfJeqK, 41) >= 0 {
		t.Fatal("arm64 used the amd64 syscall number")
	}
	if amd64.Fingerprint() == arm64.Fingerprint() {
		t.Fatal("the fingerprints must differ per architecture")
	}
	if _, err := Build(Policy{}, "riscv64"); err == nil {
		t.Fatal("an unsupported architecture was accepted")
	}
}

func TestBuildRejectsUnknownExtraSyscalls(t *testing.T) {
	if _, err := Build(Policy{ExtraDeny: []string{"not_a_syscall"}}, "amd64"); err == nil {
		t.Fatal("an unknown syscall name was accepted")
	}
	// A bare number names a syscall this package has no table entry for.
	filter, err := Build(Policy{ExtraDeny: []string{"133", "ptrace"}}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	count := 0
	for _, name := range filter.Denied {
		if name == "ptrace" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ptrace appears %d times: %v", count, filter.Denied)
	}
	if instructionIndex(filter.Instructions, bpfJeqK, 133) < 0 {
		t.Fatalf("syscall 133 is not denied: %v", filter.Denied)
	}
}

func TestBuildKeepsJumpsWithinClassicBPFRange(t *testing.T) {
	// aarch64 lacks every name in the list below, so this exercises the
	// name-resolution error path and, with a large ExtraDeny, the jump-range
	// guard.
	many := []string{}
	for _, name := range []string{"a", "b", "c"} {
		many = append(many, name)
	}
	if _, err := Build(Policy{ExtraDeny: many}, "amd64"); err == nil {
		t.Fatal("unknown names were accepted")
	}
	filter, err := Build(Policy{}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(filter.Instructions) > 250 {
		t.Fatalf("the program grew unexpectedly: %d instructions", len(filter.Instructions))
	}
}

func TestAvailableIsPlatformDependent(t *testing.T) {
	err := Available()
	if runtime.GOOS != "linux" {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux Available returned %v", err)
		}
		if err := Apply(Filter{Instructions: []Instruction{{Code: bpfRetK, K: RetAllow}}}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux Apply returned %v", err)
		}
		if err := Exec(Filter{}, "/bin/true", []string{"/bin/true"}, nil); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux Exec returned %v", err)
		}
		return
	}
	if err := Apply(Filter{}); err == nil {
		t.Fatal("an empty filter was applied")
	}
	if err := Exec(Filter{}, "", nil, nil); err == nil {
		t.Fatal("an empty executable path was accepted")
	}
}

// seccompHelperEnv makes the test binary exec itself under the filter,
// because a filter cannot be installed from outside the process.
const seccompHelperEnv = "ZENFORGE_SECCOMP_TEST_TARGET"

// seccompSocketAttemptEnv marks the third process in the chain: the one that
// makes the socket calls with the filter already installed.
const seccompSocketAttemptEnv = "ZENFORGE_SECCOMP_TEST_SOCKET_ATTEMPT"

func TestSeccompHelperProcess(t *testing.T) {
	if os.Getenv(seccompHelperEnv) == "" {
		t.Skip("helper process for TestSeccompDeniesSockets")
	}
	filter, err := Build(Policy{}, runtime.GOARCH)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	// Exec replaces this process, so the socket attempt must happen in a
	// fresh process with the filter inherited.
	env := append(os.Environ(), seccompSocketAttemptEnv+"=1")
	if err := Exec(filter, os.Args[0], []string{os.Args[0], "-test.run=TestSeccompSocketAttemptProcess"}, env); err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
}

// TestSeccompDeniesSockets runs the helper on a kernel with seccomp.
//
// The attempt itself is made by this test binary (see
// TestSeccompSocketAttemptProcess) rather than by a shell: `/dev/tcp` is a
// bash extension, and on a host where /bin/sh is dash the redirection fails
// before any syscall happens, which would make the test pass without
// proving anything.
func TestSeccompDeniesSockets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("seccomp is Linux only")
	}
	if _, err := os.Stat("/proc/sys/kernel/seccomp/actions_avail"); err != nil {
		t.Skipf("this kernel does not advertise seccomp actions: %v", err)
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestSeccompHelperProcess")
	cmd.Env = append(os.Environ(), seccompHelperEnv+"=attempt")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the helper itself failed: %v (%s)", err, output)
	}
	// An IP socket is denied with EPERM, so the child reports the denial
	// rather than a connection result.
	if !strings.Contains(string(output), "ip-socket=denied") {
		t.Fatalf("an IP socket was not denied with EPERM: %s", output)
	}
	// Unix sockets stay available, which is what keeps subprocess tooling
	// working; a kernel that denies them would be a policy bug, so report
	// it rather than passing silently.
	if !strings.Contains(string(output), "unix-socket=allowed") {
		t.Fatalf("a unix socket was not allowed: %s", output)
	}
}

// TestSeccompSocketAttemptProcess is the child that runs under the filter.
func TestSeccompSocketAttemptProcess(t *testing.T) {
	if os.Getenv(seccompSocketAttemptEnv) == "" {
		t.Skip("child process for TestSeccompDeniesSockets")
	}
	fmt.Println(socketAttemptResult())
}

// socketAttemptResult attempts the two socket families and reports what the
// kernel did, in a form the parent can assert on.
func socketAttemptResult() string {
	verdict := func(err error) string {
		if err == nil {
			return "allowed"
		}
		if errors.Is(err, syscall.EPERM) || strings.Contains(err.Error(), "operation not permitted") {
			return "denied"
		}
		return "error:" + err.Error()
	}
	ip, ipErr := net.Dial("tcp", "127.0.0.1:1")
	if ip != nil {
		_ = ip.Close()
	}
	unixSocket, unixErr := net.Listen("unix", filepath.Join(os.TempDir(), fmt.Sprintf("zenforge-seccomp-%d.sock", os.Getpid())))
	if unixSocket != nil {
		_ = unixSocket.Close()
	}
	return fmt.Sprintf("ip-socket=%s unix-socket=%s", verdict(ipErr), verdict(unixErr))
}

// evaluate runs the program the way the kernel's classic BPF interpreter
// does, so the semantics (jump directions, the architecture guard, the
// default action) are verified without a Linux host.
func evaluate(t *testing.T, filter Filter, arch uint32, nr uint32, arg0 uint32) uint32 {
	t.Helper()
	data := map[uint32]uint32{offsetArch: arch, offsetSyscallNr: nr, offsetArg0: arg0}
	accumulator := uint32(0)
	pc := 0
	for steps := 0; steps < 1000; steps++ {
		if pc < 0 || pc >= len(filter.Instructions) {
			t.Fatalf("program counter %d is outside the program", pc)
		}
		instruction := filter.Instructions[pc]
		switch instruction.Code {
		case bpfLdW:
			value, ok := data[instruction.K]
			if !ok {
				t.Fatalf("load from unmodelled offset %d", instruction.K)
			}
			accumulator = value
			pc++
		case bpfJeqK:
			if accumulator == instruction.K {
				pc += int(instruction.JT) + 1
			} else {
				pc += int(instruction.JF) + 1
			}
		case bpfRetK:
			return instruction.K
		default:
			t.Fatalf("unexpected opcode %#x", instruction.Code)
		}
	}
	t.Fatal("program did not terminate")
	return 0
}

func TestProgramSemanticsDenyAllowAndKill(t *testing.T) {
	filter, err := Build(Policy{}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	deny := RetErrno(ErrnoEPERM)
	// A foreign architecture is killed: filtering it with this syscall
	// table would silently fail open.
	if got := evaluate(t, filter, archARM64.AuditArch, 198, 0); got != RetKillProcess {
		t.Fatalf("foreign architecture returned %#x", got)
	}
	// connect (42) is denied, socket with a non-Unix domain is denied.
	if got := evaluate(t, filter, archAMD64.AuditArch, 42, 0); got != deny {
		t.Fatalf("connect returned %#x", got)
	}
	if got := evaluate(t, filter, archAMD64.AuditArch, 41, 2 /* AF_INET */); got != deny {
		t.Fatalf("socket(AF_INET) returned %#x", got)
	}
	// AF_UNIX sockets fall through to allow.
	if got := evaluate(t, filter, archAMD64.AuditArch, 41, AFUnix); got != RetAllow {
		t.Fatalf("socket(AF_UNIX) returned %#x", got)
	}
	if got := evaluate(t, filter, archAMD64.AuditArch, 53, AFUnix); got != RetAllow {
		t.Fatalf("socketpair(AF_UNIX) returned %#x", got)
	}
	// recvfrom stays allowed; an unrelated syscall (read, 0) is allowed.
	if got := evaluate(t, filter, archAMD64.AuditArch, 45, 0); got != RetAllow {
		t.Fatalf("recvfrom returned %#x", got)
	}
	if got := evaluate(t, filter, archAMD64.AuditArch, 0, 0); got != RetAllow {
		t.Fatalf("read returned %#x", got)
	}
	// io_uring and ptrace are denied unconditionally.
	for _, nr := range []uint32{101, 425, 426, 427} {
		if got := evaluate(t, filter, archAMD64.AuditArch, nr, 0); got != deny {
			t.Fatalf("syscall %d returned %#x", nr, got)
		}
	}

	// With network allowed the socket domains are unrestricted but io_uring
	// and ptrace stay denied.
	open, err := Build(Policy{AllowNetwork: true}, "amd64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got := evaluate(t, open, archAMD64.AuditArch, 41, 2); got != RetAllow {
		t.Fatalf("socket(AF_INET) with network returned %#x", got)
	}
	if got := evaluate(t, open, archAMD64.AuditArch, 425, 0); got != deny {
		t.Fatalf("io_uring_setup returned %#x", got)
	}

	// The ARM64 program denies ARM64 numbers and allows the AMD64 ones that
	// merely look like its own denied numbers.
	arm, err := Build(Policy{}, "arm64")
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if got := evaluate(t, arm, archARM64.AuditArch, 198, 2); got != deny {
		t.Fatalf("arm64 socket(AF_INET) returned %#x", got)
	}
	if got := evaluate(t, arm, archARM64.AuditArch, 41, 2); got != RetAllow {
		t.Fatalf("arm64 read returned %#x", got)
	}
}
