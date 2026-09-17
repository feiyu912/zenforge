package seatbelt

import (
	"context"
	"errors"
	"fmt"
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

// scriptedRunner records one execution and returns a scripted outcome.
type scriptedRunner struct {
	calls int
	args  []string
	exe   string
	run   func(ctx context.Context, stdout, stderr io.Writer) error
}

func (r *scriptedRunner) Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	r.calls++
	r.exe = executable
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
	if config.LookPath == nil {
		config.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	}
	adapter, err := New(config)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return adapter, runner
}

func openSession(t *testing.T, adapter *Adapter, workingDir string) *sandbox.Session {
	t.Helper()
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_1", WorkingDir: workingDir, Env: map[string]string{"SESSION_FLAG": "1"},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return session
}

func TestAdapterRunsTheCommandInsideTheGeneratedProfile(t *testing.T) {
	dir := t.TempDir()
	adapter, runner := newAdapter(t, Config{WritableRoots: []string{dir}})
	session := openSession(t, adapter, dir)

	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "echo hi"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Stdout != "ok\n" || result.WorkingDirectory != canonicalPath(dir) {
		t.Fatalf("result = %#v", result)
	}
	joined := strings.Join(runner.args, " ")
	if runner.calls != 1 || !strings.Contains(joined, "-p (version 1)") {
		t.Fatalf("runner calls=%d args=%v", runner.calls, runner.args)
	}
	if !strings.HasSuffix(joined, "/bin/sh -c echo hi") {
		t.Fatalf("command was not passed to the shell: %v", runner.args)
	}
	if !strings.Contains(joined, "-D WRITABLE_ROOT_0=") {
		t.Fatalf("writable root was not bound: %v", runner.args)
	}
	// The session metadata exposes the profile and its hash, so a host can
	// record what policy governed a run.
	if hash, _ := session.Metadata[profileHashMetadataKey].(string); len(hash) != 64 {
		t.Fatalf("profile hash = %#v", session.Metadata[profileHashMetadataKey])
	}
	if backend, _ := result.Metadata["backend"].(string); backend != "seatbelt" {
		t.Fatalf("result metadata = %#v", result.Metadata)
	}
}

func TestAdapterMapsExitCodesAndErrors(t *testing.T) {
	dir := t.TempDir()
	adapter, runner := newAdapter(t, Config{WritableRoots: []string{dir}, DefaultTimeout: time.Second})
	session := openSession(t, adapter, dir)

	runner.run = func(context.Context, io.Writer, io.Writer) error {
		return exec.Command("/bin/sh", "-c", "exit 7").Run()
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "false"})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error: %v", err)
	}
	if result.ExitCode != 7 || result.Metadata["exitCode"] != 7 {
		t.Fatalf("result = %#v", result)
	}

	runner.run = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "sleep 10", Timeout: 20 * time.Millisecond}); !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("timeout error = %v", err)
	}

	runner.run = func(_ context.Context, _, _ io.Writer) error { return errors.New("runner exploded") }
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "sh"}); !errors.Is(err, sandbox.ErrExecuteFailed) {
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
	smallSession := openSession(t, small, dir)
	if _, err := small.Execute(context.Background(), smallSession, sandbox.ExecuteRequest{Command: "sh"}); !errors.Is(err, sandbox.ErrResponseTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}

	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{}); !errors.Is(err, sandbox.ErrExecuteFailed) {
		t.Fatalf("empty command error = %v", err)
	}
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "sh", CWD: "relative"}); !errors.Is(err, sandbox.ErrExecuteFailed) {
		t.Fatalf("relative cwd error = %v", err)
	}
}

func TestAdapterLifecycleAndAvailability(t *testing.T) {
	dir := t.TempDir()
	adapter, _ := newAdapter(t, Config{WritableRoots: []string{dir}})
	session := openSession(t, adapter, dir)
	if err := adapter.Close(context.Background(), session); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "sh"}); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("closed session error = %v", err)
	}
	if err := adapter.Close(context.Background(), session); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("double close error = %v", err)
	}
	if _, err := adapter.Open(context.Background(), sandbox.OpenRequest{}); !errors.Is(err, sandbox.ErrSessionOpenFailed) {
		t.Fatalf("missing run id error = %v", err)
	}
	if _, err := adapter.Execute(context.Background(), &sandbox.Session{ID: "missing"}, sandbox.ExecuteRequest{Command: "sh"}); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("unknown session error = %v", err)
	}

	// A missing runner is an unavailable sandbox, not a crash.
	missing, err := New(Config{
		WritableRoots: []string{dir},
		LookPath:      func(string) (string, error) { return "", errors.New("not found") },
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := missing.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); !errors.Is(err, sandbox.ErrSandboxUnavailable) {
		t.Fatalf("missing runner error = %v", err)
	}

	// Off macOS the adapter refuses to open a session instead of running
	// without a sandbox.
	if runtime.GOOS != "darwin" {
		offPlatform, err := New(Config{WritableRoots: []string{dir}})
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		if _, err := offPlatform.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); !errors.Is(err, sandbox.ErrSandboxUnavailable) {
			t.Fatalf("off-platform error = %v", err)
		}
	}

	if _, err := New(Config{WritableRoots: []string{"relative"}}); err == nil {
		t.Fatal("relative writable root was accepted")
	}
	if _, err := New(Config{ProtectedNames: []string{"a/b"}}); err == nil {
		t.Fatal("nested protected name was accepted")
	}
}

func TestAdapterAcceptsPerRequestRoots(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	adapter, _ := newAdapter(t, Config{WritableRoots: []string{dir}})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_1", WorkingDir: dir,
		Metadata: map[string]any{
			writableRootsMetaKey: []string{extra},
			readOnlyPathsMetaKey: []string{filepath.Join(dir, "docs")},
			networkMetadataKey:   true,
		},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	params, _ := session.Metadata[paramsMetadataKey].(map[string]string)
	if params["WRITABLE_ROOT_1"] != canonicalPath(extra) {
		t.Fatalf("params = %#v", params)
	}
	if params["READONLY_PATH_0"] != canonicalPath(filepath.Join(dir, "docs")) {
		t.Fatalf("params = %#v", params)
	}
	profile, _ := session.Metadata[profileMetadataKey].(string)
	if !strings.Contains(profile, "(allow network-outbound)") {
		t.Fatalf("per-request network grant missing:\n%s", profile)
	}
	// A request may also revoke network access.
	quiet, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_2", WorkingDir: dir, Metadata: map[string]any{networkMetadataKey: false},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if profile, _ := quiet.Metadata[profileMetadataKey].(string); strings.Contains(profile, "(allow network-outbound)") {
		t.Fatal("network was granted despite an explicit false")
	}
}

// TestSeatbeltActuallyEnforcesTheProfile runs the real sandbox on macOS:
// writes inside the writable root succeed, writes outside it fail, and
// `.git` inside the root stays read-only.
func TestSeatbeltActuallyEnforcesTheProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is macOS only")
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

	// The adapter canonicalizes the working directory (the policy matches
	// the vnodes the kernel checks), so build paths from the session.
	root = session.WorkingDir
	gitDir = filepath.Join(root, ".git")
	inside := filepath.Join(root, "victim.txt")
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: fmt.Sprintf("echo inside > %q && cat %q", inside, inside),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v (stderr=%s)", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("inside write failed: exit=%d stderr=%s", result.ExitCode, result.Stderr)
	}

	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: fmt.Sprintf("echo escaped > %q", outside),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("write outside the writable root succeeded: %#v", result)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the outside file exists despite the sandbox")
	}

	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: fmt.Sprintf("echo tampered > %q", filepath.Join(gitDir, "HEAD")),
	})
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

	// Network is denied by default: a raw TCP connect to a loopback port
	// fails inside the sandbox even though the port is open outside it.
	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: fmt.Sprintf("/usr/bin/nc -z 127.0.0.1 1; echo status=$?"),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if strings.Contains(result.Stdout, "status=0") {
		t.Fatalf("network was reachable inside the sandbox: %#v", result)
	}
}
