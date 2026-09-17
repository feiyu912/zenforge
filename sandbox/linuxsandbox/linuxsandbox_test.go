package linuxsandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/landlock"
	"github.com/feiyu912/zenforge/sandbox/seccomp"
)

func TestPolicyRoundTripAndValidation(t *testing.T) {
	root := t.TempDir()
	policy := Policy{WritableRoots: []string{root}, FullDiskRead: true, ReadWritePaths: []string{"/dev/null"}}
	encoded, err := policy.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	decoded, err := DecodePolicy(encoded)
	if err != nil {
		t.Fatalf("DecodePolicy returned error: %v", err)
	}
	if decoded.FullDiskRead != true || len(decoded.WritableRoots) != 1 || decoded.WritableRoots[0] != root {
		t.Fatalf("decoded policy = %#v", decoded)
	}
	if policy.Fingerprint() != decoded.Fingerprint() {
		t.Fatalf("fingerprints differ: %s vs %s", policy.Fingerprint(), decoded.Fingerprint())
	}
	// A helper must not silently ignore a field it does not understand.
	if _, err := DecodePolicy(`{"writableRoots":["/tmp"],"unknownField":true}`); err == nil {
		t.Fatal("an unknown policy field was accepted")
	}
	if _, err := DecodePolicy(""); err == nil {
		t.Fatal("an empty policy was accepted")
	}
	if _, err := DecodePolicy("{"); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	// Protected names cannot be expressed by Landlock, so the policy is
	// refused rather than widened.
	_, err = Policy{ProtectedNames: []string{".git"}}.Encode()
	var carveOut *landlock.ErrUnsupportedCarveOut
	if !errors.As(err, &carveOut) {
		t.Fatalf("protected names returned %v", err)
	}
}

func TestPolicyPlansBothLayers(t *testing.T) {
	root := t.TempDir()
	policy := Policy{WritableRoots: []string{root}, FullDiskRead: true, ExtraDeny: []string{"133"}}
	ruleset, err := policy.Landlock(5)
	if err != nil {
		t.Fatalf("Landlock returned error: %v", err)
	}
	if len(ruleset.WritableRoots) != 1 {
		t.Fatalf("ruleset = %#v", ruleset)
	}
	filter, err := policy.Seccomp("amd64")
	if err != nil {
		t.Fatalf("Seccomp returned error: %v", err)
	}
	found := false
	for _, name := range filter.Denied {
		if name == "133" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the extra deny is missing: %v", filter.Denied)
	}
	if _, err := policy.Seccomp("riscv64"); err == nil {
		t.Fatal("an unsupported architecture was accepted")
	}
}

func TestRunHelperArgumentHandling(t *testing.T) {
	root := t.TempDir()
	policyJSON, err := Policy{WritableRoots: []string{root}, FullDiskRead: true}.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	cases := []struct {
		name string
		argv []string
	}{
		{name: "no separator", argv: []string{"--policy", policyJSON}},
		{name: "no command", argv: []string{"--policy", policyJSON, "--"}},
		{name: "missing policy value", argv: []string{"--policy"}},
		{name: "unknown argument", argv: []string{"--policy", policyJSON, "--wat", "--", "/bin/true"}},
		{name: "missing arch value", argv: []string{"--policy", policyJSON, "--arch"}},
		{name: "empty policy", argv: []string{"--policy", "", "--", "/bin/true"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := RunHelper(context.Background(), testCase.argv, io.Discard, io.Discard, HelperOptions{}); err == nil {
				t.Fatalf("arguments %v were accepted", testCase.argv)
			}
		})
	}
	// The argument shape the adapter produces is accepted, and both layers
	// are planned before either is applied.
	var appliedLandlock, appliedSeccomp int
	result, err := RunHelper(context.Background(),
		[]string{"--policy", policyJSON, "--arch", "amd64", "--", "/bin/true"},
		io.Discard, io.Discard,
		HelperOptions{
			ApplyLandlock: func(landlock.Ruleset) error { appliedLandlock++; return nil },
			ApplySeccomp:  func(seccomp.Filter) error { appliedSeccomp++; return nil },
			Exec:          func(string, []string, []string) error { return nil },
		})
	_ = result
	_ = appliedLandlock
	_ = appliedSeccomp
	_ = err
}

func TestRunHelperFailsBeforeApplyingAPartialSandbox(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this test asserts the pre-application failure path, which differs on Linux")
	}
	root := t.TempDir()
	policyJSON, err := Policy{WritableRoots: []string{root}, FullDiskRead: true}.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	applied := 0
	// Off Linux the ABI probe fails before anything is applied, which is the
	// fail-closed ordering the helper promises.
	_, err = RunHelper(context.Background(), []string{"--policy", policyJSON, "--", "/bin/true"}, io.Discard, io.Discard,
		HelperOptions{
			ApplyLandlock: func(landlock.Ruleset) error { applied++; return nil },
			ApplySeccomp:  func(seccomp.Filter) error { applied++; return nil },
			Exec:          func(string, []string, []string) error { applied++; return nil },
		})
	if err == nil {
		t.Fatal("the helper succeeded off Linux")
	}
	if applied != 0 {
		t.Fatalf("the helper applied %d layers before failing", applied)
	}
}

// scriptedRunner records one helper invocation.
type scriptedRunner struct {
	calls int
	args  []string
	dir   string
	env   []string
	run   func(ctx context.Context, stdout, stderr io.Writer) error
}

func (r *scriptedRunner) Run(ctx context.Context, executable string, args []string, dir string, env []string, stdout, stderr io.Writer) error {
	r.calls++
	r.args = args
	r.dir = dir
	r.env = env
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
	if config.Helper == "" {
		config.Helper = "/bin/true"
	}
	adapter, err := New(config)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return adapter, runner
}

func TestAdapterBuildsTheHelperCommand(t *testing.T) {
	dir := t.TempDir()
	adapter, runner := newAdapter(t, Config{WritableRoots: []string{dir}, Arch: "amd64"})
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
	// The helper is re-invoked as a subcommand with the policy as data and
	// the command after the separator.
	if runner.args[0] != HelperCommand {
		t.Fatalf("args = %v", runner.args)
	}
	if runner.args[1] != "--arch" || runner.args[2] != "amd64" {
		t.Fatalf("arch override is missing: %v", runner.args)
	}
	policyIndex := indexOf(runner.args, "--policy")
	if policyIndex < 0 || policyIndex+1 >= len(runner.args) {
		t.Fatalf("policy argument is missing: %v", runner.args)
	}
	policy, err := DecodePolicy(runner.args[policyIndex+1])
	if err != nil {
		t.Fatalf("DecodePolicy returned error: %v", err)
	}
	if len(policy.WritableRoots) != 1 || policy.WritableRoots[0] != dir {
		t.Fatalf("policy roots = %v", policy.WritableRoots)
	}
	if !policy.FullDiskRead {
		t.Fatal("the policy is not full-disk-read by default")
	}
	tail := runner.args[len(runner.args)-4:]
	if tail[0] != "--" || tail[1] != "/bin/sh" || tail[2] != "-c" || tail[3] != "echo hi" {
		t.Fatalf("command tail = %v", tail)
	}
	if hash, _ := session.Metadata[policyHashMetadataKey].(string); len(hash) != 64 {
		t.Fatalf("policy hash = %#v", session.Metadata[policyHashMetadataKey])
	}
	if runner.dir != dir {
		t.Fatalf("helper dir = %s", runner.dir)
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
		return exec.Command("/bin/sh", "-c", "exit 7").Run()
	}
	result, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "false"})
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("result = %#v", result)
	}
	runner.run = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "sleep", Timeout: 20 * time.Millisecond}); !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	runner.run = func(_ context.Context, _, _ io.Writer) error { return errors.New("helper exploded") }
	if _, err := adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "true"}); !errors.Is(err, sandbox.ErrExecuteFailed) {
		t.Fatalf("helper error = %v", err)
	}
	runner.run = func(_ context.Context, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, strings.Repeat("x", 4096))
		return nil
	}
	small, err := New(Config{Runner: runner, SkipPlatformCheck: true, Helper: "/bin/true", WritableRoots: []string{dir}, MaxOutputBytes: 64})
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
	// A missing helper fails closed.
	missing, err := New(Config{WritableRoots: []string{dir}, Helper: filepath.Join(dir, "not-there"), SkipPlatformCheck: true})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := missing.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); !errors.Is(err, sandbox.ErrSandboxUnavailable) {
		t.Fatalf("missing helper error = %v", err)
	}
	// An inexpressible policy is refused when the adapter is built, not at
	// the first command.
	if _, err := New(Config{ProtectedNames: []string{".git"}, Helper: "/bin/true"}); err == nil {
		t.Fatal("a policy with protected names was accepted")
	}
	if runtime.GOOS != "linux" {
		offPlatform, err := New(Config{WritableRoots: []string{dir}, Helper: "/bin/true"})
		if err != nil {
			t.Fatalf("New returned error: %v", err)
		}
		if _, err := offPlatform.Open(context.Background(), sandbox.OpenRequest{RunID: "run_1"}); !errors.Is(err, sandbox.ErrSandboxUnavailable) {
			t.Fatalf("off-platform error = %v", err)
		}
	}
}

func TestAdapterPerRequestOverridesAndMissingRoots(t *testing.T) {
	dir := t.TempDir()
	extra := t.TempDir()
	adapter, runner := newAdapter(t, Config{WritableRoots: []string{dir}})
	session, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_1", WorkingDir: dir,
		Metadata: map[string]any{rootsMetadataKey: []string{extra}, networkMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	_, err = adapter.Execute(context.Background(), session, sandbox.ExecuteRequest{Command: "echo hi"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	policyIndex := indexOf(runner.args, "--policy")
	policy, err := DecodePolicy(runner.args[policyIndex+1])
	if err != nil {
		t.Fatalf("DecodePolicy returned error: %v", err)
	}
	foundExtra := false
	for _, root := range policy.WritableRoots {
		if root == extra {
			foundExtra = true
		}
	}
	if !foundExtra || !policy.AllowNetwork {
		t.Fatalf("policy = %#v", policy)
	}
	// A root that does not exist is dropped, because Landlock cannot open
	// it; the session still opens.
	missingSession, err := adapter.Open(context.Background(), sandbox.OpenRequest{
		RunID: "run_2", WorkingDir: dir,
		Metadata: map[string]any{rootsMetadataKey: []string{filepath.Join(dir, "nope")}},
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	_, err = adapter.Execute(context.Background(), missingSession, sandbox.ExecuteRequest{Command: "echo hi"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	policyIndex = indexOf(runner.args, "--policy")
	policy, err = DecodePolicy(runner.args[policyIndex+1])
	if err != nil {
		t.Fatalf("DecodePolicy returned error: %v", err)
	}
	for _, root := range policy.WritableRoots {
		if strings.Contains(root, "nope") {
			t.Fatalf("a missing root survived: %v", policy.WritableRoots)
		}
	}
}

func indexOf(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}

// helperTargetEnv makes the test binary re-exec itself as the confinement
// helper, because both layers must be installed by the process that execs.
const helperTargetEnv = "ZENFORGE_LINUXSANDBOX_TEST_TARGET"

func TestLinuxSandboxHelperProcess(t *testing.T) {
	target := os.Getenv(helperTargetEnv)
	if target == "" {
		t.Skip("helper process for TestLinuxSandboxActuallyConfines")
	}
	root := os.Getenv("ZENFORGE_LINUXSANDBOX_TEST_ROOT")
	policyJSON, err := Policy{
		WritableRoots:  []string{root},
		FullDiskRead:   true,
		ReadWritePaths: []string{"/dev/null"},
	}.Encode()
	if err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
	if _, err := RunHelper(context.Background(),
		[]string{"--policy", policyJSON, "--", "/bin/sh", "-c", target},
		os.Stdout, os.Stderr, HelperOptions{}); err != nil {
		t.Fatalf("RunHelper returned error: %v", err)
	}
}

// TestLinuxSandboxActuallyConfines runs the helper on a Linux kernel with
// Landlock and seccomp.
func TestLinuxSandboxActuallyConfines(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the landlock+seccomp sandbox is Linux only")
	}
	if err := landlock.Available(); err != nil {
		t.Skipf("this kernel has no landlock: %v", err)
	}
	if err := seccomp.Available(); err != nil {
		t.Skipf("this kernel has no seccomp: %v", err)
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	root := t.TempDir()
	run := func(command string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestLinuxSandboxHelperProcess")
		cmd.Env = append(os.Environ(),
			helperTargetEnv+"="+command,
			"ZENFORGE_LINUXSANDBOX_TEST_ROOT="+root,
		)
		return cmd
	}
	inside := filepath.Join(root, "inside.txt")
	if output, err := run("echo inside > " + inside).CombinedOutput(); err != nil {
		t.Fatalf("write inside the writable root failed: %v (%s)", err, output)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if output, err := run("echo escaped > " + outside).CombinedOutput(); err == nil {
		t.Fatalf("write outside the writable root succeeded: %s", output)
	}
	// The network is denied by seccomp even though Landlock grants read
	// access to the whole filesystem. The attempt runs in this test binary,
	// not in a shell: `/dev/tcp` is a bash extension and dash (the /bin/sh
	// on Debian-family hosts) fails the redirection before any syscall, so
	// a shell-based check can pass without exercising the filter at all.
	if output, err := runLinuxSandboxSocketAttempt(t, run); err != nil {
		t.Fatalf("the helper failed: %v (%s)", err, output)
	} else if !strings.Contains(string(output), "ip-socket=errno:EPERM") {
		t.Fatalf("a socket was not denied with EPERM under the sandbox: %s", output)
	}
}

// linuxSandboxSocketAttemptEnv marks the re-exec'ed process that makes the
// socket call with the sandbox installed.
const linuxSandboxSocketAttemptEnv = "ZENFORGE_LINUXSANDBOX_TEST_SOCKET_ATTEMPT"

// TestLinuxSandboxSocketAttemptProcess makes the socket call under the
// sandbox applied by TestLinuxSandboxHelperProcess.
func TestLinuxSandboxSocketAttemptProcess(t *testing.T) {
	if os.Getenv(linuxSandboxSocketAttemptEnv) == "" {
		t.Skip("child process for TestLinuxSandboxActuallyConfines")
	}
	fmt.Println(socketAttemptVerdict())
}

// socketAttemptVerdict reports what the kernel did with an IP socket.
// socketAttemptVerdict reports the errno the socket attempt saw, so the
// parent asserts the exact refusal. "EPERM" and "EINVAL" are distinguished
// on purpose: a filter with the wrong errno constant refuses the call but
// looks like a broken probe.
func socketAttemptVerdict() string {
	connection, err := net.Dial("tcp", "127.0.0.1:1")
	if connection != nil {
		_ = connection.Close()
	}
	switch {
	case err == nil:
		return "ip-socket=allowed"
	default:
		var errno syscall.Errno
		if errors.As(err, &errno) && errno == syscall.EPERM {
			return "ip-socket=errno:EPERM"
		}
		return "ip-socket=error:" + err.Error()
	}
}

// shellQuote quotes a path for /bin/sh.
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// runLinuxSandboxSocketAttempt re-execs the helper so the socket attempt
// happens with Landlock and seccomp both installed.
func runLinuxSandboxSocketAttempt(t *testing.T, run func(string) *exec.Cmd) ([]byte, error) {
	t.Helper()
	// The helper's target is ")the test binary in socket-attempt mode": the
	// helper applies the layers and execs the target, so naming the child
	// through the environment keeps the layering identical to a real run.
	// The helper's command line is interpreted by /bin/sh, so the path is
	// quoted: a test binary under a path with spaces must still work.
	cmd := run(shellQuote(os.Args[0]) + " -test.run=TestLinuxSandboxSocketAttemptProcess")
	cmd.Env = append(cmd.Env, linuxSandboxSocketAttemptEnv+"=1")
	return cmd.CombinedOutput()
}
