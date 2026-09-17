// Package landlock plans and (on Linux) applies Landlock filesystem
// restrictions. Landlock is an unprivileged, in-process Linux security
// module: a process installs a ruleset on itself, and the restriction is
// inherited by every process it spawns. It therefore cannot be wrapped
// around a command from the outside the way bubblewrap can — the
// restriction must be installed by the process that is about to exec, or
// by a helper that execs the command.
//
// The planner is platform independent and owns the security-relevant
// arithmetic: which access rights exist at a given Landlock ABI, what each
// policy grants, and what the backend must refuse rather than silently
// ignore. The Linux file applies the planned ruleset with raw syscalls.
package landlock

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Access is a bitmask of Landlock filesystem access rights. The bit values
// are the kernel's (landlock.h); rights added by later ABIs are simply
// absent from an older ABI's mask.
type Access uint64

// Filesystem access rights, by the ABI that introduced them.
const (
	AccessExecute    Access = 1 << 0  // ABI 1
	AccessWriteFile  Access = 1 << 1  // ABI 1
	AccessReadFile   Access = 1 << 2  // ABI 1
	AccessReadDir    Access = 1 << 3  // ABI 1
	AccessRemoveDir  Access = 1 << 4  // ABI 1
	AccessRemoveFile Access = 1 << 5  // ABI 1
	AccessMakeChar   Access = 1 << 6  // ABI 1
	AccessMakeDir    Access = 1 << 7  // ABI 1
	AccessMakeReg    Access = 1 << 8  // ABI 1
	AccessMakeSock   Access = 1 << 9  // ABI 1
	AccessMakeFifo   Access = 1 << 10 // ABI 1
	AccessMakeBlock  Access = 1 << 11 // ABI 1
	AccessMakeSym    Access = 1 << 12 // ABI 1
	AccessRefer      Access = 1 << 13 // ABI 2
	AccessTruncate   Access = 1 << 14 // ABI 3
	AccessIoctlDev   Access = 1 << 15 // ABI 5
)

// accessByABI lists every filesystem right with the ABI that introduced it.
var accessByABI = []struct {
	abi    int
	access Access
}{
	{1, AccessExecute},
	{1, AccessWriteFile},
	{1, AccessReadFile},
	{1, AccessReadDir},
	{1, AccessRemoveDir},
	{1, AccessRemoveFile},
	{1, AccessMakeChar},
	{1, AccessMakeDir},
	{1, AccessMakeReg},
	{1, AccessMakeSock},
	{1, AccessMakeFifo},
	{1, AccessMakeBlock},
	{1, AccessMakeSym},
	{2, AccessRefer},
	{3, AccessTruncate},
	{5, AccessIoctlDev},
}

// MaxSupportedABI is the newest ABI this package knows about.
const MaxSupportedABI = 5

// ReadAccess is the set of rights needed to read and execute files. Codex
// grants exactly this on the whole filesystem, so a sandboxed command can
// still run binaries and read its inputs.
const ReadAccess = AccessExecute | AccessReadFile | AccessReadDir

// WriteAccess is the set of rights a writable root is granted. The write
// rights are permissive on purpose: a Landlock ruleset has no deny rule, so
// the only way to express "read-only here" is to not grant write rights at
// all.
const WriteAccess = AccessWriteFile | AccessRemoveDir | AccessRemoveFile |
	AccessMakeChar | AccessMakeDir | AccessMakeReg | AccessMakeSock |
	AccessMakeFifo | AccessMakeBlock | AccessMakeSym

// FileAccessAt returns the rights that may be granted on a non-directory
// path. The kernel rejects a path-beneath rule on a file that also carries
// directory rights (landlock_add_rule returns EINVAL), so a rule for a
// device node or a single file must be masked down to these bits; passing
// the full directory set would fail the whole ruleset and leave the command
// unsandboxed or unable to start.
func FileAccessAt(abi int) Access {
	access := AccessExecute | AccessReadFile | AccessWriteFile
	if abi >= 3 {
		access |= AccessTruncate
	}
	if abi >= 5 {
		access |= AccessIoctlDev
	}
	return access
}

// AllAccessAt returns every right the kernel supports at abi. An ABI above
// MaxSupportedABI is treated as MaxSupportedABI, which is the safe
// direction: requesting a right the running kernel does not know makes the
// ruleset creation fail, and a fail-closed error is worse than a slightly
// narrower sandbox.
func AllAccessAt(abi int) Access {
	if abi > MaxSupportedABI {
		abi = MaxSupportedABI
	}
	var access Access
	for _, entry := range accessByABI {
		if entry.abi <= abi {
			access |= entry.access
		}
	}
	return access
}

// ReadAccessAt is the read and execute right set at abi. Every read right
// exists from ABI 1, so this is ReadAccess; the function exists so callers
// never hand-write the mask.
func ReadAccessAt(abi int) Access {
	return AllAccessAt(abi) & ReadAccess
}

// Rule grants a set of access rights beneath one path.
type Rule struct {
	Path   string
	Access Access
}

// Ruleset is a planned Landlock ruleset.
type Ruleset struct {
	// ABI is the Landlock ABI the plan was built for.
	ABI int
	// HandledAccess is the set of rights the ruleset handles. A right that
	// is not handled is not restricted at all, which is why the planner
	// handles every right the ABI supports rather than only the granted
	// ones.
	HandledAccess Access
	// Rules are the per-path grants.
	Rules []Rule
	// ReadOnlyRoots records the roots that received read access only, for
	// reporting and tests.
	ReadOnlyRoots []string
	// WritableRoots records the roots that received write access.
	WritableRoots []string
}

// Policy describes the filesystem boundary to plan.
type Policy struct {
	// WritableRoots are the only paths a sandboxed command may modify.
	WritableRoots []string
	// ReadOnlyPaths requests read-only access to extra paths. Landlock
	// already grants read access to the whole filesystem, so these are
	// informational unless FullDiskRead is false.
	ReadOnlyPaths []string
	// FullDiskRead grants read access to the whole filesystem, matching the
	// reference. When it is false, read access is granted only to the
	// readable roots, and the command cannot see anything else at all.
	FullDiskRead bool
	// ReadableRoots are the read roots for a non-full-disk policy.
	ReadableRoots []string
	// ProtectedNames requests per-path read-only carve-outs inside writable
	// roots. Landlock cannot express them (see Build), so requesting one is
	// an error rather than a silent no-op.
	ProtectedNames []string
	// ReadWritePaths are individual files granted read-write access, used
	// for device nodes such as /dev/null.
	ReadWritePaths []string
}

// DefaultDevicePath is granted read-write by every landlock plan. The
// reference does the same: a sandbox that denies writing to /dev/null
// breaks every command that discards output (`cmd >/dev/null`), which is
// most of them.
const DefaultDevicePath = "/dev/null"

// ErrUnsupportedCarveOut reports a policy Landlock cannot express.
type ErrUnsupportedCarveOut struct {
	Feature string
	Detail  string
}

func (e *ErrUnsupportedCarveOut) Error() string {
	return fmt.Sprintf("landlock cannot express %s: %s", e.Feature, e.Detail)
}

// Build plans a ruleset for the policy at the given ABI.
//
// Landlock rights are additive: for a given path the kernel takes the union
// of every matching rule, and there is no deny rule. A read-only carve-out
// inside a writable root is therefore impossible — the root's own rule
// already grants the write rights — so a policy that asks for one is
// rejected instead of silently granting more than it promised. Bubblewrap
// (or Seatbelt) is the backend for those policies.
func Build(policy Policy, abi int) (Ruleset, error) {
	if abi < 1 {
		return Ruleset{}, fmt.Errorf("landlock requires ABI 1 or newer (got %d)", abi)
	}
	if len(policy.ProtectedNames) > 0 {
		return Ruleset{}, &ErrUnsupportedCarveOut{
			Feature: "protected names inside writable roots",
			Detail:  "Landlock has no deny rule and unions every matching rule, so a writable root cannot contain a read-only path; use the bubblewrap or Seatbelt backend",
		}
	}
	writable, err := normalizePaths(policy.WritableRoots, "writable root")
	if err != nil {
		return Ruleset{}, err
	}
	readable, err := normalizePaths(policy.ReadableRoots, "readable root")
	if err != nil {
		return Ruleset{}, err
	}
	readWrite, err := normalizePaths(policy.ReadWritePaths, "read-write path")
	if err != nil {
		return Ruleset{}, err
	}
	known := abi
	if known > MaxSupportedABI {
		known = MaxSupportedABI
	}
	full := AllAccessAt(known)
	read := ReadAccessAt(known)

	ruleset := Ruleset{ABI: abi, HandledAccess: full}
	if policy.FullDiskRead {
		ruleset.Rules = append(ruleset.Rules, Rule{Path: "/", Access: read})
		ruleset.ReadOnlyRoots = append(ruleset.ReadOnlyRoots, "/")
	} else {
		// Without whole-filesystem read access, grant read on the declared
		// roots only. Nothing else is visible: an unhandled path is denied
		// outright, not merely unwritable.
		for _, root := range readable {
			ruleset.Rules = append(ruleset.Rules, Rule{Path: root, Access: read})
			ruleset.ReadOnlyRoots = append(ruleset.ReadOnlyRoots, root)
		}
	}
	// Individual files that need write access (a device node such as
	// /dev/null) are granted the write set that applies to files. The safe
	// device is granted even when the caller did not ask for it, because a
	// sandbox that cannot write to /dev/null breaks ordinary shell use.
	fileAccess := FileAccessAt(known)
	devices := readWrite
	if !containsPath(devices, DefaultDevicePath) {
		if _, err := os.Stat(DefaultDevicePath); err == nil {
			devices = append(devices, DefaultDevicePath)
			sort.Strings(devices)
		}
	}
	for _, path := range devices {
		ruleset.Rules = append(ruleset.Rules, Rule{Path: path, Access: accessForPath(path, full, fileAccess)})
	}
	for _, root := range writable {
		ruleset.Rules = append(ruleset.Rules, Rule{Path: root, Access: accessForPath(root, full, fileAccess)})
		ruleset.WritableRoots = append(ruleset.WritableRoots, root)
	}
	return ruleset, nil
}

// accessForPath picks the mask the kernel accepts for a path: a
// non-directory may only carry the file rights, while a directory takes the
// full set.
func accessForPath(path string, directory, file Access) Access {
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return file
	}
	return directory
}

// containsPath reports whether a path list already names path.
func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}

// normalizePaths validates, canonicalizes, deduplicates, and sorts a path
// list. Sorting keeps the ruleset deterministic, which matters because it
// is hashed and compared between runs.
func normalizePaths(paths []string, label string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("landlock %s %q must be absolute", label, raw)
		}
		cleaned := filepath.Clean(path)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out, nil
}

// Fingerprint is a stable description of the ruleset, used to tie a run to
// the policy that governed it.
func (r Ruleset) Fingerprint() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "abi=%d handled=%d", r.ABI, r.HandledAccess)
	for _, rule := range r.Rules {
		fmt.Fprintf(&builder, "|%s=%d", rule.Path, rule.Access)
	}
	return builder.String()
}
