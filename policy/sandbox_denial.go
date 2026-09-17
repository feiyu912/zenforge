package policy

import "strings"

// Sandbox denial detection follows the codex is_likely_sandbox_denied
// heuristic: a non-zero exit whose status or output dialect suggests the
// confinement — not the command logic — caused the failure. The
// classification is deliberately conservative and advisory: it only adds
// an explanatory marker and escalation hint to the tool result, and any
// actual escape from confinement still requires explicit user approval.
//
// Signal sources, in order:
//   - exit 128+SIGSYS (159): a seccomp kill is always confinement.
//   - shell-level exits 2 (misuse), 126 (not executable), 127 (not
//     found): almost never confinement, rejected before keyword scanning.
//   - failure-message dialects emitted by sandbox executors and the
//     kernels they delegate to: "operation not permitted" (seatbelt),
//     "permission denied" (landlock/ACL), "read-only file system"
//     (bwrap read-only binds), plus explicit subsystem names.
const sigsysExitCode = 159

var sandboxDenialKeywords = []string{
	"operation not permitted",
	"permission denied",
	"read-only file system",
	"failed to write file",
	"seccomp",
	"sandbox",
	"landlock",
}

// IsLikelySandboxDenied reports whether a failed command's exit code and
// combined output look like a sandbox denial. exitCode 0 is never a
// denial.
func IsLikelySandboxDenied(exitCode int, output string) bool {
	if exitCode == 0 {
		return false
	}
	switch exitCode {
	case 2, 126, 127:
		// Shell misuse, non-executable file, or command not found:
		// ordinary failures that escalation cannot fix.
		return false
	case sigsysExitCode:
		return true
	}
	lowered := strings.ToLower(output)
	for _, keyword := range sandboxDenialKeywords {
		if strings.Contains(lowered, keyword) {
			return true
		}
	}
	return false
}
