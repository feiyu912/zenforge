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
	req.Payload["fingerprint"] = "  "
	if _, err := ScopeKey(req, ScopeRun); err == nil {
		t.Fatal("run scope accepted blank fingerprint")
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

func TestRequestValidationRejectsUnknownRiskAndInvalidOptions(t *testing.T) {
	req := testRequest()
	req.Risk = "severe"
	if err := req.Validate(); err == nil {
		t.Fatal("unknown risk was accepted")
	}
	req = testRequest()
	req.Options = []Option{{Action: "permit", Label: "Permit"}}
	if err := req.Validate(); err == nil {
		t.Fatal("unknown option action was accepted")
	}
	req.Options = []Option{{Action: DecisionApprove}}
	if err := req.Validate(); err == nil {
		t.Fatal("empty option label was accepted")
	}
}

func TestBindRequestOwnsRoutingIdentityAndCopiesPayload(t *testing.T) {
	req := testRequest()
	req.ID = ""
	req.RunID = "forged_run"
	req.ToolCallID = "forged_call"
	req.ToolName = "forged_tool"
	req.CreatedAt = time.Time{}
	req.Payload = map[string]any{"nested": map[string]any{"value": "original"}}

	bound := BindRequest(req, "run_real", "call_real", "tool_real")
	if bound.ID == "" || bound.CreatedAt.IsZero() {
		t.Fatalf("missing generated request identity: %#v", bound)
	}
	if bound.RunID != "run_real" || bound.ToolCallID != "call_real" || bound.ToolName != "tool_real" {
		t.Fatalf("routing identity was not bound: %#v", bound)
	}
	bound.Payload["nested"].(map[string]any)["value"] = "changed"
	if got := req.Payload["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("binding aliased source payload: %v", got)
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
