package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

func TestCLIBrokerReadsDecision(t *testing.T) {
	req := approval.Request{
		ID:          "approval_1",
		RunID:       "run_1",
		Operation:   "shell.command",
		Title:       "Approve shell command",
		Description: "Run tests",
		Risk:        approval.RiskHigh,
		Options:     approval.DefaultOptions(),
		CreatedAt:   time.Now().UTC(),
	}
	var out bytes.Buffer
	decision, err := New(strings.NewReader("2\n"), &out).Request(context.Background(), req)
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if decision.Action != approval.DecisionReject || decision.RequestID != req.ID {
		t.Fatalf("decision = %#v", decision)
	}
	if !strings.Contains(out.String(), "Approval required: Approve shell command") {
		t.Fatalf("prompt output = %q", out.String())
	}
}
