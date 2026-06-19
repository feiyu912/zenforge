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

func TestReviewCommandBlocksShellControlOperatorsBeforeAllowlist(t *testing.T) {
	policy := ShellPolicy{
		AllowCommands:   []string{"go test ./..."},
		RequireApproval: true,
	}
	cases := []struct {
		command string
		ruleKey string
	}{
		{command: "go test ./... && rm -rf tmp", ruleKey: "shell_control"},
		{command: "go test ./...; rm -rf tmp", ruleKey: "shell_control"},
		{command: "go test ./... | cat", ruleKey: "shell_control"},
		{command: "go test ./... > out.txt", ruleKey: "shell_redirection"},
		{command: "go test ./... $(printf bad)", ruleKey: "shell_substitution"},
	}
	for _, tt := range cases {
		review := ReviewCommand(policy, tt.command)
		if review.Decision != ReviewBlock || review.RuleKey != tt.ruleKey {
			t.Fatalf("ReviewCommand(%q) = %#v, want %s block", tt.command, review, tt.ruleKey)
		}
	}
}

func TestReviewCommandUsesShellASTForQuotedMetacharacters(t *testing.T) {
	policy := ShellPolicy{AllowCommands: []string{"echo"}}
	for _, command := range []string{
		`echo 'a|b'`,
		`echo "a;b"`,
		`echo 'x > y'`,
	} {
		if review := ReviewCommand(policy, command); review.Decision != ReviewAllow {
			t.Fatalf("ReviewCommand(%q) = %#v, want allow", command, review)
		}
	}
}

func TestReviewCommandBlocksASTDangerousStructures(t *testing.T) {
	policy := ShellPolicy{AllowCommands: []string{"echo", "cat", "python3"}, RequireApproval: true}
	tests := []struct {
		command string
		ruleKey string
	}{
		{command: `echo hi | cat`, ruleKey: "shell_control"},
		{command: `echo hi > out.txt`, ruleKey: "shell_redirection"},
		{command: `echo $(cat secret.txt)`, ruleKey: "shell_substitution"},
		{command: `python3 -c 'import subprocess; subprocess.run(["id"])'`, ruleKey: "embedded_script:python"},
	}
	for _, tt := range tests {
		review := ReviewCommand(policy, tt.command)
		if review.Decision != ReviewBlock || review.RuleKey != tt.ruleKey {
			t.Fatalf("ReviewCommand(%q) = %#v, want block %q", tt.command, review, tt.ruleKey)
		}
	}
}

func TestReviewCommandAppliesDenyRulesAfterEnvironmentAssignments(t *testing.T) {
	policy := ShellPolicy{AllowCommands: []string{"env"}, DenyCommands: []string{"rm"}}
	review := ReviewCommand(policy, `TARGET=tmp rm -rf tmp`)
	if review.Decision != ReviewBlock || review.RuleKey != "deny:rm" {
		t.Fatalf("review = %#v, want deny:rm", review)
	}
}

func TestReviewCommandRequiresApprovalWhenASTIsTooComplex(t *testing.T) {
	review := ReviewCommand(ShellPolicy{RequireApproval: true}, `(echo hi)`)
	if review.Decision != ReviewRequireApproval || review.RuleKey != "bashast:too_complex" {
		t.Fatalf("review = %#v, want AST approval", review)
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
