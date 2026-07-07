package approval

import (
	"context"
	"testing"
)

func TestPendingBrokerImplementsInboxButNotRegistrar(t *testing.T) {
	var value any = NewPendingBroker(0)
	if _, ok := value.(Inbox); !ok {
		t.Fatal("PendingBroker does not implement Inbox")
	}
	if _, ok := value.(RequestRegistrar); ok {
		t.Fatal("PendingBroker unexpectedly implements RequestRegistrar")
	}
}

func TestRecordValidateStrictState(t *testing.T) {
	req := testRequest()
	record := Record{
		Request: req, Status: StatusPending,
		CreatedAt: req.CreatedAt, UpdatedAt: req.CreatedAt,
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("valid pending record: %v", err)
	}
	decision := Decision{RequestID: req.ID, Action: DecisionApprove, Scope: ScopeOnce}
	record.Decision = &decision
	if err := record.Validate(); err == nil {
		t.Fatal("pending record with decision validated")
	}
}

func TestPendingBrokerInboxLookupAndList(t *testing.T) {
	broker := NewPendingBroker(1)
	req := testRequest()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := broker.Request(ctx, req)
		done <- err
	}()
	<-broker.Requests()
	got, err := broker.Lookup(context.Background(), req.ID)
	if err != nil || got.ID != req.ID {
		t.Fatalf("Lookup = (%q, %v)", got.ID, err)
	}
	list, err := broker.List(context.Background(), req.RunID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = (%d, %v)", len(list), err)
	}
	cancel()
	<-done
}
