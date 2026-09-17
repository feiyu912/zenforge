//go:build linux

package landlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ruleTypePathBeneath is LANDLOCK_RULE_PATH_BENEATH.
const ruleTypePathBeneath = 1

// prSetNoNewPrivs is PR_SET_NO_NEW_PRIVS. Landlock refuses to restrict a
// process that could still gain privileges through setuid or file
// capabilities, because that would let the sandbox be escaped.
const prSetNoNewPrivs = 38

// rulesetAttr mirrors struct landlock_ruleset_attr.
type rulesetAttr struct {
	HandledAccessFS  uint64
	HandledAccessNet uint64
	Scoped           uint64
}

// pathBeneathAttr mirrors struct landlock_path_beneath_attr. The kernel
// declares it packed, which matters for the trailing 4-byte hole: without
// packing the struct is 16 bytes, and the kernel reads 12.
type pathBeneathAttr struct {
	AllowedAccess uint64
	ParentFd      int32
	_             [4]byte
}

// ErrUnsupported reports a kernel without Landlock support.
var ErrUnsupported = errors.New("landlock is not supported by this kernel")

// ABIVersion returns the Landlock ABI the running kernel implements.
func ABIVersion() (int, error) {
	// A NULL attribute with the VERSION flag asks for the ABI version.
	version, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return 0, fmt.Errorf("%w: %v", ErrUnsupported, errno)
	}
	return int(version), nil
}

// Available reports whether Landlock can be used on this host.
func Available() error {
	if _, err := ABIVersion(); err != nil {
		return err
	}
	return nil
}

// Restrict installs the ruleset on the calling thread. Every process the
// caller spawns afterwards inherits the restriction, so a caller that
// intends to run a command must install the ruleset and then exec.
func Restrict(ruleset Ruleset) error {
	if len(ruleset.Rules) == 0 {
		return fmt.Errorf("landlock: ruleset has no rules")
	}
	attr := rulesetAttr{HandledAccessFS: uint64(ruleset.HandledAccess)}
	// The network and scope fields were added by later ABIs; a kernel that
	// does not know them rejects a non-zero value with E2BIG, so leave them
	// zero.
	fd, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)),
		unsafe.Sizeof(attr),
		0,
	)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	rulesetFd := int(fd)
	defer func() { _ = unix.Close(rulesetFd) }()

	for _, rule := range ruleset.Rules {
		if err := addPathBeneathRule(rulesetFd, rule); err != nil {
			return err
		}
	}
	// Restriction requires no-new-privileges; without it the kernel rejects
	// the restriction with EPERM.
	if err := unix.Prctl(prSetNoNewPrivs, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFd), 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

// addPathBeneathRule opens one path and adds it to the ruleset. A path that
// does not exist is an error: silently dropping a writable root would
// produce a sandbox narrower than the caller asked for, and silently
// dropping a readable root would break the command in a confusing way.
func addPathBeneathRule(rulesetFd int, rule Rule) error {
	how := unix.O_PATH | unix.O_CLOEXEC
	if info, err := os.Stat(rule.Path); err == nil && info.IsDir() {
		how = unix.O_PATH | unix.O_CLOEXEC | unix.O_DIRECTORY
	}
	pathFd, err := unix.Open(rule.Path, how, 0)
	if err != nil {
		return fmt.Errorf("open %s for landlock: %w", rule.Path, err)
	}
	defer func() { _ = unix.Close(pathFd) }()

	attr := pathBeneathAttr{AllowedAccess: uint64(rule.Access), ParentFd: int32(pathFd)}
	if _, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd),
		uintptr(ruleTypePathBeneath),
		uintptr(unsafe.Pointer(&attr)),
		0, 0, 0,
	); errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", rule.Path, errno)
	}
	return nil
}

// Exec installs the ruleset and replaces the current process with the
// command. It never returns on success. This is the only correct shape for
// a Landlock sandbox: the restriction must be installed by the process that
// execs, because it cannot be imposed from outside.
func Exec(ruleset Ruleset, path string, argv []string, env []string) error {
	if path == "" {
		return fmt.Errorf("landlock: executable path is required")
	}
	if err := Restrict(ruleset); err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}
