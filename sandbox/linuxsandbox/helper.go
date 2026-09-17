package linuxsandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/feiyu912/zenforge/sandbox/landlock"
	"github.com/feiyu912/zenforge/sandbox/seccomp"
)

// HelperResult reports how a helper invocation ended. It is used by tests
// and by the CLI's exit-code mapping; the helper itself normally never
// returns, because Exec replaces the process.
type HelperResult struct {
	// Executed is true when the command was exec'ed, which means the
	// helper process no longer exists and the result was produced by a
	// simulation rather than by the real system calls.
	Executed bool
	// Policy is the decoded policy.
	Policy Policy
}

// HelperOptions lets a caller replace the two confinement steps and the
// exec, which is how the helper's argument handling is tested on hosts
// where the system calls do not exist.
type HelperOptions struct {
	// ApplyLandlock replaces landlock.Restrict.
	ApplyLandlock func(landlock.Ruleset) error
	// ApplySeccomp replaces seccomp.Apply.
	ApplySeccomp func(seccomp.Filter) error
	// Exec replaces the final exec.
	Exec func(path string, argv []string, env []string) error
	// Arch overrides the architecture the seccomp filter is planned for.
	Arch string
}

// RunHelper parses the helper arguments, plans both confinement layers, and
// hands off to exec. argv is everything after the subcommand name.
//
// The accepted shape is:
//
//	--policy <json> [--no-network] -- <command> [args...]
//
// The command runs in the helper's working directory, which the adapter
// sets, so the policy needs no cwd field.
func RunHelper(ctx context.Context, argv []string, stdout, stderr io.Writer, options HelperOptions) (HelperResult, error) {
	var result HelperResult
	policyJSON, helperArch, command, err := parseHelperArgs(argv)
	if err != nil {
		return result, err
	}
	policy, err := DecodePolicy(policyJSON)
	if err != nil {
		return result, err
	}
	result.Policy = policy
	if err := ctx.Err(); err != nil {
		return result, err
	}

	// Plan both layers before applying either: a policy that cannot be
	// expressed must fail before the process is half restricted, because a
	// partially applied sandbox is neither the requested one nor none.
	abi, err := landlock.ABIVersion()
	if err != nil {
		return result, fmt.Errorf("linux sandbox needs landlock: %w", err)
	}
	ruleset, err := policy.Landlock(abi)
	if err != nil {
		return result, err
	}
	arch := helperArch
	if arch == "" {
		arch = options.Arch
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	filter, err := policy.Seccomp(arch)
	if err != nil {
		return result, err
	}

	applyLandlock := options.ApplyLandlock
	if applyLandlock == nil {
		applyLandlock = landlock.Restrict
	}
	applySeccomp := options.ApplySeccomp
	if applySeccomp == nil {
		applySeccomp = seccomp.Apply
	}
	exec := options.Exec
	if exec == nil {
		exec = defaultExec
	}
	if err := applyLandlock(ruleset); err != nil {
		return result, fmt.Errorf("apply landlock ruleset: %w", err)
	}
	if err := applySeccomp(filter); err != nil {
		return result, fmt.Errorf("apply seccomp filter: %w", err)
	}
	if err := exec(command[0], command, os.Environ()); err != nil {
		return result, fmt.Errorf("exec %s: %w", command[0], err)
	}
	result.Executed = true
	return result, nil
}

// parseHelperArgs interprets the helper's own arguments.
func parseHelperArgs(argv []string) (string, string, []string, error) {
	var policyJSON, arch string
	separator := -1
	for index := 0; index < len(argv); index++ {
		argument := argv[index]
		switch {
		case argument == "--":
			separator = index
		case argument == "--policy":
			if index+1 >= len(argv) {
				return "", "", nil, fmt.Errorf("--policy requires a value")
			}
			policyJSON = argv[index+1]
			index++
		case strings.HasPrefix(argument, "--policy="):
			policyJSON = strings.TrimPrefix(argument, "--policy=")
		case argument == "--arch":
			if index+1 >= len(argv) {
				return "", "", nil, fmt.Errorf("--arch requires a value")
			}
			arch = argv[index+1]
			index++
		case strings.HasPrefix(argument, "--arch="):
			arch = strings.TrimPrefix(argument, "--arch=")
		default:
			return "", "", nil, fmt.Errorf("unknown linux sandbox argument %q", argument)
		}
		if separator >= 0 {
			break
		}
	}
	if separator < 0 {
		return "", "", nil, fmt.Errorf("the helper requires a -- separator before the command")
	}
	command := argv[separator+1:]
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return "", "", nil, fmt.Errorf("the helper requires a command after --")
	}
	return policyJSON, arch, command, nil
}
