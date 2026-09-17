package policy

import "testing"

func TestIsLikelySandboxDenied(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		output   string
		want     bool
	}{
		{"success is never a denial", 0, "permission denied", false},
		{"operation not permitted", 1, "mkdir: /x: Operation not permitted", true},
		{"permission denied", 1, "bash: /x: Permission denied", true},
		{"read-only file system", 1, "touch: cannot touch '/x': Read-only file system", true},
		{"seccomp subsystem", 1, "seccomp filter blocked the syscall", true},
		{"landlock subsystem", 1, "landlock: access denied", true},
		{"sandbox keyword", 1, "denied by sandbox policy", true},
		{"sigsys kill", 159, "", true},
		{"shell misuse quick-reject", 2, "permission denied", false},
		{"not executable quick-reject", 126, "permission denied", false},
		{"not found quick-reject", 127, "sandbox: command not found", false},
		{"ordinary failure", 1, "grep: foo: No such file or directory", false},
		{"timeout", 124, "timed out waiting for process", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLikelySandboxDenied(tc.exitCode, tc.output); got != tc.want {
				t.Fatalf("IsLikelySandboxDenied(%d, %q) = %v, want %v", tc.exitCode, tc.output, got, tc.want)
			}
		})
	}
}
