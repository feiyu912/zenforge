// Package linuxsandbox combines the two in-process Linux confinement
// layers — Landlock (filesystem) and seccomp (network and dangerous
// syscalls) — into a sandbox.Sandbox backend. Both layers must be
// installed by the process that execs the confined command, so the backend
// runs a helper process: the zenforge binary re-invokes itself as
// `linux-sandbox`, applies the ruleset and the filter, and execs the
// command. That indirection is not incidental — it is the only correct
// shape, because a restriction installed in the agent process would
// confine the agent, not the command.
//
// The policy is planned as data on every platform, so the security-relevant
// arithmetic runs under test even where it cannot be applied.
package linuxsandbox

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/sandbox/landlock"
	"github.com/feiyu912/zenforge/sandbox/seccomp"
)

// HelperCommand is the subcommand the backend re-invokes itself with.
const HelperCommand = "linux-sandbox"

// Policy is the confinement applied to one helper invocation. It is passed
// to the helper as JSON, so a policy can be logged, diffed, and tested
// without the executable that will run it.
type Policy struct {
	// WritableRoots are the only paths the command may modify.
	WritableRoots []string `json:"writableRoots,omitempty"`
	// ReadableRoots are the roots granted read access when FullDiskRead is
	// false.
	ReadableRoots []string `json:"readableRoots,omitempty"`
	// FullDiskRead grants read access to the whole filesystem, which is
	// what a coding agent needs.
	FullDiskRead bool `json:"fullDiskRead,omitempty"`
	// ReadWritePaths are individual paths granted read-write access, such
	// as /dev/null.
	ReadWritePaths []string `json:"readWritePaths,omitempty"`
	// AllowNetwork keeps the network syscalls available.
	AllowNetwork bool `json:"allowNetwork,omitempty"`
	// ExtraDeny names additional syscalls for the seccomp filter.
	ExtraDeny []string `json:"extraDeny,omitempty"`
	// ProtectedNames requests per-path read-only carve-outs inside writable
	// roots, which Landlock cannot express. Requesting one is an error
	// rather than a silent widening of the policy.
	ProtectedNames []string `json:"protectedNames,omitempty"`
}

// Validate rejects a policy that cannot be applied faithfully.
func (p Policy) Validate() error {
	if len(p.ProtectedNames) > 0 {
		return &landlock.ErrUnsupportedCarveOut{
			Feature: "protected names inside writable roots",
			Detail:  "use the bubblewrap or Seatbelt backend for a policy with read-only carve-outs",
		}
	}
	if _, err := p.Landlock(landlock.MaxSupportedABI); err != nil {
		return err
	}
	return nil
}

// Encode serializes the policy for the helper.
func (p Policy) Encode() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode linux sandbox policy: %w", err)
	}
	return string(encoded), nil
}

// DecodePolicy parses and validates a policy from the helper's arguments.
func DecodePolicy(encoded string) (Policy, error) {
	var policy Policy
	if strings.TrimSpace(encoded) == "" {
		return policy, fmt.Errorf("linux sandbox policy is empty")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	// A helper must not be steered by fields it does not understand: an
	// unknown field is a policy the caller believes is in force.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("decode linux sandbox policy: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return policy, err
	}
	return policy, nil
}

// Landlock plans the filesystem ruleset for the policy.
func (p Policy) Landlock(abi int) (landlock.Ruleset, error) {
	return landlock.Build(landlock.Policy{
		WritableRoots:  p.WritableRoots,
		ReadableRoots:  p.ReadableRoots,
		ReadWritePaths: p.ReadWritePaths,
		FullDiskRead:   p.FullDiskRead,
		ProtectedNames: p.ProtectedNames,
	}, abi)
}

// Seccomp plans the syscall filter for the policy and architecture.
func (p Policy) Seccomp(arch string) (seccomp.Filter, error) {
	return seccomp.Build(seccomp.Policy{
		AllowNetwork: p.AllowNetwork,
		ExtraDeny:    p.ExtraDeny,
	}, arch)
}

// Fingerprint is a stable description of the policy, used to tie a run to
// the policy that governed it.
func (p Policy) Fingerprint() string {
	parts := []string{fmt.Sprintf("fullDiskRead=%t", p.FullDiskRead), fmt.Sprintf("allowNetwork=%t", p.AllowNetwork)}
	lists := [][]string{p.WritableRoots, p.ReadableRoots, p.ReadWritePaths, p.ExtraDeny}
	labels := []string{"write", "read", "rw", "deny"}
	for index, list := range lists {
		sorted := append([]string(nil), list...)
		sort.Strings(sorted)
		for _, entry := range sorted {
			parts = append(parts, labels[index]+":"+filepath.Clean(entry))
		}
	}
	return strings.Join(parts, "|")
}
