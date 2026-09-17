// Package bwrap provides a Linux bubblewrap (bwrap) sandbox backend. The
// sandbox is expressed as an argument list: a fresh mount namespace with a
// read-only root (or a tmpfs root plus the approved read roots), writable
// bind mounts for the declared roots, read-only rebinds for protected
// metadata such as `.git`, and no network unless the caller opts in. The
// argument list is built and validated independently of execution, so the
// policy is auditable and testable on any host.
//
// Seccomp filtering is not applied: bubblewrap sets up namespaces and bind
// mounts, and the reference applies its syscall filter through a separate
// helper process. Commands therefore keep the host's syscall surface; the
// filesystem and network boundaries are what this backend enforces.
package bwrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Default executable name.
const defaultExecutable = "bwrap"

// platformDefaultReadRoots are the read-only roots a toolchain expects on
// Linux, ported from the reference.
var platformDefaultReadRoots = []string{
	"/bin",
	"/sbin",
	"/usr",
	"/etc",
	"/lib",
	"/lib64",
	"/nix/store",
	"/run/current-system/sw",
}

// defaultProtectedNames are pinned read-only inside every writable root.
var defaultProtectedNames = []string{".git", ".zenforge"}

// Policy describes one bubblewrap mount layout.
type Policy struct {
	// WritableRoots are the only paths mounted read-write. They must be
	// absolute and exist, because bubblewrap cannot bind a missing target.
	WritableRoots []string
	// ReadableRoots are extra read-only roots for a restricted layout.
	ReadableRoots []string
	// ReadOnlyPaths are paths that must stay read-only even inside a
	// writable root. They are rebound read-only after the writable mount.
	ReadOnlyPaths []string
	// ProtectedNames pins these basenames read-only inside every writable
	// root. Empty selects the defaults.
	ProtectedNames []string
	// FullDiskRead starts from a read-only bind of the host root. When it is
	// false the sandbox starts from an empty tmpfs and binds only the
	// approved read roots.
	FullDiskRead bool
	// IncludePlatformDefaults adds the platform read roots to a restricted
	// layout.
	IncludePlatformDefaults bool
	// AllowNetwork keeps the host network namespace. The default creates a
	// new one, so the sandbox has no network at all.
	AllowNetwork bool
	// MountProc mounts a fresh /proc inside the sandbox. It defaults to
	// true; a fresh procfs hides host process roots.
	MountProc *bool
	// ExtraArgs are inserted before the terminating "--", for
	// host-specific bubblewrap flags.
	ExtraArgs []string
}

// Command is a prepared bubblewrap invocation.
type Command struct {
	// Executable is the bubblewrap binary.
	Executable string
	// Args is the full argument list, ending with the sandboxed command.
	Args []string
	// WorkingDir is the directory the sandboxed command starts in.
	WorkingDir string
}

// BuildArgs renders the policy as a bubblewrap argument list.
func BuildArgs(policy Policy, command []string, workingDir string) (Command, error) {
	if len(command) == 0 {
		return Command{}, fmt.Errorf("bwrap requires a command")
	}
	cwd := strings.TrimSpace(workingDir)
	if cwd == "" {
		current, err := os.Getwd()
		if err != nil {
			return Command{}, err
		}
		cwd = current
	}
	if !filepath.IsAbs(cwd) {
		return Command{}, fmt.Errorf("bwrap working directory %q must be absolute", workingDir)
	}
	cwd = canonicalPath(cwd)

	writable, err := normalizeRoots(policy.WritableRoots, "writable root")
	if err != nil {
		return Command{}, err
	}
	readable, err := normalizeRoots(policy.ReadableRoots, "readable root")
	if err != nil {
		return Command{}, err
	}
	readOnly, err := normalizeRoots(policy.ReadOnlyPaths, "read-only path")
	if err != nil {
		return Command{}, err
	}
	protected := policy.ProtectedNames
	if len(protected) == 0 {
		protected = defaultProtectedNames
	}
	for _, name := range protected {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, `/\`) {
			return Command{}, fmt.Errorf("bwrap protected name %q must be a plain basename", name)
		}
	}

	args := []string{
		// A new session detaches from the controlling terminal, and
		// die-with-parent guarantees the sandbox dies with the agent.
		"--new-session",
		"--die-with-parent",
	}

	// Root filesystem: either the host read-only, or an empty tmpfs with
	// the approved read roots bound in. /dev is mounted after the root so
	// explicit /dev binds stay visible, matching the reference order.
	if policy.FullDiskRead {
		args = append(args, "--ro-bind", "/", "/")
	} else {
		args = append(args, "--tmpfs", "/")
		roots := append([]string(nil), readable...)
		if policy.IncludePlatformDefaults {
			for _, root := range platformDefaultReadRoots {
				if _, err := os.Stat(root); err == nil {
					roots = append(roots, root)
				}
			}
		}
		roots, err = normalizeRoots(roots, "readable root")
		if err != nil {
			return Command{}, err
		}
		for _, root := range roots {
			// A plain read root is bound at its logical path: callers may
			// execute binaries from it inside the sandbox. A root that sits
			// under a writable root is bound at its canonical target so the
			// later writable bind lines up.
			mount := root
			for _, writableRoot := range writable {
				if pathWithin(root, writableRoot) {
					mount = canonicalPath(root)
					break
				}
			}
			args = append(args, "--ro-bind", mount, mount)
		}
	}
	args = append(args, "--dev", "/dev", "--bind-try", "/dev/shm", "/dev/shm")

	// Writable roots, shallowest first, so a nested writable root wins over
	// its parent's protections.
	sorted := append([]string(nil), writable...)
	sort.SliceStable(sorted, func(i, j int) bool { return pathDepth(sorted[i]) < pathDepth(sorted[j]) })
	for _, root := range sorted {
		target := canonicalPath(root)
		args = append(args, "--bind", target, target)
	}

	// Explicit read-only paths are rebound after the writable mounts, so
	// the read-only bind wins for the paths it names.
	for _, path := range readOnly {
		args = append(args, "--ro-bind-try", path, path)
	}
	// Protected metadata inside each writable root: bind the existing path
	// read-only over itself. A path that does not exist yet is skipped;
	// bubblewrap cannot mask a missing target, so a command could create it
	// in that case, which the documentation calls out.
	for _, root := range sorted {
		for _, name := range protected {
			path := filepath.Join(canonicalPath(root), name)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			args = append(args, "--ro-bind", path, path)
		}
	}

	args = append(args,
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		// Clear the capability set: a user namespace plus all dropped
		// capabilities removes the paths that would otherwise re-open the
		// host filesystem.
		"--cap-drop", "ALL",
	)
	if !policy.AllowNetwork {
		args = append(args, "--unshare-net")
	}
	if policy.MountProc == nil || *policy.MountProc {
		args = append(args, "--proc", "/proc")
	}
	args = append(args, "--chdir", cwd)
	args = append(args, policy.ExtraArgs...)
	args = append(args, "--")
	args = append(args, command...)
	return Command{Executable: defaultExecutable, Args: args, WorkingDir: cwd}, nil
}

// normalizeRoots validates, canonicalizes, and deduplicates a path list.
func normalizeRoots(paths []string, label string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("bwrap %s %q must be absolute", label, raw)
		}
		cleaned := canonicalPath(path)
		if cleaned == "/" {
			return nil, fmt.Errorf("bwrap %s must not be the filesystem root", label)
		}
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	return out, nil
}

// pathWithin reports whether path is inside root (or is root).
func pathWithin(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

// pathDepth counts a path's components.
func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}

// canonicalPath resolves symlinks where it can, and otherwise resolves the
// longest existing ancestor, so a path that does not exist yet still lines
// up with the canonical form of the roots it belongs to.
func canonicalPath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		if absolute, err := filepath.Abs(resolved); err == nil {
			return filepath.Clean(absolute)
		}
		return filepath.Clean(resolved)
	}
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
		return filepath.Join(append([]string{resolved}, remainder...)...)
	}
}
