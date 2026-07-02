package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

type call struct {
	executable string
	args       []string
}

type fakeRunner struct {
	mu      sync.Mutex
	calls   []call
	run     func(context.Context, []string, io.Writer, io.Writer) error
	inspect func(context.Context, []string, io.Writer, io.Writer) error
}

func (f *fakeRunner) Run(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, call{executable: executable, args: slices.Clone(args)})
	f.mu.Unlock()
	if args[0] == "inspect" {
		if f.inspect != nil {
			return f.inspect(ctx, args, stdout, stderr)
		}
		_, _ = io.WriteString(stderr, "Error: No such object")
		return &exec.ExitError{}
	}
	if f.run != nil {
		return f.run(ctx, args, stdout, stderr)
	}
	return nil
}

func TestLifecycleAndArguments(t *testing.T) {
	runner := &fakeRunner{}
	adapter, err := New(Config{DockerCLI: "/custom/docker", Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run 1", EnvironmentID: "test:image", WorkingDir: t.TempDir(),
		Env:    map[string]string{"ZED": "2", "ALPHA": "1"},
		Mounts: []sandbox.Mount{{Source: t.TempDir(), Destination: "/src"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.WorkingDir != defaultWorkingDir {
		t.Fatalf("working dir = %q", session.WorkingDir)
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: "printf ok", CWD: "/src", Env: map[string]string{"B": "2", "A": "1"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execute = %#v, %v", result, err)
	}
	if err := adapter.Close(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("calls = %d, want 5", len(runner.calls))
	}
	if runner.calls[1].executable != "/custom/docker" {
		t.Fatalf("executable = %q", runner.calls[1].executable)
	}
	create := strings.Join(runner.calls[1].args, "\x00")
	for _, expected := range []string{"create", "--network\x00none", "--cap-drop\x00ALL", "--read-only",
		"--env\x00ALPHA=1\x00--env\x00ZED=2", "test:image"} {
		if !strings.Contains(create, expected) {
			t.Errorf("create args missing %q: %v", expected, runner.calls[1].args)
		}
	}
	execute := strings.Join(runner.calls[3].args, "\x00")
	if !strings.Contains(execute, "exec\x00--workdir\x00/src\x00--env\x00A=1\x00--env\x00B=2") {
		t.Errorf("exec args = %v", runner.calls[3].args)
	}
	if got := runner.calls[4].args; !slices.Equal(got[:2], []string{"rm", "-f"}) {
		t.Errorf("close args = %v", got)
	}
	if err := adapter.Close(context.Background(), session); !errors.Is(err, sandbox.ErrClosed) {
		t.Fatalf("second close error = %v", err)
	}
}

func TestMountMapsHostWorkingDirectory(t *testing.T) {
	runner := &fakeRunner{}
	adapter, _ := New(Config{Runner: runner})
	root := t.TempDir()
	child := root + "/child"
	if err := exec.Command("mkdir", child).Run(); err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "r", WorkingDir: child,
		Mounts: []sandbox.Mount{{Source: root, Destination: "/workspace", Mode: "rw"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.WorkingDir != "/workspace/child" {
		t.Fatalf("working dir = %q", session.WorkingDir)
	}
}

func TestExecuteMapsShellHostCWDFromSessionMounts(t *testing.T) {
	runner := &fakeRunner{}
	adapter, _ := New(Config{Runner: runner})
	root := t.TempDir()
	child := root + "/child"
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	requestMetadata := map[string]any{"toolCallId": "open-call"}
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run", WorkingDir: root, Metadata: requestMetadata,
		Mounts: []sandbox.Mount{{Source: root, Destination: "/workspace", Mode: "rw"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requestMetadata[mountsMetadataKey] != nil || session.Metadata["toolCallId"] != "open-call" {
		t.Fatalf("metadata was overwritten: request=%#v session=%#v", requestMetadata, session.Metadata)
	}
	_, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: "pwd", CWD: child,
	})
	if err != nil {
		t.Fatal(err)
	}
	execArgs := runner.calls[len(runner.calls)-1].args
	if got := execArgs[2]; got != "/workspace/child" {
		t.Fatalf("docker exec cwd = %q, args=%v", got, execArgs)
	}

	restoredMetadata := map[string]any{
		mountsMetadataKey: []any{map[string]any{
			"source": root, "destination": "/workspace", "mode": "rw",
		}},
	}
	restored := &sandbox.Session{
		ID: session.ID, RunID: session.RunID, WorkingDir: session.WorkingDir,
		Metadata: restoredMetadata,
	}
	delete(adapter.sessions, session.ID)
	runner.inspect = func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, `{"running":true,"labels":{"zenforge.run_id":"run","zenforge.subtask_id":""}}`)
		return nil
	}
	_, err = adapter.Execute(context.Background(), restored, sandbox.ExecuteRequest{
		Command: "pwd", CWD: "/Users/not-mounted/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	execArgs = runner.calls[len(runner.calls)-1].args
	if got := execArgs[2]; got != "/workspace" {
		t.Fatalf("unmapped host cwd = %q, want session fallback", got)
	}
}

func TestOpenValidationAndUnavailableMapping(t *testing.T) {
	adapter, _ := New(Config{Runner: &fakeRunner{}})
	_, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "r", Env: map[string]string{"BAD-NAME": "x"}})
	if sandbox.Code(err) != sandbox.ErrSessionOpenFailed {
		t.Fatalf("code = %q, err = %v", sandbox.Code(err), err)
	}
	runner := &fakeRunner{run: func(context.Context, []string, io.Writer, io.Writer) error {
		return exec.ErrNotFound
	}}
	adapter, _ = New(Config{Runner: runner})
	_, err = adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "r"})
	if sandbox.Code(err) != sandbox.ErrSandboxUnavailable {
		t.Fatalf("code = %q, err = %v", sandbox.Code(err), err)
	}
}

func TestExecuteTimeoutAndOutputLimit(t *testing.T) {
	runner := &fakeRunner{run: func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		if args[0] != "exec" {
			return nil
		}
		if strings.Contains(args[len(args)-1], "large") {
			_, _ = io.WriteString(stdout, "123456")
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	adapter, _ := New(Config{Runner: runner, MaxOutputBytes: 5})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "large"})
	if sandbox.Code(err) != sandbox.ErrResponseTooLarge || result.Stdout != "12345" {
		t.Fatalf("large result = %#v, err = %v", result, err)
	}
	result, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: "sleep", Timeout: time.Millisecond,
	})
	if sandbox.Code(err) != sandbox.ErrTimeout || result.ExitCode != 124 {
		t.Fatalf("timeout result = %#v, err = %v", result, err)
	}
}

func TestNonZeroCommandIsAResult(t *testing.T) {
	runner := &fakeRunner{run: func(_ context.Context, args []string, _, stderr io.Writer) error {
		if args[0] == "exec" {
			_, _ = io.WriteString(stderr, "failed")
			return &exec.ExitError{}
		}
		return nil
	}}
	adapter, _ := New(Config{Runner: runner})
	session, _ := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "r"})
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "false"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != -1 || result.Stderr != "failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRestoredSessionIsValidatedAndReused(t *testing.T) {
	session := &sandbox.Session{
		ID: "zenforge-restored", RunID: "run", SubtaskID: "sub",
		WorkingDir: "/workspace",
	}
	runner := &fakeRunner{inspect: func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, `{"running":true,"labels":{"zenforge.run_id":"run","zenforge.subtask_id":"sub"}}`)
		return nil
	}}
	adapter, _ := New(Config{Runner: runner})
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "true"})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execute restored session = %#v, %v", result, err)
	}
	if len(runner.calls) != 2 || runner.calls[0].args[0] != "inspect" || runner.calls[1].args[0] != "exec" {
		t.Fatalf("calls = %#v", runner.calls)
	}

	badRunner := &fakeRunner{inspect: func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, `{"running":true,"labels":{"zenforge.run_id":"other","zenforge.subtask_id":"sub"}}`)
		return nil
	}}
	badAdapter, _ := New(Config{Runner: badRunner})
	_, err = badAdapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "true"})
	if sandbox.Code(err) != sandbox.ErrSandboxUnavailable {
		t.Fatalf("mismatched labels code = %q, err = %v", sandbox.Code(err), err)
	}
}

func TestOpenRemovesMatchingLeftoverContainer(t *testing.T) {
	runner := &fakeRunner{inspect: func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, `{"running":false,"labels":{"zenforge.run_id":"run","zenforge.subtask_id":""}}`)
		return nil
	}}
	adapter, _ := New(Config{Runner: runner})
	if _, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		got = append(got, call.args[0])
	}
	if !slices.Equal(got, []string{"inspect", "rm", "create", "start"}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestOpenRejectsMatchingActiveContainer(t *testing.T) {
	runner := &fakeRunner{inspect: func(_ context.Context, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, `{"running":true,"labels":{"zenforge.run_id":"run","zenforge.subtask_id":""}}`)
		return nil
	}}
	adapter, _ := New(Config{Runner: runner})
	_, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "run"})
	if sandbox.Code(err) != sandbox.ErrSessionOpenFailed {
		t.Fatalf("code = %q, err = %v", sandbox.Code(err), err)
	}
	if len(runner.calls) != 1 || runner.calls[0].args[0] != "inspect" {
		t.Fatalf("active container was modified: %#v", runner.calls)
	}
}

func TestLifecycleMapsContextStateWhenRunnerReturnsKilled(t *testing.T) {
	killed := errors.New("signal: killed")
	runner := &fakeRunner{run: func(ctx context.Context, args []string, _, _ io.Writer) error {
		if args[0] == "create" {
			<-ctx.Done()
			return killed
		}
		return nil
	}}
	adapter, _ := New(Config{Runner: runner, DefaultTimeout: time.Millisecond})
	_, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "timeout"})
	if sandbox.Code(err) != sandbox.ErrTimeout {
		t.Fatalf("open code = %q, err = %v", sandbox.Code(err), err)
	}

	runner = &fakeRunner{}
	adapter, _ = New(Config{Runner: runner})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{RunID: "close"})
	if err != nil {
		t.Fatal(err)
	}
	runner.run = func(ctx context.Context, args []string, _, _ io.Writer) error {
		if args[0] == "rm" {
			<-ctx.Done()
			return killed
		}
		return nil
	}
	closeCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err = adapter.Close(closeCtx, session)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("close error = %v", err)
	}
}

func TestDockerIntegration(t *testing.T) {
	if os.Getenv("ZENFORGE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ZENFORGE_DOCKER_INTEGRATION=1 to run against Docker")
	}
	adapter, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "integration-" + time.Now().Format("150405.000000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := adapter.Close(context.Background(), session); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{
		Command: "printf zenforge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "zenforge" {
		t.Fatalf("result = %#v", result)
	}
}
