package policy

import "testing"

func TestPlanFileAccessRootsApprovalAndDeny(t *testing.T) {
	policy := FilePolicy{
		ReadRoots:       []string{"docs"},
		WriteRoots:      []string{"generated"},
		RequireApproval: true,
	}
	if plan := PlanFileAccess(policy, FileRead, "docs/a.md"); !plan.Allowed || plan.Root != "docs" {
		t.Fatalf("read plan = %#v, want docs allow", plan)
	}
	if plan := PlanFileAccess(policy, FileWrite, "tmp/a.md"); !plan.RequiresApproval || plan.Allowed {
		t.Fatalf("write plan = %#v, want approval outside root", plan)
	}
	if plan := PlanFileAccess(FilePolicy{ReadRoots: []string{"docs"}}, FileRead, "tmp/a.md"); plan.Allowed || plan.RequiresApproval {
		t.Fatalf("read plan = %#v, want deny outside root", plan)
	}
}

func TestPlanFileAccessRejectsTraversal(t *testing.T) {
	plan := PlanFileAccess(FilePolicy{ReadRoots: []string{"."}}, FileRead, "../secret.txt")
	if plan.Allowed || plan.RequiresApproval {
		t.Fatalf("traversal plan = %#v, want deny", plan)
	}
}

func TestPlanFileAccessRejectsAbsolutePaths(t *testing.T) {
	plan := PlanFileAccess(FilePolicy{ReadRoots: []string{"."}, RequireApproval: true}, FileRead, "/tmp/secret.txt")
	if plan.Allowed || plan.RequiresApproval {
		t.Fatalf("absolute path plan = %#v, want deny", plan)
	}
}

func TestPlanFileAccessScopesApprovalRulesByLogicalRoot(t *testing.T) {
	filePolicy := FilePolicy{WriteRoots: []string{"generated"}, RequireApproval: true}
	tmpA := PlanFileAccess(filePolicy, FileWrite, "tmp/a.txt")
	tmpB := PlanFileAccess(filePolicy, FileWrite, "tmp/nested/b.txt")
	cache := PlanFileAccess(filePolicy, FileWrite, "cache/a.txt")
	if tmpA.RuleKey != tmpB.RuleKey {
		t.Fatalf("same logical root should share rule key: %#v %#v", tmpA, tmpB)
	}
	if tmpA.RuleKey == cache.RuleKey {
		t.Fatalf("different logical roots must not share rule key: %#v %#v", tmpA, cache)
	}
	if tmpA.Root != "tmp" || cache.Root != "cache" {
		t.Fatalf("unexpected logical roots: %#v %#v", tmpA, cache)
	}
}

func TestPlanFileWriteFingerprintsContent(t *testing.T) {
	access := PlanFileAccess(FilePolicy{WriteRoots: []string{"docs"}}, FileWrite, "docs/a.md")
	first := PlanFileWrite(access, "one", "first")
	second := PlanFileWrite(access, "two", "second")
	if first.SHA256 == second.SHA256 {
		t.Fatalf("expected different content hashes: first=%#v second=%#v", first, second)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatalf("expected content-sensitive fingerprints: first=%#v second=%#v", first, second)
	}
}
