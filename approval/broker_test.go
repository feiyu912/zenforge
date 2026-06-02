package approval

import (
	"context"
	"errors"
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

func TestPendingBrokerWaitsForSubmittedDecision(t *testing.T) {
	broker := NewPendingBroker(1)
	result := make(chan Decision, 1)
	errs := make(chan error, 1)
	req := testRequest()

	go func() {
		decision, err := broker.Request(context.Background(), req)
		if err != nil {
			errs <- err
			return
		}
		result <- decision
	}()

	observed := <-broker.Requests()
	if observed.ID != req.ID || observed.RunID != req.RunID {
		t.Fatalf("unexpected observed request: %#v", observed)
	}
	if pending, ok := broker.Pending(req.ID); !ok || pending.ID != req.ID {
		t.Fatalf("pending request not found: %#v ok=%v", pending, ok)
	}

	if err := broker.Submit(context.Background(), Decision{RequestID: req.ID, Action: DecisionApprove}); err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	select {
	case err := <-errs:
		t.Fatalf("Request returned error: %v", err)
	case decision := <-result:
		if decision.Action != DecisionApprove || decision.Scope != ScopeOnce || decision.DecidedAt.IsZero() {
			t.Fatalf("unexpected decision: %#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for decision")
	}
	if _, ok := broker.Pending(req.ID); ok {
		t.Fatalf("request remained pending after submit")
	}
}

func TestPendingBrokerListsAndRemovesCanceledRequests(t *testing.T) {
	broker := NewPendingBroker(0)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	req := testRequest()

	go func() {
		_, err := broker.Request(ctx, req)
		errs <- err
	}()
	for {
		if len(broker.ListPending()) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()

	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if pending := broker.ListPending(); len(pending) != 0 {
		t.Fatalf("pending after cancel: %#v", pending)
	}
}

func TestPendingBrokerListsPendingForRun(t *testing.T) {
	broker := NewPendingBroker(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 3)
	startPendingRequest := func(req Request) {
		go func() {
			_, err := broker.Request(ctx, req)
			errs <- err
		}()
	}
	reqB := testRequest()
	reqB.ID = "approval_b"
	reqA := testRequest()
	reqA.ID = "approval_a"
	reqOther := testRequest()
	reqOther.ID = "approval_other"
	reqOther.RunID = "run_other"
	startPendingRequest(reqB)
	startPendingRequest(reqA)
	startPendingRequest(reqOther)

	for {
		if len(broker.ListPending()) == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	pending := broker.ListPendingForRun("run_1")
	if len(pending) != 2 {
		t.Fatalf("pending for run_1 = %#v", pending)
	}
	if pending[0].ID != "approval_a" || pending[1].ID != "approval_b" {
		t.Fatalf("pending requests were not sorted by id: %#v", pending)
	}
	if other := broker.ListPendingForRun("run_missing"); len(other) != 0 {
		t.Fatalf("pending for missing run = %#v", other)
	}
	cancel()
	for i := 0; i < 3; i++ {
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	}
}

func TestPendingBrokerRejectsUnknownDecision(t *testing.T) {
	broker := NewPendingBroker(0)
	err := broker.Submit(context.Background(), Decision{RequestID: "missing", Action: DecisionApprove})
	if !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("expected ErrRequestNotFound, got %v", err)
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
