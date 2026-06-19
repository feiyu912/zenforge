package approval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAbortErrorSignalsRunCancellation(t *testing.T) {
	err := NewAbortError("operator stopped")
	if !errors.Is(err, ErrAborted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("abort error does not signal cancellation: %v", err)
	}
	if err.Error() != "approval aborted: operator stopped" {
		t.Fatalf("abort error = %q", err)
	}
}

func TestValidateDecisionForRequestBindsIdentityAndScope(t *testing.T) {
	req := Request{ID: "approval_1", Payload: map[string]any{"fingerprint": "fp_1"}}
	valid := Decision{RequestID: req.ID, Action: DecisionApprove, Scope: ScopeRun}
	if err := ValidateDecisionForRequest(req, valid); err != nil {
		t.Fatalf("valid decision returned error: %v", err)
	}
	wrong := valid
	wrong.RequestID = "approval_other"
	if err := ValidateDecisionForRequest(req, wrong); err == nil {
		t.Fatal("expected mismatched request id error")
	}
	missingScopeKey := valid
	missingScopeKey.Scope = ScopeRule
	if err := ValidateDecisionForRequest(req, missingScopeKey); err == nil {
		t.Fatal("expected missing rule key error")
	}
}

func TestRequestJSONRoundTripAndValidation(t *testing.T) {
	req := Request{
		ID:        "approval_1",
		RunID:     "run_1",
		Operation: "shell.command",
		Title:     "Approve command",
		Risk:      RiskHigh,
		Options:   DefaultOptions(),
		CreatedAt: time.Unix(1, 0).UTC(),
		Payload:   map[string]any{"command": "git status"},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded.ID != req.ID || decoded.Options[0].Action != DecisionApprove {
		t.Fatalf("unexpected decoded request: %#v", decoded)
	}
}

func TestRequiredResultRoundTrip(t *testing.T) {
	req := Request{
		ID:        "approval_1",
		RunID:     "run_1",
		Operation: "shell.command",
		Title:     "Approve command",
		Risk:      RiskMedium,
		Options:   DefaultOptions(),
		CreatedAt: time.Now().UTC(),
	}
	got, ok := RequestFromResult(RequiredResult(req))
	if !ok {
		t.Fatalf("expected request from result")
	}
	if got.ID != req.ID {
		t.Fatalf("request id = %q, want %q", got.ID, req.ID)
	}
}

func TestRequiredPlanValidatesRequest(t *testing.T) {
	req := Request{
		ID:        "approval_1",
		RunID:     "run_1",
		Operation: "shell.command",
		Title:     "Approve command",
		Risk:      RiskHigh,
		Options:   DefaultOptions(),
		CreatedAt: time.Now().UTC(),
	}
	plan := RequiredPlan(req)
	if !plan.Required {
		t.Fatalf("plan should be required")
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if err := (Plan{}).Validate(); err != nil {
		t.Fatalf("optional plan Validate returned error: %v", err)
	}
}

func TestScopeKeyRequiresMatchingRequestIdentity(t *testing.T) {
	req := testScopeRequest()
	if got, err := ScopeKey(req, ScopeRun); err != nil || got != "fingerprint_1" {
		t.Fatalf("run scope key = %q, err=%v", got, err)
	}
	if got, err := ScopeKey(req, ScopeRule); err != nil || got != "rule_1" {
		t.Fatalf("rule scope key = %q, err=%v", got, err)
	}
	delete(req.Payload, "fingerprint")
	if _, err := ScopeKey(req, ScopeRun); err == nil {
		t.Fatal("run scope accepted missing fingerprint")
	}
	delete(req.Payload, "ruleKey")
	if _, err := ScopeKey(req, ScopeRule); err == nil {
		t.Fatal("rule scope accepted missing rule key")
	}
}

func TestDecisionValidationRejectsUnknownActionAndScope(t *testing.T) {
	if err := (Decision{RequestID: "approval_1", Action: "permit"}).Validate(); err == nil {
		t.Fatal("unknown decision action was accepted")
	}
	if err := (Decision{RequestID: "approval_1", Action: DecisionApprove, Scope: "forever"}).Validate(); err == nil {
		t.Fatal("unknown decision scope was accepted")
	}
}

func testScopeRequest() Request {
	return Request{
		ID:        "approval_1",
		RunID:     "run_1",
		Operation: "shell.command",
		Title:     "Approve command",
		Risk:      RiskHigh,
		Options:   DefaultOptions(),
		Payload: map[string]any{
			"fingerprint": "fingerprint_1",
			"ruleKey":     "rule_1",
		},
	}
}
