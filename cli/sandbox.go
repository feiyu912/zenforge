package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/bwrap"
	"github.com/feiyu912/zenforge/sandbox/docker"
	"github.com/feiyu912/zenforge/sandbox/linuxsandbox"
	"github.com/feiyu912/zenforge/sandbox/seatbelt"
)

// Sandbox backend names accepted by --sandbox and `shell.sandbox.backend`.
const (
	SandboxNone     = "none"
	SandboxSeatbelt = "seatbelt"
	SandboxBwrap    = "bwrap"
	SandboxDocker   = "docker"
	// SandboxLandlock is the in-process Linux sandbox: Landlock for the
	// filesystem and seccomp for the network, applied by a helper process.
	SandboxLandlock = "landlock"
)

// sandboxOptions is the resolved sandbox configuration.
type sandboxOptions struct {
	Backend      string
	Roots        []string
	AllowNetwork bool
	// Restricted selects bubblewrap's empty-root layout instead of the
	// read-only host root.
	Restricted bool
	// Image and EnvironmentID are the Docker image and the environment a
	// session reports.
	Image         string
	EnvironmentID string
	// Timeout bounds one sandboxed execution.
	Timeout time.Duration
	// ProtectedNames pins metadata read-only inside writable roots.
	ProtectedNames []string
}

// validateSandboxBackend checks the backend name before anything is built.
func validateSandboxBackend(backend string) error {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", SandboxNone, SandboxSeatbelt, SandboxBwrap, SandboxDocker, SandboxLandlock:
		return nil
	default:
		return fmt.Errorf("unknown sandbox backend %q (want none, seatbelt, bwrap, docker, or landlock)", backend)
	}
}

// isDockerBackend reports whether the configured backend is the container
// backend, which is the only one that needs explicit bind mounts.
func isDockerBackend(backend string) bool {
	return strings.EqualFold(strings.TrimSpace(backend), SandboxDocker)
}

// sandboxRoots is the resolved writable-root list: the explicit
// --sandbox-root values, or the shell working directory when none were given.
// buildSandbox and the Docker mount wiring must agree on it, or the
// documented "the shell working directory by default" promise holds on one
// backend and not the other.
func sandboxRoots(opts sandboxOptions, defaultWorkingDir string) []string {
	roots := append([]string(nil), opts.Roots...)
	if len(roots) == 0 && defaultWorkingDir != "" {
		roots = append(roots, defaultWorkingDir)
	}
	return roots
}

// dockerMounts turns the writable roots into container bind mounts. A Docker
// container has a filesystem of its own: without a mount the model's shell
// runs in an empty working directory and cannot read the project at all, so
// --sandbox-root would be a documented flag with no effect. Each root keeps
// its absolute host path inside the container, which is what lets the shell
// tool map the host working directory onto the mount. Roots are writable
// unless --sandbox-restricted asked for the tighter layout, matching the
// documented meaning of the flag.
func dockerMounts(roots []string, restricted bool) ([]sandbox.Mount, error) {
	mode := "rw"
	if restricted {
		mode = "ro"
	}
	mounts := make([]sandbox.Mount, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		absolute, err := filepath.Abs(trimmed)
		if err != nil {
			return nil, fmt.Errorf("resolving sandbox root %q: %w", root, err)
		}
		if _, ok := seen[absolute]; ok {
			continue
		}
		seen[absolute] = struct{}{}
		mounts = append(mounts, sandbox.Mount{Source: absolute, Destination: absolute, Mode: mode})
	}
	return mounts, nil
}

// buildSandbox constructs the configured backend. An empty or "none"
// backend returns a nil sandbox, which keeps the shell local.
func buildSandbox(opts sandboxOptions, defaultWorkingDir string, fallbackTimeout time.Duration) (sandbox.Sandbox, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = fallbackTimeout
	}
	roots := sandboxRoots(opts, defaultWorkingDir)
	switch strings.ToLower(strings.TrimSpace(opts.Backend)) {
	case "", SandboxNone:
		return nil, nil
	case SandboxSeatbelt:
		return seatbelt.New(seatbelt.Config{
			WritableRoots:     roots,
			AllowNetwork:      opts.AllowNetwork,
			ProtectedNames:    opts.ProtectedNames,
			DefaultTimeout:    timeout,
			DefaultWorkingDir: defaultWorkingDir,
		})
	case SandboxBwrap:
		fullRead := !opts.Restricted
		return bwrap.New(bwrap.Config{
			WritableRoots:     roots,
			AllowNetwork:      opts.AllowNetwork,
			ProtectedNames:    opts.ProtectedNames,
			FullDiskRead:      &fullRead,
			DefaultTimeout:    timeout,
			DefaultWorkingDir: defaultWorkingDir,
		})
	case SandboxDocker:
		network := "none"
		if opts.AllowNetwork {
			network = "bridge"
		}
		return docker.New(docker.Config{
			DefaultImage:      opts.Image,
			DefaultWorkingDir: defaultWorkingDir,
			DefaultTimeout:    timeout,
			NetworkMode:       network,
		})
	case SandboxLandlock:
		// The landlock backend reads the whole filesystem unless the
		// restricted layout is requested, matching the bwrap backend.
		landlockFullRead := !opts.Restricted
		return linuxsandbox.New(linuxsandbox.Config{
			WritableRoots:     roots,
			AllowNetwork:      opts.AllowNetwork,
			ProtectedNames:    opts.ProtectedNames,
			DefaultTimeout:    timeout,
			DefaultWorkingDir: defaultWorkingDir,
			FullDiskRead:      &landlockFullRead,
		})
	default:
		return nil, fmt.Errorf("unknown sandbox backend %q (want none, seatbelt, bwrap, docker, or landlock)", opts.Backend)
	}
}

// linuxSandboxHelper is the hidden helper subcommand. It parses the policy,
// applies Landlock and seccomp, and execs the command; on success it never
// returns.
func linuxSandboxHelper(ctx context.Context, args []string, ioStreams IO) error {
	_, err := linuxsandbox.RunHelper(ctx, args, ioStreams.Stdout, ioStreams.Stderr, linuxsandbox.HelperOptions{})
	return err
}
