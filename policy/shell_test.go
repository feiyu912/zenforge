package policy

import (
	"errors"
	"testing"
)

func TestReviewCommandAllowDenyAndApproval(t *testing.T) {
	policy := ShellPolicy{
		AllowCommands:   []string{"go test ./..."},
		DenyCommands:    []string{"rm"},
		RequireApproval: true,
	}
	if got := ReviewCommand(policy, "go test ./...").Decision; got != ReviewAllow {
		t.Fatalf("allow decision = %s", got)
	}
	if got := ReviewCommand(policy, "rm -rf tmp").Decision; got != ReviewBlock {
		t.Fatalf("deny decision = %s", got)
	}
	if got := ReviewCommand(policy, "git status").Decision; got != ReviewRequireApproval {
		t.Fatalf("approval decision = %s", got)
	}
}

func TestResolveWorkingDirBlocksEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveWorkingDir(root, ".."); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape, got %v", err)
	}
	if _, err := ResolveWorkingDir(root, "."); err != nil {
		t.Fatalf("ResolveWorkingDir returned error: %v", err)
	}
}
