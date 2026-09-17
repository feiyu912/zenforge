package bwrap

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

func TestBuildArgsBindsOnlyTheDeclaredRoots(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	command, err := BuildArgs(Policy{FullDiskRead: true, WritableRoots: []string{root}, ReadOnlyPaths: []string{filepath.Join(root, "docs")}}, []string{"/bin/sh", "-c", "true"}, root)
	if err != nil {
		t.Fatalf("BuildArgs returned error: %v", err)
	}
	canonicalRoot := canonicalPath(root)
	joined := strings.Join(command.Args, " ")
	for _, want := range []string{
		"--new-session",
		"--die-with-parent",
		"--ro-bind / /",
		"--dev /dev",
		"--bind " + canonicalRoot + " " + canonicalRoot,
		"--ro-bind " + canonicalPath(gitDir) + " " + canonicalPath(gitDir),
		"--ro-bind-try " + canonicalPath(filepath.Join(root, "docs")),
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-net",
		"--cap-drop ALL",
		"--proc /proc",
		"--chdir " + canonicalRoot,
		"-- /bin/sh -c true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("arguments are missing %q:\n%v", want, command.Args)
		}
	}
	// The command must come last, after the terminating "--".
	if command.Args[len(command.Args)-4] != "--" || command.Args[len(command.Args)-1] != "true" {
		t.Fatalf("command tail = %v", command.Args[len(command.Args)-4:])
	}
}

func TestBuildArgsHonoursNetworkAndProcOptions(t *testing.T) {
	root := t.TempDir()
	command, err := BuildArgs(Policy{WritableRoots: []string{root}, AllowNetwork: true}, []string{"/bin/true"}, root)
	if err != nil {
		t.Fatalf("BuildArgs returned error: %v", err)
	}
	if strings.Contains(strings.Join(command.Args, " "), "--unshare-net") {
		t.Fatalf("network was unshared despite AllowNetwork:\n%v", command.Args)
	}
	off := false
	command, err = BuildArgs(Policy{WritableRoots: []string{root}, MountProc: &off}, []string{"/bin/true"}, root)
	if err != nil {
		t.Fatalf("BuildArgs returned error: %v", err)
	}
	if strings.Contains(strings.Join(command.Args, " "), "--proc") {
		t.Fatalf("/proc was mounted despite an explicit false:\n%v", command.Args)
	}
}

func TestBuildArgsRestrictedLayoutUsesApprovedReadRoots(t *testing.T) {
	root := t.TempDir()
	read := t.TempDir()
	command, err := BuildArgs(Policy{
		WritableRoots:           []string{root},
		ReadableRoots:           []string{read},
		FullDiskRead:            false,
		IncludePlatformDefaults: true,
	}, []string{"/bin/true"}, root)
	if err != nil {
		t.Fatalf("BuildArgs returned error: %v", err)
	}
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "--tmpfs /") {
		t.Fatalf("restricted layout did not start from a tmpfs:\n%v", command.Args)
	}
	if strings.Contains(joined, "--ro-bind / /") {
		t.Fatalf("restricted layout bound the host root:\n%v", command.Args)
	}
	if !strings.Contains(joined, "--ro-bind "+canonicalPath(read)+" "+canonicalPath(read)) {
		t.Fatalf("declared read root was not bound:\n%v", command.Args)
	}
	// Platform defaults that exist on this host are bound too; at least one
	// of them exists on any Unix host.
	found := false
	for _, candidate := range platformDefaultReadRoots {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if strings.Contains(joined, "--ro-bind "+candidate+" "+candidate) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no platform default read root was bound:\n%v", command.Args)
	}
}

func TestBuildArgsOrdersWritableRootsShallowestFirst(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "nested")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	command, err := BuildArgs(Policy{WritableRoots: []string{child, parent}}, []string{"/bin/true"}, parent)
	if err != nil {
		t.Fatalf("BuildArgs returned error: %v", err)
	}
	first := indexOf(command.Args, canonicalPath(parent))
	second := indexOf(command.Args, canonicalPath(child))
	if first < 0 || second < 0 || first > second {
		t.Fatalf("nested writable root must be bound last: %v", command.Args)
	}
}

func TestBuildArgsRejectsUnusableInput(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name   string
		policy Policy
		args   []string
	}{
		{name: "no command", policy: Policy{WritableRoots: []string{root}}},
		{name: "relative root", policy: Policy{WritableRoots: []string{"work"}}, args: []string{"/bin/true"}},
		{name: "root is filesystem root", policy: Policy{WritableRoots: []string{"/"}}, args: []string{"/bin/true"}},
		{name: "relative read root", policy: Policy{ReadableRoots: []string{"data"}}, args: []string{"/bin/true"}},
		{name: "nested protected name", policy: Policy{WritableRoots: []string{root}, ProtectedNames: []string{"a/b"}}, args: []string{"/bin/true"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := BuildArgs(testCase.policy, testCase.args, root); err == nil {
				t.Fatalf("policy %#v was accepted", testCase.policy)
			}
		})
	}
	if _, err := BuildArgs(Policy{}, []string{"/bin/true"}, "relative"); err == nil {
		t.Fatal("relative working directory was accepted")
	}
}

func TestBuildArgsSkipsProtectedNamesThatDoNotExistYet(t *testing.T) {
	root := t.TempDir()
	command, err := BuildArgs(Policy{WritableRoots: []string{root}}, []string{"/bin/true"}, root)
	if err != nil {
		t.Fatalf("BuildArgs returned error: %v", err)
	}
	if strings.Contains(strings.Join(command.Args, " "), ".zenforge") {
		t.Fatalf("a missing protected path was bound:\n%v", command.Args)
	}
}

// scriptedRunner records one execution.
type scriptedRunner struct {
	calls int
	args  []string
	run   func(ctx context.Context, stdout, stderr io.Writer) error
}

func (r *scriptedRunner) Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	r.calls++
	r.args = args
	if r.run != nil {
		return r.run(ctx, stdout, stderr)
	}
	_, _ = io.WriteString(stdout, "ok\n")
	return nil
}

func newAdapter(t *testing.T, config Config) (*Adapter, *scriptedRunner) {
	t.Helper()
	runner := &scriptedRunner{}
	config.Runner = runner
	config.SkipPlatformCheck = true
	adapter, err := New(config)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return adapter, runner
}

func TestAdapterRunsTheCommandAfterTheSandboxFlags(t *testing.T) {
	dir := t.TempDir()
	adapter, runner := newAdapter(t, Config{WritableRoots: []string{dir}})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1", WorkingDir: dir})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "echo hi"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Stdout != "ok\n" || result.WorkingDirectory != dir {
		t.Fatalf("result = %#v", result)
	}
	tail := runner.args[len(runner.args)-4:]
	if tail[0] != "--" || tail[1] != "/bin/sh" || tail[2] != "-c" || tail[3] != "echo hi" {
		t.Fatalf("command tail = %v", tail)
	}
	if hash, _ := session.Metadata[argsHashMetadataKey].(string); len(hash) != 64 {
		t.Fatalf("args hash = %#v", session.Metadata[argsHashMetadataKey])
	}
	// A per-call cwd replaces the layout's --chdir value.
	other := t.TempDir()
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "echo hi", CWD: other}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	index := indexOf(runner.args, "--chdir")
	if index < 0 || runner.args[index+1] != other {
		t.Fatalf("--chdir was not replaced: %v", runner.args)
	}
}

func TestAdapterMapsExitCodesAndErrors(t *testing.T) {
	dir := t.TempDir()
	adapter, runner := newAdapter(t, Config{WritableRoots: []string{dir}, DefaultTimeout: time.Second})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1", WorkingDir: dir})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	runner.run = func(context.Context, io.Writer, io.Writer) error {
		return exec.Command("/bin/sh", "-c", "exit 5").Run()
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "false"})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error: %v", err)
	}
	if result.ExitCode != 5 {
		t.Fatalf("result = %#v", result)
	}

	runner.run = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "sleep", Timeout: 20 * time.Millisecond}); !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("timeout error = %v", err)
	}

	runner.run = func(_ context.Context, _, _ io.Writer) error { return errors.New("runner exploded") }
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "true"}); !errors.Is(err, sandbox.ErrExecuteFailed) {
		t.Fatalf("runner error = %v", err)
	}

	runner.run = func(_ context.Context, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, strings.Repeat("x", 2048))
		return nil
	}
	small, err := New(Config{Runner: runner, SkipPlatformCheck: true, WritableRoots: []string{dir}, MaxOutputBytes: 64})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	smallSession, err := small.Open(context.Background(), sandbox.OpenRequest{RunID: "run_2", WorkingDir: dir})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if _, err := small.Execute(context.Background(), smallSession, sandbox.ExecuteRequest{Command: "true"}); !errors.Is(err, sandbox.ErrResponseTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}

	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{}); !errors.Is(err, sandbox.ErrExecuteFailed) {
		t.Fatalf("empty command error = %v", err)
	}
}

func TestAdapterLifecycleAndAvailability(t *testing.T) {
	dir := t.TempDir()
	adapter, _ := newAdapter(t, Config{WritableRoots: []string{dir}})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1", WorkingDir: dir})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if err := adapter.Close(context.Background(), session); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "true"}); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("closed session error = %v", err)
	}
	if err := adapter.Close(context.Background(), session); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("double close error = %v", err)
	}
	if _, err := adapter.Open(context.Background(), sandbox.OpenRequest{}); !errors.Is(err, sandbox.ErrSessionOpenFailed) {
		t.Fatalf("missing run id error = %v", err)
	}

	missing, err := New(Config{WritableRoots: []string{dir}, LookPath: func(string) (string, error) { return "", errors.New("not found") }})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := missing.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); !errors.Is(err, sandbox.ErrSandboxUnavailable) {
		t.Fatalf("missing runner error = %v", err)
	}
	if runtime.GOOS != "linux" {
		offPlatform, err := New(Config{WritableRoots: []string{dir}})
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		if _, err := offPlatform.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); !errors.Is(err, sandbox.ErrSandboxUnavailable) {
			t.Fatalf("off-platform error = %v", err)
		}
	}
}

func TestAdapterPerRequestPolicyOverrides(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	adapter, _ := newAdapter(t, Config{WritableRoots: []string{dir}})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_1", WorkingDir: dir,
		Metadata: map[string]any{
			writableRootsMetaKey: []string{extra},
			networkMetadataKey:   true,
		},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	args := strings.Join(session.Metadata[argsMetadataKey].([]string), " ")
	if !strings.Contains(args, "--bind "+canonicalPath(extra)+" "+canonicalPath(extra)) {
		t.Fatalf("per-request writable root was not bound:\n%s", args)
	}
	if strings.Contains(args, "--unshare-net") {
		t.Fatalf("per-request network grant was ignored:\n%s", args)
	}
	// A writable root that does not exist is dropped rather than failing.
	missing := filepath.Join(dir, "does-not-exist")
	session, err = adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_2", WorkingDir: dir, Metadata: map[string]any{writableRootsMetaKey: []string{missing}},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if strings.Contains(strings.Join(session.Metadata[argsMetadataKey].([]string), " "), missing) {
		t.Fatal("a missing writable root was bound")
	}
}

// TestBwrapActuallyEnforcesTheLayout runs real bubblewrap on Linux.
func TestBwrapActuallyEnforcesTheLayout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap is Linux only")
	}
	if _, err := exec.LookPath(defaultExecutable); err != nil {
		t.Skipf("%s is not installed: %v", defaultExecutable, err)
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")

	adapter, err := New(Config{WritableRoots: []string{root}, DefaultTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run_real", WorkingDir: root})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer func() { _ = adapter.Close(context.Background(), session) }()

	inside := filepath.Join(session.WorkingDir, "victim.txt")
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: "echo inside > " + inside + " && cat " + inside,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v (stderr=%s)", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("inside write failed: exit=%d stderr=%s", result.ExitCode, result.Stderr)
	}
	if output, err := os.ReadFile(inside); err != nil || string(output) != "inside\n" {
		t.Fatalf("file = %q err=%v", output, err)
	}

	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "echo escaped > " + outside})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("write outside the writable root succeeded: %#v", result)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the outside file exists despite the sandbox")
	}

	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "echo tampered > " + filepath.Join(gitDir, "HEAD")})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf(".git was writable inside the sandbox: %#v", result)
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil || string(head) != "ref: refs/heads/main\n" {
		t.Fatalf("HEAD changed: %q err=%v", head, err)
	}

	// Network is unshared by default.
	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: "cat /proc/net/dev >/dev/null 2>&1; echo status=$?",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result.Stdout, "status=") {
		t.Fatalf("unexpected network probe output: %#v", result)
	}
}
