package cli

import (
	"bytes"
	"context"
	"flag"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/bwrap"
	"github.com/feiyu912/zenforge/sandbox/docker"
	"github.com/feiyu912/zenforge/sandbox/linuxsandbox"
	"github.com/feiyu912/zenforge/sandbox/seatbelt"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
)

func TestValidateSandboxBackend(t *testing.T) {
	// Surrounding whitespace and case are normalized, so a configuration
	// written by hand is not rejected for formatting.
	for _, backend := range []string{"", "none", "seatbelt", "bwrap", "docker", "landlock", "BWRAP", " seatbelt "} {
		if err := validateSandboxBackend(backend); err != nil {
			t.Fatalf("backend %q rejected: %v", backend, err)
		}
	}
	for _, backend := range []string{"chroot", "bwrap2", "seatbelt2"} {
		if err := validateSandboxBackend(backend); err == nil {
			t.Fatalf("backend %q was accepted", backend)
		}
	}
}

func TestBuildSandboxSelectsTheConfiguredBackend(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		backend string
		want    string
	}{
		{"", ""},
		{"none", ""},
		{"seatbelt", "seatbelt"},
		{"bwrap", "bwrap"},
		{"docker", "docker"},
		{"landlock", "landlock"},
	}
	for _, testCase := range cases {
		t.Run(testCase.backend, func(t *testing.T) {
			built, err := buildSandbox(sandboxOptions{Backend: testCase.backend}, dir, time.Second)
			if err != nil {
				t.Fatalf("buildSandbox returned error: %v", err)
			}
			if testCase.want == "" {
				if built != nil {
					t.Fatalf("backend %q produced a sandbox: %#v", testCase.backend, built)
				}
				return
			}
			if built == nil {
				t.Fatalf("backend %q produced no sandbox", testCase.backend)
			}
			switch testCase.want {
			case "seatbelt":
				if _, ok := built.(*seatbelt.Adapter); !ok {
					t.Fatalf("backend %q produced %T", testCase.backend, built)
				}
			case "bwrap":
				if _, ok := built.(*bwrap.Adapter); !ok {
					t.Fatalf("backend %q produced %T", testCase.backend, built)
				}
			case "docker":
				if _, ok := built.(*docker.Adapter); !ok {
					t.Fatalf("backend %q produced %T", testCase.backend, built)
				}
			case "landlock":
				if _, ok := built.(*linuxsandbox.Adapter); !ok {
					t.Fatalf("backend %q produced %T", testCase.backend, built)
				}
			}
		})
	}
	if _, err := buildSandbox(sandboxOptions{Backend: "chroot"}, dir, time.Second); err == nil {
		t.Fatal("an unknown backend was accepted")
	}
	// A protected-name policy cannot be expressed by the landlock backend,
	// so it is refused when the backend is built rather than at the first
	// command.
	if _, err := buildSandbox(sandboxOptions{Backend: "landlock", ProtectedNames: []string{".git"}}, dir, time.Second); err == nil {
		t.Fatal("the landlock backend accepted a protected-name policy")
	}
}

func TestSandboxFlagsParseAndValidate(t *testing.T) {
	t.Run("unknown backend", func(t *testing.T) {
		var stderr bytes.Buffer
		code := Main(context.Background(), []string{"run", "--sandbox", "chroot", "hello"}, IO{Stderr: &stderr})
		if code != exitInvalidUsage {
			t.Fatalf("code = %d, want %d", code, exitInvalidUsage)
		}
		if !strings.Contains(stderr.String(), "unknown sandbox backend") {
			t.Fatalf("unexpected stderr: %q", stderr.String())
		}
	})
	t.Run("flags bind", func(t *testing.T) {
		opts := defaultOptions()
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		bindOptions(fs, &opts)
		args := []string{
			"--sandbox", "bwrap",
			"--sandbox-root", "/one",
			"--sandbox-root", "/two",
			"--sandbox-allow-network",
			"--sandbox-restricted",
			"--sandbox-image", "alpine:3.20",
			"--sandbox-timeout", "45s",
			"--sandbox-protected", ".git",
			"--plan",
			"--goals",
			"--jobs",
			"--goal-max-rounds", "5",
		}
		if err := fs.Parse(args); err != nil {
			t.Fatalf("Parse returned error: %v", err)
		}
		if opts.sandboxBackend != "bwrap" || len(opts.sandboxRoots) != 2 {
			t.Fatalf("sandbox options = %#v", opts)
		}
		if !opts.sandboxAllowNetwork || !opts.sandboxRestricted || opts.sandboxImage != "alpine:3.20" {
			t.Fatalf("sandbox options = %#v", opts)
		}
		if opts.sandboxTimeout != 45*time.Second || len(opts.sandboxProtected) != 1 {
			t.Fatalf("sandbox options = %#v", opts)
		}
		// The plan and goal flags exist too; they were documented but were
		// previously reachable only through configuration.
		if !opts.planMode || !opts.goalsEnabled || opts.goalMaxRounds != 5 {
			t.Fatalf("plan/goal options = %#v", opts)
		}
		if !opts.jobsEnabled {
			t.Fatalf("jobs options = %#v", opts)
		}
		if err := validateOptionEnums(opts); err != nil {
			t.Fatalf("validateOptionEnums returned error: %v", err)
		}
	})
}
func TestLinuxSandboxHelperSubcommandIsWired(t *testing.T) {
	// The helper subcommand exists and reports why it cannot run here. It is
	// deliberately not exercised on Linux in-process: a successful helper
	// execs, which would replace the test process.
	if runtime.GOOS == "linux" {
		t.Skip("the helper replaces the process on success")
	}
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"linux-sandbox", "--policy", `{"fullDiskRead":true}`, "--", "/bin/true"}, IO{Stdout: &bytes.Buffer{}, Stderr: &stderr})
	if code == 0 {
		t.Fatalf("the helper succeeded off Linux: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "landlock") {
		t.Fatalf("the failure does not explain the missing layer: %q", stderr.String())
	}
}

func TestDockerSandboxMountsTheWritableRoots(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()

	t.Run("the working directory by default", func(t *testing.T) {
		// A container has a filesystem of its own, so --sandbox-root has to
		// become a bind mount. Without it the documented default ("the shell
		// working directory") was silently a no-op for the docker backend:
		// the model's shell ran in an empty directory and could not read the
		// project at all.
		opts := defaultOptions()
		opts.sandboxBackend = SandboxDocker
		opts.shellWorkingDir = workspace
		config, err := shellToolConfig(opts)
		if err != nil {
			t.Fatalf("shellToolConfig returned error: %v", err)
		}
		if config.Backend != shelltool.ShellBackendSandbox {
			t.Fatalf("backend = %q, want %q", config.Backend, shelltool.ShellBackendSandbox)
		}
		mount := singleMount(t, config.Mounts)
		if mount.Source != workspace || mount.Destination != workspace {
			t.Fatalf("mount = %#v, want the workspace mounted at its host path", mount)
		}
		if mount.Mode != "rw" {
			t.Fatalf("mode = %q, want rw: --sandbox-root is documented as a writable root", mount.Mode)
		}
	})

	t.Run("explicit roots and the restricted layout", func(t *testing.T) {
		opts := defaultOptions()
		opts.sandboxBackend = SandboxDocker
		opts.shellWorkingDir = workspace
		opts.sandboxRoots = []string{workspace, extra, workspace}
		opts.sandboxRestricted = true
		config, err := shellToolConfig(opts)
		if err != nil {
			t.Fatalf("shellToolConfig returned error: %v", err)
		}
		if len(config.Mounts) != 2 {
			t.Fatalf("mounts = %#v, want two: explicit roots in order, duplicates dropped", config.Mounts)
		}
		for _, mount := range config.Mounts {
			if mount.Mode != "ro" {
				t.Fatalf("mode = %q, want ro under --sandbox-restricted", mount.Mode)
			}
		}
	})

	t.Run("no mounts for the non-container backends", func(t *testing.T) {
		// The local shell runs on the host, and the other backends confine
		// it in place; a bind mount would be meaningless or misleading.
		for _, backend := range []string{"", SandboxNone, SandboxBwrap, SandboxLandlock} {
			opts := defaultOptions()
			opts.sandboxBackend = backend
			opts.shellWorkingDir = workspace
			config, err := shellToolConfig(opts)
			if err != nil {
				t.Fatalf("backend %q returned error: %v", backend, err)
			}
			if len(config.Mounts) != 0 {
				t.Fatalf("backend %q produced mounts: %#v", backend, config.Mounts)
			}
		}
	})
}

func singleMount(t *testing.T, mounts []sandbox.Mount) sandbox.Mount {
	t.Helper()
	if len(mounts) != 1 {
		t.Fatalf("mounts = %#v, want exactly one", mounts)
	}
	return mounts[0]
}
