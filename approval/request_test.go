package approval

import (
	"encoding/json"
	"testing"
	"time"
)

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
