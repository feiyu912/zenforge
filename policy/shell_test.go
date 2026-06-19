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

func TestReviewCommandDoesNotAllowChainsByFirstCommandPrefix(t *testing.T) {
	policy := ShellPolicy{
		AllowCommands:   []string{"go test ./..."},
		RequireApproval: true,
	}
	cases := []string{
		"go test ./... && rm -rf tmp",
		"go test ./...; rm -rf tmp",
		"go test ./... | cat",
		"go test ./... $(printf bad)",
	}
	for _, command := range cases {
		review := ReviewCommand(policy, command)
		if review.Decision == ReviewAllow {
			t.Fatalf("ReviewCommand(%q) = %#v, want approval or block", command, review)
		}
	}
}

func TestReviewCommandRequiresApprovalForOutputRedirection(t *testing.T) {
	review := ReviewCommand(ShellPolicy{AllowCommands: []string{"go test ./..."}, RequireApproval: true}, "go test ./... > out.txt")
	if review.Decision != ReviewRequireApproval || review.RuleKey != "bashsec:redirections" {
		t.Fatalf("review = %#v, want redirection approval", review)
	}
}

func TestReviewCommandUsesShellASTForQuotedMetacharacters(t *testing.T) {
	policy := ShellPolicy{AllowCommands: []string{"echo"}}
	for _, command := range []string{
		`echo 'a|b'`,
		`echo 'x > y'`,
	} {
		if review := ReviewCommand(policy, command); review.Decision != ReviewAllow {
			t.Fatalf("ReviewCommand(%q) = %#v, want allow", command, review)
		}
	}
	if review := ReviewCommand(policy, `echo "a;b"`); review.Decision != ReviewBlock {
		t.Fatalf("ambiguous quoted metacharacter review = %#v, want block", review)
	}
}

func TestReviewCommandUsesPlatformBashSecuritySemantics(t *testing.T) {
	policy := ShellPolicy{AllowCommands: []string{"echo", "cat", "python3"}, RequireApproval: true}
	tests := []struct {
		command  string
		decision ReviewDecision
		ruleKey  string
	}{
		{command: `echo hi | cat`, decision: ReviewAllow, ruleKey: "allow:parsed"},
		{command: `echo hi > out.txt`, decision: ReviewRequireApproval, ruleKey: "bashsec:redirections"},
		{command: `echo $(cat secret.txt)`, decision: ReviewAllow, ruleKey: "allow:parsed"},
		{command: `python3 -c 'import subprocess; subprocess.run(["id"])'`, decision: ReviewBlock},
	}
	for _, tt := range tests {
		review := ReviewCommand(policy, tt.command)
		if review.Decision != tt.decision || review.RuleKey != tt.ruleKey {
			t.Fatalf("ReviewCommand(%q) = %#v, want %s %q", tt.command, review, tt.decision, tt.ruleKey)
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
