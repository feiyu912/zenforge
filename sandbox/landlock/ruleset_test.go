package landlock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAccessMasksFollowTheABI(t *testing.T) {
	abi1 := AllAccessAt(1)
	if abi1&AccessRefer != 0 || abi1&AccessTruncate != 0 || abi1&AccessIoctlDev != 0 {
		t.Fatalf("ABI 1 must not include newer rights: %b", abi1)
	}
	if abi1&WriteAccess != WriteAccess {
		t.Fatalf("ABI 1 is missing the base write rights: %b", abi1)
	}
	if AllAccessAt(2)&AccessRefer == 0 {
		t.Fatal("ABI 2 must include refer")
	}
	if AllAccessAt(2)&AccessTruncate != 0 {
		t.Fatal("ABI 2 must not include truncate")
	}
	if AllAccessAt(3)&AccessTruncate == 0 {
		t.Fatal("ABI 3 must include truncate")
	}
	if AllAccessAt(4)&AccessIoctlDev != 0 {
		t.Fatal("ABI 4 must not include ioctl_dev")
	}
	if AllAccessAt(5)&AccessIoctlDev == 0 {
		t.Fatal("ABI 5 must include ioctl_dev")
	}
	// A kernel newer than this package is clamped, never asked for rights it
	// cannot name.
	if AllAccessAt(99) != AllAccessAt(MaxSupportedABI) {
		t.Fatal("a newer ABI must be clamped to the newest known ABI")
	}
	if ReadAccessAt(5) != ReadAccess {
		t.Fatalf("read mask = %b", ReadAccessAt(5))
	}
}

func TestBuildPlansReadOnlyFilesystemWithWritableRoots(t *testing.T) {
	root := t.TempDir()
	ruleset, err := Build(Policy{WritableRoots: []string{root}, FullDiskRead: true}, 5)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if ruleset.ABI != 5 || ruleset.HandledAccess != AllAccessAt(5) {
		t.Fatalf("ruleset = %#v", ruleset)
	}
	// The whole filesystem is readable but not writable, and the writable
	// root is granted everything.
	byPath := map[string]Access{}
	for _, rule := range ruleset.Rules {
		byPath[rule.Path] = rule.Access
	}
	if byPath["/"] != ReadAccess {
		t.Fatalf("root rule = %b", byPath["/"])
	}
	if byPath[filepath.Clean(root)] != AllAccessAt(5) {
		t.Fatalf("writable root rule = %b", byPath[filepath.Clean(root)])
	}
	if len(ruleset.WritableRoots) != 1 || ruleset.WritableRoots[0] != filepath.Clean(root) {
		t.Fatalf("writable roots = %v", ruleset.WritableRoots)
	}
	// The plan is deterministic, so it can be hashed and compared.
	again, err := Build(Policy{WritableRoots: []string{root}, FullDiskRead: true}, 5)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if ruleset.Fingerprint() != again.Fingerprint() {
		t.Fatalf("fingerprints differ: %s vs %s", ruleset.Fingerprint(), again.Fingerprint())
	}
	if !strings.Contains(ruleset.Fingerprint(), "abi=5") {
		t.Fatalf("fingerprint = %s", ruleset.Fingerprint())
	}
}

func TestBuildRestrictedPolicyGrantsOnlyTheReadRoots(t *testing.T) {
	read := t.TempDir()
	write := t.TempDir()
	ruleset, err := Build(Policy{ReadableRoots: []string{read}, WritableRoots: []string{write}}, 3)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	paths := map[string]Access{}
	for _, rule := range ruleset.Rules {
		paths[rule.Path] = rule.Access
	}
	if _, ok := paths["/"]; ok {
		t.Fatal("a restricted policy must not grant read access to /")
	}
	if paths[filepath.Clean(read)] != ReadAccess {
		t.Fatalf("read root rule = %b", paths[filepath.Clean(read)])
	}
	if paths[filepath.Clean(write)] != AllAccessAt(3) {
		t.Fatalf("writable root rule = %b", paths[filepath.Clean(write)])
	}
	// Deduplication and sorting keep the plan stable.
	dup, err := Build(Policy{ReadableRoots: []string{read, read}, WritableRoots: []string{write, write}}, 3)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if dup.Fingerprint() != ruleset.Fingerprint() {
		t.Fatalf("duplicates changed the plan: %s vs %s", dup.Fingerprint(), ruleset.Fingerprint())
	}
}

func TestFileAccessExcludesDirectoryRights(t *testing.T) {
	// The kernel rejects a file rule that carries directory rights, so the
	// file mask must never contain them.
	for _, abi := range []int{1, 2, 3, 4, 5, 99} {
		access := FileAccessAt(abi)
		if access&(AccessReadDir|AccessRemoveDir|AccessRemoveFile|AccessMakeChar|AccessMakeDir|AccessMakeReg|AccessMakeSock|AccessMakeFifo|AccessMakeBlock|AccessMakeSym|AccessRefer) != 0 {
			t.Fatalf("FileAccessAt(%d) = %d carries directory rights", abi, access)
		}
		if access&(AccessReadFile|AccessWriteFile|AccessExecute) == 0 {
			t.Fatalf("FileAccessAt(%d) = %d is missing the basic file rights", abi, access)
		}
	}
	if FileAccessAt(2)&AccessTruncate != 0 || FileAccessAt(3)&AccessTruncate == 0 {
		t.Fatal("truncate must follow ABI 3")
	}
	if FileAccessAt(4)&AccessIoctlDev != 0 || FileAccessAt(5)&AccessIoctlDev == 0 {
		t.Fatal("ioctl-dev must follow ABI 5")
	}
}

func TestBuildGrantsTheSafeDeviceByDefault(t *testing.T) {
	// A plan that did not ask for /dev/null still gets it, because a sandbox
	// that cannot write there breaks `cmd >/dev/null`.
	root := t.TempDir()
	ruleset, err := Build(Policy{WritableRoots: []string{root}, FullDiskRead: true}, 5)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	found := false
	for _, rule := range ruleset.Rules {
		if rule.Path == DefaultDevicePath {
			found = true
			if rule.Access != FileAccessAt(5) {
				t.Fatalf("/dev/null access = %d, want the file mask", rule.Access)
			}
		}
	}
	if !found {
		t.Fatalf("the default device was not granted: %#v", ruleset.Rules)
	}
}

func TestBuildGrantsReadWritePathsAndDevices(t *testing.T) {
	root := t.TempDir()
	ruleset, err := Build(Policy{WritableRoots: []string{root}, ReadWritePaths: []string{"/dev/null"}, FullDiskRead: true}, 5)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	found := false
	for _, rule := range ruleset.Rules {
		if rule.Path == "/dev/null" && rule.Access == FileAccessAt(5) {
			found = true
		}
	}
	if !found {
		t.Fatalf("/dev/null was not granted read-write access: %#v", ruleset.Rules)
	}
}

func TestBuildRejectsWhatLandlockCannotExpress(t *testing.T) {
	root := t.TempDir()
	// Protected names are a read-only carve-out inside a writable root;
	// Landlock unions every matching rule and has no deny rule, so granting
	// them would silently widen the policy. Refuse instead.
	if _, err := Build(Policy{WritableRoots: []string{root}, ProtectedNames: []string{".git"}}, 5); err == nil {
		t.Fatal("a protected-name policy was accepted")
	} else {
		var unsupported *ErrUnsupportedCarveOut
		if !errors.As(err, &unsupported) {
			t.Fatalf("error = %v, want ErrUnsupportedCarveOut", err)
		}
		if !strings.Contains(err.Error(), "no deny rule") {
			t.Fatalf("error should explain why: %v", err)
		}
	}
	if _, err := Build(Policy{WritableRoots: []string{"relative"}}, 5); err == nil {
		t.Fatal("a relative writable root was accepted")
	}
	if _, err := Build(Policy{WritableRoots: []string{root}}, 0); err == nil {
		t.Fatal("ABI 0 was accepted")
	}
	if _, err := Build(Policy{ReadWritePaths: []string{"dev/null"}}, 5); err == nil {
		t.Fatal("a relative read-write path was accepted")
	}
}

func TestAvailableIsPlatformDependent(t *testing.T) {
	err := Available()
	if runtime.GOOS != "linux" {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux Available returned %v", err)
		}
		if _, err := ABIVersion(); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux ABIVersion returned %v", err)
		}
		if err := Restrict(Ruleset{Rules: []Rule{{Path: "/", Access: ReadAccess}}}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux Restrict returned %v", err)
		}
		if err := Exec(Ruleset{}, "/bin/true", []string{"/bin/true"}, nil); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("off Linux Exec returned %v", err)
		}
		return
	}
	if err != nil {
		t.Skipf("this kernel has no landlock: %v", err)
	}
	if version, err := ABIVersion(); err != nil || version < 1 {
		t.Fatalf("ABIVersion = %d, %v", version, err)
	}
	if err := Restrict(Ruleset{}); err == nil {
		t.Fatal("an empty ruleset was accepted")
	}
}

// landlockHelperEnv makes the test binary exec itself as the sandboxed
// command, because Exec replaces the process and Landlock cannot be
// installed from outside.
const landlockHelperEnv = "ZENFORGE_LANDLOCK_TEST_TARGET"

func TestLandlockHelperProcess(t *testing.T) {
	target := os.Getenv(landlockHelperEnv)
	if target == "" {
		t.Skip("helper process for TestLandlockEnforcesReadOnlyOutsideWritableRoots")
	}
	// Reached only in the re-exec'ed process: apply the ruleset handed over
	// in the environment and exec the real command.
	root := os.Getenv("ZENFORGE_LANDLOCK_TEST_ROOT")
	ruleset, err := Build(Policy{WritableRoots: []string{root}, FullDiskRead: true}, 5)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if err := Exec(ruleset, "/bin/sh", []string{"/bin/sh", "-c", target}, os.Environ()); err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
}

// TestLandlockEnforcesReadOnlyOutsideWritableRoots runs the helper on a
// kernel that supports Landlock.
func TestLandlockEnforcesReadOnlyOutsideWritableRoots(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("landlock is Linux only")
	}
	if err := Available(); err != nil {
		t.Skipf("this kernel has no landlock: %v", err)
	}
	if testing.Short() {
		t.Skip("short mode")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")

	run := func(command string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestLandlockHelperProcess")
		cmd.Env = append(os.Environ(),
			landlockHelperEnv+"="+command,
			"ZENFORGE_LANDLOCK_TEST_ROOT="+root,
		)
		return cmd
	}
	inside := filepath.Join(root, "inside.txt")
	// Inside the writable root: allowed.
	if output, err := run("echo inside > " + inside).CombinedOutput(); err != nil {
		t.Fatalf("write inside the writable root failed: %v (%s)", err, output)
	}
	if content, err := os.ReadFile(inside); err != nil || string(content) != "inside\n" {
		t.Fatalf("file = %q err=%v", content, err)
	}
	// Outside: denied, and the file must not exist.
	if output, err := run("echo escaped > " + outside).CombinedOutput(); err == nil {
		t.Fatalf("write outside the writable root succeeded: %s", output)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the outside file exists despite landlock")
	}
	// Reading the host filesystem still works (the reference grants
	// whole-filesystem read access on purpose).
	if output, err := run("cat /etc/hostname >/dev/null && echo readable").CombinedOutput(); err != nil || !strings.Contains(string(output), "readable") {
		t.Fatalf("read outside the writable root failed: %v (%s)", err, output)
	}
}
