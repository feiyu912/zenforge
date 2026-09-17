// Package seatbelt provides a macOS Seatbelt (sandbox-exec) sandbox
// backend. The policy is generated as an SBPL document: a closed-by-default
// profile that allows process execution, the system read paths a toolchain
// needs, write access only to the declared roots, and no network unless the
// caller opts in. Inside every writable root, protected metadata paths
// (notably `.git` and ZenForge's own config directory) are pinned read-only
// so a sandboxed command cannot rewrite the repository's history or the
// configuration that the next policy will be built from.
package seatbelt

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Default protected names inside a writable root. A sandboxed command may
// read them but must not write them.
var defaultProtectedNames = []string{".git", ".zenforge"}

// Policy describes one SBPL profile.
type Policy struct {
	// WritableRoots are the only paths the sandbox may modify. They must be
	// absolute.
	WritableRoots []string
	// ReadableRoots are extra read paths beyond the platform defaults.
	ReadableRoots []string
	// ReadOnlyPaths are extra paths that must stay read-only even when they
	// fall inside a writable root.
	ReadOnlyPaths []string
	// ProtectedNames pins these basenames read-only wherever they appear
	// inside a writable root. Empty selects the defaults.
	ProtectedNames []string
	// AllowNetwork grants outbound network access. The default denies it.
	AllowNetwork bool
	// AllowLocalBinding grants network-bind, which local test servers need.
	AllowLocalBinding bool
	// ExtraRules is appended verbatim, for host-specific allowances.
	ExtraRules string
}

// BuiltPolicy is a prepared profile with its parameter bindings.
type BuiltPolicy struct {
	// Profile is the SBPL source.
	Profile string
	// Params bind the `(param ...)` references used by Profile.
	Params map[string]string
}

// Argument returns the sandbox-exec arguments that select the profile and
// bind its parameters. Parameters are passed on the command line (rather
// than interpolated into the policy text), so a path containing SBPL syntax
// cannot change the policy.
func (b BuiltPolicy) Argument() []string {
	keys := make([]string, 0, len(b.Params))
	for key := range b.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]string, 0, 2+2*len(keys))
	for _, key := range keys {
		args = append(args, "-D", key+"="+b.Params[key])
	}
	return append(args, "-p", b.Profile)
}

// BuildPolicy validates the policy and renders it as SBPL. Every declared
// path becomes a named parameter, so the profile text itself carries no
// host-specific paths.
func BuildPolicy(policy Policy) (BuiltPolicy, error) {
	writable, err := normalizeRoots(policy.WritableRoots, "writable root")
	if err != nil {
		return BuiltPolicy{}, err
	}
	readable, err := normalizeRoots(policy.ReadableRoots, "readable root")
	if err != nil {
		return BuiltPolicy{}, err
	}
	readOnly, err := normalizeRoots(policy.ReadOnlyPaths, "read-only path")
	if err != nil {
		return BuiltPolicy{}, err
	}
	protected := policy.ProtectedNames
	if len(protected) == 0 {
		protected = defaultProtectedNames
	}
	for _, name := range protected {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, `/\`) {
			return BuiltPolicy{}, fmt.Errorf("seatbelt protected name %q must be a plain basename", name)
		}
	}

	params := map[string]string{}
	var builder strings.Builder
	builder.WriteString(basePolicy)
	builder.WriteString(processDefaults)

	// Readable roots, anchored by params.
	if len(readable) > 0 {
		builder.WriteString("\n; caller-declared read-only roots\n")
		builder.WriteString("(allow file-read* file-test-existence\n")
		for index, root := range readable {
			key := fmt.Sprintf("READABLE_ROOT_%d", index)
			params[key] = root
			fmt.Fprintf(&builder, "  (subpath (param %q))\n", key)
		}
		builder.WriteString(")\n")
	}

	// Writable roots plus the protections that keep an authority boundary
	// (the root itself, and protected metadata inside it) intact.
	if len(writable) > 0 {
		// A writable root is readable too (a workspace must be), and its
		// ancestor directories stay resolvable so tools can determine
		// their own working directory.
		builder.WriteString("\n; writable roots are readable, including their ancestor chain\n")
		builder.WriteString("(allow file-read* file-test-existence\n")
		for index := range writable {
			key := fmt.Sprintf("WRITABLE_ROOT_%d", index)
			fmt.Fprintf(&builder, "  (subpath (param %q))\n", key)
			fmt.Fprintf(&builder, "  (path-ancestors (param %q))\n", key)
		}
		builder.WriteString(")\n")

		builder.WriteString("\n; writable roots, each excluding its protected metadata\n")
		builder.WriteString("(allow file-write*\n")
		for index, root := range writable {
			key := fmt.Sprintf("WRITABLE_ROOT_%d", index)
			params[key] = root
			if len(protected) == 0 {
				fmt.Fprintf(&builder, "  (subpath (param %q))\n", key)
				continue
			}
			// The exclusion belongs on the allow rule: the profile is
			// closed by default, so a path that is not allowed is denied.
			// (Encoding the same set as a deny with require-not would deny
			// the *complement* -- everything under the root except the
			// protected paths, which is the opposite of the intent.)
			fmt.Fprintf(&builder, "  (require-all\n    (subpath (param %q))\n", key)
			for nameIndex, name := range protected {
				protectedKey := fmt.Sprintf("PROTECTED_%d_%d", index, nameIndex)
				params[protectedKey] = filepath.Join(root, name)
				// Exclude the protected path itself and everything beneath
				// it; `subpath` alone would still allow first-time
				// creation of the protected directory.
				fmt.Fprintf(&builder, "    (require-not (literal (param %q)))\n    (require-not (subpath (param %q)))\n", protectedKey, protectedKey)
			}
			builder.WriteString("  )\n")
		}
		builder.WriteString(")\n")

		// A sandboxed process must not be able to unlink or replace a root
		// directory itself, because that root is reused to build the next
		// policy. The path stays in the parameter binding, so no host path
		// is ever interpolated into the profile text.
		for index := range writable {
			key := fmt.Sprintf("WRITABLE_ROOT_%d", index)
			fmt.Fprintf(&builder, "(deny file-write-unlink (require-all (literal (param %q)) (vnode-type DIRECTORY)))\n", key)
		}
	}

	// Explicit read-only paths, denied for writes with precedence over the
	// writable-root allowance.
	if len(readOnly) > 0 {
		builder.WriteString("\n; explicit read-only paths\n")
		for index, path := range readOnly {
			key := fmt.Sprintf("READONLY_PATH_%d", index)
			params[key] = path
			fmt.Fprintf(&builder, "(deny file-write* (subpath (param %q)))\n", key)
		}
	}

	if policy.AllowNetwork {
		builder.WriteString("\n; network access granted by the caller\n(allow network-outbound)\n(allow network-inbound)\n")
		if policy.AllowLocalBinding {
			builder.WriteString("(allow network-bind (local ip \"*:*\"))\n(allow network-inbound (local ip \"localhost:*\"))\n(allow network-outbound (remote ip \"localhost:*\"))\n")
		}
		builder.WriteString(networkPolicy)
	}
	if rules := strings.TrimSpace(policy.ExtraRules); rules != "" {
		builder.WriteString("\n; caller rules\n")
		builder.WriteString(rules)
		builder.WriteString("\n")
	}
	return BuiltPolicy{Profile: builder.String(), Params: params}, nil
}

// normalizeRoots validates and cleans a path list.
func normalizeRoots(paths []string, label string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("seatbelt %s %q must be absolute", label, raw)
		}
		cleaned := canonicalPath(path)
		if cleaned == "/" {
			return nil, fmt.Errorf("seatbelt %s must not be the filesystem root", label)
		}
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return out, nil
}

// canonicalPath resolves symlinks so the profile matches the vnodes the
// kernel actually checks (the reference canonicalizes for the same reason:
// a policy written for /tmp does not cover /private/tmp). A path that does
// not exist yet keeps its cleaned absolute form.
func canonicalPath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		if absolute, err := filepath.Abs(resolved); err == nil {
			return filepath.Clean(absolute)
		}
		return filepath.Clean(resolved)
	}
	// The path does not exist yet (a read-only path, or a file the run will
	// create). Resolve the longest existing ancestor and rejoin the rest, so
	// the result still lines up with the canonical form of the roots.
	remainder := []string{}
	current := cleaned
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			continue
		}
		parts := append([]string{resolved}, remainder...)
		return filepath.Join(parts...)
	}
}

// basePolicy is the closed-by-default profile: process rules, the sysctl,
// IOKit, and Mach lookups a toolchain performs at startup, the shared
// platform read paths, and the standard temporary directories. It is a
// faithful port of the reference's `seatbelt_base_policy.sbpl` and
// `seatbelt_read_only_platform_defaults.sbpl`, which is why the text is
// kept verbatim rather than paraphrased: Seatbelt profiles are compiled by
// the kernel's policy compiler, and a reworded rule is a different rule.
const basePolicy = `(version 1)

; inspired by Chrome's sandbox policy:
; https://source.chromium.org/chromium/chromium/src/+/main:sandbox/policy/mac/common.sb;l=273-319;drc=7b3962fe2e5fc9e2ee58000dc8fbf3429d84d3bd
; https://source.chromium.org/chromium/chromium/src/+/main:sandbox/policy/mac/renderer.sb;l=64;drc=7b3962fe2e5fc9e2ee58000dc8fbf3429d84d3bd

; start with closed-by-default
(deny default)

; child processes inherit the policy of their parent
(allow process-exec)
(allow process-fork)
(allow signal (target same-sandbox))

; process-info
(allow process-info* (target same-sandbox))

(allow file-write-data
  (require-all
    (path "/dev/null")
    (vnode-type CHARACTER-DEVICE)))

; sysctls permitted.
(allow sysctl-read
  (sysctl-name "hw.activecpu")
  (sysctl-name "hw.busfrequency_compat")
  (sysctl-name "hw.byteorder")
  (sysctl-name "hw.cacheconfig")
  (sysctl-name "hw.cachelinesize_compat")
  (sysctl-name "hw.cpufamily")
  (sysctl-name "hw.cpufrequency_compat")
  (sysctl-name "hw.cputype")
  (sysctl-name "hw.l1dcachesize_compat")
  (sysctl-name "hw.l1icachesize_compat")
  (sysctl-name "hw.l2cachesize_compat")
  (sysctl-name "hw.l3cachesize_compat")
  (sysctl-name "hw.logicalcpu_max")
  (sysctl-name "hw.machine")
  (sysctl-name "hw.model")
  (sysctl-name "hw.memsize")
  (sysctl-name "hw.ncpu")
  (sysctl-name "hw.nperflevels")
  ; Chrome locks these CPU feature detection down a bit more tightly,
  ; but mostly for fingerprinting concerns which isn't an issue for codex.
  (sysctl-name-prefix "hw.optional.arm.")
  (sysctl-name-prefix "hw.optional.armv8_")
  (sysctl-name "hw.packages")
  (sysctl-name "hw.pagesize_compat")
  (sysctl-name "hw.pagesize")
  (sysctl-name "hw.physicalcpu")
  (sysctl-name "hw.physicalcpu_max")
  (sysctl-name "hw.logicalcpu")
  (sysctl-name "hw.cpufrequency")
  (sysctl-name "hw.tbfrequency_compat")
  (sysctl-name "hw.vectorunit")
  (sysctl-name "machdep.cpu.brand_string")
  (sysctl-name "kern.argmax")
  (sysctl-name "kern.hostname")
  (sysctl-name "kern.maxfilesperproc")
  (sysctl-name "kern.maxproc")
  (sysctl-name "kern.osproductversion")
  (sysctl-name "kern.osrelease")
  (sysctl-name "kern.ostype")
  (sysctl-name "kern.osvariant_status")
  (sysctl-name "kern.osversion")
  (sysctl-name "kern.secure_kernel")
  ; Python's ProcessPoolExecutor queries this through sysconf(_SC_SEM_NSEMS_MAX).
  (sysctl-name "kern.sysv.semmns")
  (sysctl-name "kern.usrstack64")
  (sysctl-name "kern.version")
  (sysctl-name "sysctl.proc_cputype")
  (sysctl-name "vm.loadavg")
  (sysctl-name-prefix "hw.perflevel")
  (sysctl-name-prefix "kern.proc.pgrp.")
  (sysctl-name-prefix "kern.proc.pid.")
  (sysctl-name-prefix "net.routetable.")
)

; Allow Java to read some CPU info. This is misclassified as a "write" because
; userspace passes a memory buffer to the sysctl, but conceptually it is a read.
(allow sysctl-write
  (sysctl-name "kern.grade_cputype"))

; IOKit
(allow iokit-open
  (iokit-registry-entry-class "RootDomainUserClient")
)

; needed to look up user info, see https://crbug.com/792228
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
)

; Needed for python multiprocessing on MacOS for the SemLock
(allow ipc-posix-sem)

; Needed for PyTorch/libomp on macOS to register OpenMP runtimes.
(allow ipc-posix-shm-read-data
  ipc-posix-shm-write-create
  ipc-posix-shm-write-unlink
  (ipc-posix-name-regex #"^/__KMP_REGISTERED_LIB_[0-9]+$"))

(allow mach-lookup
  (global-name "com.apple.PowerManagement.control")
)

; allow openpty()
(allow pseudo-tty)
(allow file-read* file-write* file-ioctl (literal "/dev/ptmx"))
(allow file-read* file-write*
  (require-all
    (regex #"^/dev/ttys[0-9]+")
    (extension "com.apple.sandbox.pty")))
; PTYs created before entering seatbelt may lack the extension; allow ioctl
; on those slave ttys so interactive shells detect a TTY and remain functional.
(allow file-ioctl (regex #"^/dev/ttys[0-9]+"))
; macOS platform defaults included when a split filesystem policy requests :minimal.

; Read access to standard system paths
(allow file-read* file-test-existence
  (subpath "/Library/Apple")
  (subpath "/Library/Filesystems/NetFSPlugins")
  (subpath "/Library/Preferences/Logging")
  (subpath "/private/var/db/DarwinDirectory/local/recordStore.data")
  (subpath "/private/var/db/timezone")
  (subpath "/usr/lib")
  (subpath "/usr/share")
  (subpath "/Library/Preferences")
  (subpath "/var/db")
  (subpath "/private/var/db"))

; Map system frameworks + dylibs for loader.
(allow file-map-executable
  (subpath "/Library/Apple/System/Library/Frameworks")
  (subpath "/Library/Apple/System/Library/PrivateFrameworks")
  (subpath "/Library/Apple/usr/lib")
  (subpath "/System/Library/Extensions")
  (subpath "/System/Library/Frameworks")
  (subpath "/System/Library/PrivateFrameworks")
  (subpath "/System/Library/SubFrameworks")
  (subpath "/System/iOSSupport/System/Library/Frameworks")
  (subpath "/System/iOSSupport/System/Library/PrivateFrameworks")
  (subpath "/System/iOSSupport/System/Library/SubFrameworks")
  (subpath "/usr/lib"))

; System Framework and AppKit resources
(allow file-read* file-test-existence
  (subpath "/Library/Apple/System/Library/Frameworks")
  (subpath "/Library/Apple/System/Library/PrivateFrameworks")
  (subpath "/Library/Apple/usr/lib")
  (subpath "/System/Library/Frameworks")
  (subpath "/System/Library/PrivateFrameworks")
  (subpath "/System/Library/SubFrameworks")
  (subpath "/System/iOSSupport/System/Library/Frameworks")
  (subpath "/System/iOSSupport/System/Library/PrivateFrameworks")
  (subpath "/System/iOSSupport/System/Library/SubFrameworks")
  (subpath "/usr/lib"))

; Allow guarded vnodes.
(allow system-mac-syscall (mac-policy-name "vnguard"))

; Determine whether a container is expected.
(allow system-mac-syscall
  (require-all
    (mac-policy-name "Sandbox")
    (mac-syscall-number 67)))

; Allow resolution of standard system symlinks.
(allow file-read-metadata file-test-existence
  (literal "/etc")
  (literal "/tmp")
  (literal "/var")
  (literal "/private/etc/localtime"))

; Allow stat'ing of firmlink parent path components.
(allow file-read-metadata file-test-existence
  (path-ancestors "/System/Volumes/Data/private"))

; Allow processes to get their current working directory.
(allow file-read* file-test-existence
  (literal "/"))

; Allow FSIOC_CAS_BSDFLAGS as alternate chflags.
(allow system-fsctl (fsctl-command FSIOC_CAS_BSDFLAGS))

; Allow access to standard special files.
(allow file-read* file-test-existence
  (literal "/dev/autofs_nowait")
  (literal "/dev/random")
  (literal "/dev/urandom")
  (literal "/private/etc/master.passwd")
  (literal "/private/etc/passwd")
  (literal "/private/etc/protocols")
  (literal "/private/etc/services"))

; Allow null/zero read/write.
(allow file-read* file-test-existence file-write-data
  (literal "/dev/null")
  (literal "/dev/zero"))

; Allow read/write access to the file descriptors.
(allow file-read-data file-test-existence file-write-data
  (subpath "/dev/fd"))

; Provide access to debugger helpers.
(allow file-read* file-test-existence file-write-data file-ioctl
  (literal "/dev/dtracehelper"))

; Allow reading standard config directories.
(allow file-read* (subpath "/etc"))
(allow file-read* (subpath "/private/etc"))

(allow file-read* file-test-existence
  (literal "/System/Library/CoreServices")
  (literal "/System/Library/CoreServices/.SystemVersionPlatform.plist")
  (literal "/System/Library/CoreServices/SystemVersion.plist"))

; Some processes read /var metadata during startup.
(allow file-read-metadata (subpath "/var"))
(allow file-read-metadata (subpath "/private/var"))

; IOKit access for root domain services.
(allow iokit-open
  (iokit-registry-entry-class "RootDomainUserClient"))

; macOS Standard library queries opendirectoryd at startup
(allow mach-lookup (global-name "com.apple.system.opendirectoryd.libinfo"))

; Allow IPC to analytics, logging, trust, and other system agents.
(allow mach-lookup
  (global-name "com.apple.analyticsd")
  (global-name "com.apple.analyticsd.messagetracer")
  (global-name "com.apple.appsleep")
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.diagnosticd")
  (global-name "com.apple.dt.automationmode.reader")
  (global-name "com.apple.espd")
  (global-name "com.apple.logd")
  (global-name "com.apple.logd.events")
  (global-name "com.apple.runningboard")
  (global-name "com.apple.secinitd")
  (global-name "com.apple.system.DirectoryService.libinfo_v1")
  (global-name "com.apple.system.logger")
  (global-name "com.apple.system.notification_center")
  (global-name "com.apple.system.opendirectoryd.membership")
  (global-name "com.apple.trustd")
  (global-name "com.apple.trustd.agent")
  (global-name "com.apple.xpc.activity.unmanaged"))

; Allow IPC to the syslog socket for logging.
(allow network-outbound (literal "/private/var/run/syslog"))

; macOS Notifications
(allow ipc-posix-shm-read*
  (ipc-posix-name "apple.shm.notification_center"))

; Regulatory domain support.
(allow file-read*
  (literal "/private/var/db/eligibilityd/eligibility.plist"))

; Audio and power management services.
(allow mach-lookup (global-name "com.apple.audio.audiohald"))
(allow mach-lookup (global-name "com.apple.audio.AudioComponentRegistrar"))
(allow mach-lookup (global-name "com.apple.PowerManagement.control"))

; Allow reading the minimum system runtime so exec works.
(allow file-read-data (subpath "/bin"))
(allow file-read-metadata (subpath "/bin"))
(allow file-read-data (subpath "/sbin"))
(allow file-read-metadata (subpath "/sbin"))
(allow file-read-data (subpath "/usr/bin"))
(allow file-read-metadata (subpath "/usr/bin"))
(allow file-read-data (subpath "/usr/sbin"))
(allow file-read-metadata (subpath "/usr/sbin"))
(allow file-read-data (subpath "/usr/libexec"))
(allow file-read-metadata (subpath "/usr/libexec"))

(allow file-read* (subpath "/Library/Preferences"))
(allow file-read* (subpath "/opt/homebrew/lib"))
(allow file-read* (subpath "/usr/local/lib"))

; Terminal basics and device handles.
(allow file-read* (regex "^/dev/fd/(0|1|2)$"))
(allow file-write* (regex "^/dev/fd/(1|2)$"))
(allow file-read* file-write* (literal "/dev/null"))
(allow file-read* file-write* (literal "/dev/tty"))
(allow file-read-metadata (literal "/dev"))
(allow file-read-metadata (regex "^/dev/.*$"))
(allow file-read-metadata (literal "/dev/stdin"))
(allow file-read-metadata (literal "/dev/stdout"))
(allow file-read-metadata (literal "/dev/stderr"))
(allow file-read-metadata (regex "^/dev/tty[^/]*$"))
(allow file-read-metadata (regex "^/dev/pty[^/]*$"))
(allow file-read* file-write* (regex "^/dev/ttys[0-9]+$"))
(allow file-read* file-write* (literal "/dev/ptmx"))
(allow file-ioctl (regex "^/dev/ttys[0-9]+$"))

; Allow metadata traversal for firmlink parents.
(allow file-read-metadata (literal "/System/Volumes") (vnode-type DIRECTORY))
(allow file-read-metadata (literal "/System/Volumes/Data") (vnode-type DIRECTORY))
(allow file-read-metadata (literal "/System/Volumes/Data/Users") (vnode-type DIRECTORY))

; App sandbox extensions
(allow file-read* (extension "com.apple.app-sandbox.read"))
(allow file-read* file-write* (extension "com.apple.app-sandbox.read-write"))
`

// networkPolicy is the reference's network policy: the system sockets and
// Mach lookups a process needs once outbound access is granted. The
// outbound allowance itself is added by BuildPolicy, so a caller opts in
// to exactly one capability.
// processDefaults are the scratch directories and application reads an
// ordinary process expects, ported from the reference's process platform
// defaults.
const processDefaults = `
(allow file-read* (subpath "/Applications"))
(allow file-read* file-test-existence file-write* (subpath "/tmp"))
(allow file-read* file-write* (subpath "/private/tmp"))
(allow file-read* file-write* (subpath "/var/tmp"))
(allow file-read* file-write* (subpath "/private/var/tmp"))
`

// networkPolicy is the reference's network policy: the system sockets and
// Mach lookups a process needs once outbound access is granted.
const networkPolicy = `
; when network access is enabled, these policies are added after the base policy.
; proxy-specific allow rules are injected by codex-core based on environment.
; Ref https://source.chromium.org/chromium/chromium/src/+/main:sandbox/policy/mac/network.sb;drc=f8f264d5e4e7509c913f4c60c2639d15905a07e4

; allow only safe AF_SYSTEM sockets used for local platform services.
(allow system-socket
  (require-all
    (socket-domain AF_SYSTEM)
    (socket-protocol 2)
  )
)

(allow mach-lookup
    ; Used by platform helpers that resolve user directory locations.
    (global-name "com.apple.bsd.dirhelper")
    (global-name "com.apple.system.opendirectoryd.membership")

    ; Communicate with the security server for TLS certificate information.
    (global-name "com.apple.SecurityServer")
    (global-name "com.apple.networkd")
    (global-name "com.apple.ocspd")
    (global-name "com.apple.trustd.agent")

    ; Read network configuration.
    (global-name "com.apple.SystemConfiguration.DNSConfiguration")
    (global-name "com.apple.SystemConfiguration.configd")
)

(allow sysctl-read
  (sysctl-name-regex #"^net.routetable")
)
`
