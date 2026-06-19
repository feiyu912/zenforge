package bashsec

import "testing"

func TestReviewClassifiesShellStructure(t *testing.T) {
	tests := []struct {
		command  string
		decision ReviewDecision
		ruleKey  string
	}{
		{command: `echo 'a|b'`, decision: ReviewAllow},
		{command: `echo hi | cat`, decision: ReviewBlock, ruleKey: "shell_control"},
		{command: `echo hi > out.txt`, decision: ReviewBlock, ruleKey: "shell_redirection"},
		{command: `echo $(date)`, decision: ReviewBlock, ruleKey: "shell_substitution"},
		{command: `(echo hi)`, decision: ReviewRequiresApproval, ruleKey: "bashast:too_complex"},
		{command: `eval 'echo hi'`, decision: ReviewBlock, ruleKey: "dangerous:eval"},
		{command: `python3 -c 'import os; os.system("id")'`, decision: ReviewBlock, ruleKey: "embedded_script:python"},
	}
	for _, tt := range tests {
		review := Review(tt.command, nil)
		if review.Decision != tt.decision || review.RuleKey != tt.ruleKey {
			t.Fatalf("Review(%q) = %#v", tt.command, review)
		}
	}
}

func TestReviewExtractsExecutableAfterEnvironmentAssignment(t *testing.T) {
	review := Review(`TARGET=tmp rm -rf tmp`, nil)
	if review.Decision != ReviewAllow || len(review.Commands) != 1 || review.Commands[0][0] != "rm" {
		t.Fatalf("review = %#v", review)
	}
}
