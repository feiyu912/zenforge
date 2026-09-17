// Package seccomp plans and (on Linux) applies a seccomp BPF filter. The
// filter is the network half of a Linux sandbox: Landlock and bubblewrap
// restrict the filesystem, but neither can stop a process from opening a
// socket, so a network boundary needs seccomp.
//
// Like Landlock, a seccomp filter must be installed by the process that
// execs the sandboxed command, so this package follows the same split: the
// filter is planned as data on every platform, and applied with syscalls
// on Linux. The default action is allow; matched rules return EPERM, which
// a toolchain reports as a permission error instead of a crash.
package seccomp

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Instruction is one classic BPF instruction (struct sock_filter).
type Instruction struct {
	Code uint16
	JT   uint8
	JF   uint8
	K    uint32
}

// BPF opcodes used by the generated program.
const (
	bpfLdW  = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeqK = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfRetK = 0x06 // BPF_RET | BPF_K
)

// Seccomp return values.
const (
	// RetAllow lets the syscall run.
	RetAllow = 0x7fff0000
	// RetKillProcess kills the process. It is the answer to a foreign
	// architecture: without the check, a filter built for one ABI could be
	// bypassed by a process running another, because syscall numbers differ.
	RetKillProcess = 0x80000000
	// ErrnoEPERM is the errno a denied syscall returns. EPERM is 1 on
	// Linux: it is spelled out here and pinned by a test because 22 (which
	// this constant used to hold) is EINVAL, and a denied syscall that
	// fails with the wrong errno is a bug that only shows up on a Linux
	// host as "invalid argument" instead of "operation not permitted".
	ErrnoEPERM = 1
)

// RetErrno returns the seccomp return value that fails a syscall with errno.
func RetErrno(errno uint32) uint32 {
	return 0x00050000 | (errno & 0xffff)
}

// Offsets inside struct seccomp_data.
const (
	offsetSyscallNr = 0
	offsetArch      = 4
	offsetArg0      = 16
)

// AFUnix is the socket domain the reference keeps available: Unix sockets
// are used for subprocess management (language servers, `cargo clippy`) over
// socketpair(2), and
// do not reach the network.
const AFUnix = 1

// Arch is a Linux architecture with its audit value and syscall numbers.
type Arch struct {
	// Name is the GOARCH-style name.
	Name string
	// AuditArch is the AUDIT_ARCH_* value the kernel reports in
	// seccomp_data.arch.
	AuditArch uint32
	// Syscalls maps a syscall name to its number. A name that is absent on
	// this architecture is skipped when a policy names it.
	Syscalls map[string]int
}

// Supported architectures. Only the two 64-bit Go targets are supported:
// shipping a hand-copied syscall table for an architecture this project
// cannot even build for would be a guess, and a wrong syscall number in a
// security filter is a silent hole. The numbers differ per architecture,
// which is exactly why the generated program checks the architecture
// first.
var (
	archAMD64 = Arch{
		Name:      "amd64",
		AuditArch: 0xc000003e,
		Syscalls: map[string]int{
			"socket": 41, "connect": 42, "accept": 43, "sendto": 44,
			"recvfrom": 45, "sendmsg": 46, "recvmsg": 47, "shutdown": 48,
			"bind": 49, "listen": 50, "getsockname": 51, "getpeername": 52,
			"socketpair": 53, "setsockopt": 54, "getsockopt": 55,
			"accept4": 288, "recvmmsg": 299, "sendmmsg": 307,
			"ptrace": 101, "process_vm_readv": 310, "process_vm_writev": 311,
			"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		},
	}
	archARM64 = Arch{
		Name:      "arm64",
		AuditArch: 0xc00000b7,
		Syscalls: map[string]int{
			"socket": 198, "connect": 203, "accept": 202, "sendto": 206,
			"recvfrom": 207, "sendmsg": 211, "recvmsg": 212, "shutdown": 210,
			"bind": 200, "listen": 201, "getsockname": 204, "getpeername": 205,
			"socketpair": 199, "setsockopt": 208, "getsockopt": 209,
			"accept4": 242, "recvmmsg": 243, "sendmmsg": 269,
			"ptrace": 117, "process_vm_readv": 270, "process_vm_writev": 271,
			"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		},
	}
)

// archByName resolves a GOARCH name.
func archByName(name string) (Arch, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "amd64", "x86_64":
		return archAMD64, nil
	case "arm64", "aarch64":
		return archARM64, nil
	default:
		return Arch{}, fmt.Errorf("seccomp: unsupported architecture %q", name)
	}
}

// networkSyscalls are denied when the policy grants no network access.
// recvfrom is deliberately absent: allowing it lets a toolchain that
// manages subprocesses over a Unix socketpair keep working, and it cannot
// reach the network on its own.
//
// bind, listen, and connect stay denied even for AF_UNIX: nothing should be
// able to turn an inherited descriptor into a listening or dialing endpoint,
// and the socketpair path subprocess tooling needs does not require them.
var networkSyscalls = []string{
	"connect", "accept", "accept4", "bind", "listen",
	"getpeername", "getsockname", "shutdown", "sendto", "sendmmsg",
	"recvmmsg", "getsockopt", "setsockopt",
}

// processInspectionSyscalls are denied unconditionally: a confined process
// has no business reading another process's memory or tracing it.
var processInspectionSyscalls = []string{"ptrace", "process_vm_readv", "process_vm_writev"}

// ioUringSyscalls are denied unconditionally: io_uring can create sockets
// without going through socket(2), so it would bypass the network rules.
var ioUringSyscalls = []string{"io_uring_setup", "io_uring_enter", "io_uring_register"}

// Policy describes the syscall boundary to plan.
type Policy struct {
	// AllowNetwork keeps the network syscalls available. The default
	// denies them.
	AllowNetwork bool
	// ExtraDeny names additional syscalls to deny.
	ExtraDeny []string
	// AllowUnixSockets keeps AF_UNIX sockets available while other domains
	// are denied. It defaults to true; set it to false to deny socket(2)
	// and socketpair(2) outright.
	AllowUnixSockets *bool
}

// Filter is a planned seccomp filter.
type Filter struct {
	// Arch is the architecture the filter was built for.
	Arch Arch
	// Instructions is the program. Its first check validates the
	// architecture.
	Instructions []Instruction
	// Denied lists the syscall names with an unconditional deny rule.
	Denied []string
	// DomainRestricted lists the syscalls allowed only for AF_UNIX.
	DomainRestricted []string
}

// Build plans a filter for the policy and the named architecture.
func Build(policy Policy, archName string) (Filter, error) {
	arch, err := archByName(archName)
	if err != nil {
		return Filter{}, err
	}
	allowUnix := true
	if policy.AllowUnixSockets != nil {
		allowUnix = *policy.AllowUnixSockets
	}

	denied := append([]string(nil), processInspectionSyscalls...)
	denied = append(denied, ioUringSyscalls...)
	if !policy.AllowNetwork {
		denied = append(denied, networkSyscalls...)
	}
	if !allowUnix {
		denied = append(denied, "socket", "socketpair")
	}
	denied = append(denied, policy.ExtraDeny...)
	sort.Strings(denied)
	denied = dedupeStrings(denied)

	var domainRestricted []string
	if !policy.AllowNetwork && allowUnix {
		domainRestricted = []string{"socket", "socketpair"}
	}

	// Resolve names to numbers. A name the architecture does not have is an
	// error for an explicit request and a skip for a built-in rule.
	numbers, err := resolveSyscalls(arch, denied, policy.ExtraDeny)
	if err != nil {
		return Filter{}, err
	}
	domainNumbers, err := resolveSyscalls(arch, domainRestricted, nil)
	if err != nil {
		return Filter{}, err
	}

	program := []Instruction{
		{Code: bpfLdW, K: offsetArch},
		// A foreign architecture must not be filtered with the wrong
		// syscall table; without this check the filter is bypassable.
		{Code: bpfJeqK, JT: 1, JF: 0, K: arch.AuditArch},
		{Code: bpfRetK, K: RetKillProcess},
		{Code: bpfLdW, K: offsetSyscallNr},
	}

	// Two passes: emit the checks with placeholder jumps, then point them at
	// the deny block once its index is known. Classic BPF jumps are 8-bit
	// offsets, so the program is validated to fit before it is returned.
	// A matching deny check jumps to the EPERM return; a non-matching one
	// falls through to the next check.
	var denyOnMatch []int
	for _, number := range numbers {
		denyOnMatch = append(denyOnMatch, len(program))
		program = append(program, Instruction{Code: bpfJeqK, K: uint32(number)})
	}
	// Domain-restricted syscalls: AF_UNIX (arg0 == 1) falls through and
	// anything else jumps to the EPERM return, so the jump is on the false
	// branch.
	var denyOnMismatch []int
	for _, number := range domainNumbers {
		skip := 2 // the arg0 load and its comparison
		program = append(program, Instruction{Code: bpfJeqK, JT: 0, JF: uint8(skip), K: uint32(number)})
		program = append(program, Instruction{Code: bpfLdW, K: offsetArg0})
		denyOnMismatch = append(denyOnMismatch, len(program))
		program = append(program, Instruction{Code: bpfJeqK, JT: 0, JF: 0, K: AFUnix})
	}

	program = append(program, Instruction{Code: bpfRetK, K: RetAllow})
	denyIndex := len(program)
	program = append(program, Instruction{Code: bpfRetK, K: RetErrno(ErrnoEPERM)})

	for _, index := range append(append([]int(nil), denyOnMatch...), denyOnMismatch...) {
		distance := denyIndex - (index + 1)
		if distance < 0 || distance > 255 {
			return Filter{}, fmt.Errorf("seccomp: filter too large for a classic BPF jump (%d instructions)", len(program))
		}
	}
	for _, index := range denyOnMatch {
		program[index].JT = uint8(denyIndex - (index + 1))
		program[index].JF = 0
	}
	for _, index := range denyOnMismatch {
		program[index].JT = 0
		program[index].JF = uint8(denyIndex - (index + 1))
	}

	return Filter{
		Arch:             arch,
		Instructions:     program,
		Denied:           denied,
		DomainRestricted: domainRestricted,
	}, nil
}

// resolveSyscalls maps names to numbers, reporting a name the architecture
// does not define when it was explicitly requested. A bare decimal number
// is accepted as a syscall number so a policy can deny a syscall this
// package has no name for; named entries are limited to the syscalls the
// filter cares about, and a wrong number would be a silent hole.
func resolveSyscalls(arch Arch, names, required []string) ([]int, error) {
	requiredSet := map[string]bool{}
	for _, name := range required {
		requiredSet[name] = true
	}
	var numbers []int
	seen := map[int]bool{}
	for _, name := range names {
		if numeric, err := strconv.Atoi(name); err == nil {
			if numeric < 0 || numeric > 1<<20 {
				return nil, fmt.Errorf("seccomp: syscall number %d is out of range", numeric)
			}
			if !seen[numeric] {
				seen[numeric] = true
				numbers = append(numbers, numeric)
			}
			continue
		}
		number, ok := arch.Syscalls[name]
		if !ok {
			if requiredSet[name] {
				return nil, fmt.Errorf("seccomp: syscall %q does not exist on %s", name, arch.Name)
			}
			continue
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	return numbers, nil
}

// dedupeStrings sorts and deduplicates a name list.
func dedupeStrings(values []string) []string {
	out := values[:0]
	for index, value := range values {
		if index > 0 && values[index-1] == value {
			continue
		}
		out = append(out, value)
	}
	return out
}

// Fingerprint is a stable description of the filter, used to tie a run to
// the policy that governed it.
func (f Filter) Fingerprint() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "arch=%s instructions=%d", f.Arch.Name, len(f.Instructions))
	for _, name := range f.Denied {
		builder.WriteString("|deny:" + name)
	}
	for _, name := range f.DomainRestricted {
		builder.WriteString("|unix-only:" + name)
	}
	return builder.String()
}
