package approval

import (
	"context"
	"testing"
	"time"
)

func TestAlwaysAllowAndDeny(t *testing.T) {
	req := testRequest()
	allow, err := AlwaysAllow().Request(context.Background(), req)
	if err != nil {
		t.Fatalf("AlwaysAllow returned error: %v", err)
	}
	if allow.Action != DecisionApprove {
		t.Fatalf("allow action = %q", allow.Action)
	}
	deny, err := AlwaysDeny("locked").Request(context.Background(), req)
	if err != nil {
		t.Fatalf("AlwaysDeny returned error: %v", err)
	}
	if deny.Action != DecisionReject || deny.Reason != "locked" {
		t.Fatalf("deny decision = %#v", deny)
	}
}

func TestTimeoutRejects(t *testing.T) {
	broker := WithTimeout(BrokerFunc(func(ctx context.Context, req Request) (Decision, error) {
		<-ctx.Done()
		return Decision{}, ctx.Err()
	}), time.Millisecond)
	decision, err := broker.Request(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if decision.Action != DecisionReject || decision.Reason != ErrorExpired {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestChannelBroker(t *testing.T) {
	requests := make(chan Request, 1)
	decisions := make(chan Decision, 1)
	broker := NewChannelBroker(requests, decisions)
	go func() {
		req := <-requests
		decisions <- Decision{RequestID: req.ID, Action: DecisionApprove}
	}()
	decision, err := broker.Request(context.Background(), testRequest())
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if decision.Action != DecisionApprove || decision.DecidedAt.IsZero() {
		t.Fatalf("decision = %#v", decision)
	}
}

func testRequest() Request {
	return Request{
		ID:        "approval_1",
		RunID:     "run_1",
		Operation: "shell.command",
		Title:     "Approve command",
		Risk:      RiskMedium,
		Options:   DefaultOptions(),
		CreatedAt: time.Now().UTC(),
	}
}
