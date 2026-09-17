package cli

import (
	"context"
	"fmt"
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

// buildSandbox constructs the configured backend. An empty or "none"
// backend returns a nil sandbox, which keeps the shell local.
func buildSandbox(opts sandboxOptions, defaultWorkingDir string, fallbackTimeout time.Duration) (sandbox.Sandbox, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = fallbackTimeout
	}
	roots := append([]string(nil), opts.Roots...)
	if len(roots) == 0 && defaultWorkingDir != "" {
		roots = append(roots, defaultWorkingDir)
	}
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
