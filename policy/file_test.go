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
