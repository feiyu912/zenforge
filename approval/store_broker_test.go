package approval_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/approval/memory"
)

func TestStoreBrokerCancellationLeavesRequestAndRestartConsumesDecision(t *testing.T) {
	store := memory.NewStore()
	broker := newBroker(t, store)
	req := request("approval_cancel")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := broker.Request(ctx, req)
		done <- err
	}()
	waitForRecord(t, store, req.ID)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Request error = %v", err)
	}
	decision := approval.Decision{RequestID: req.ID, Action: approval.DecisionApprove}
	if err := broker.Submit(context.Background(), decision); err != nil {
		t.Fatal(err)
	}
	restarted := newBroker(t, store)
	got, err := restarted.Request(context.Background(), req)
	if err != nil || got.Action != approval.DecisionApprove {
		t.Fatalf("restarted Request = (%+v, %v)", got, err)
	}
}

func TestStoreBrokerOptionsDefaultAndValidation(t *testing.T) {
	if _, err := approval.NewStoreBroker(memory.NewStore(), approval.StoreBrokerOptions{}); err != nil {
		t.Fatalf("zero options: %v", err)
	}
	if _, err := approval.NewStoreBroker(memory.NewStore(), approval.StoreBrokerOptions{
		PollInterval: -time.Millisecond,
	}); err == nil {
		t.Fatal("negative poll interval accepted")
	}
	var typedNil *memory.Store
	if _, err := approval.NewStoreBroker(typedNil, approval.StoreBrokerOptions{}); err == nil {
		t.Fatal("typed-nil store accepted")
	}
}

func TestStoreBrokerCrossBrokerSubmit(t *testing.T) {
	store := memory.NewStore()
	waiter, submitter := newBroker(t, store), newBroker(t, store)
	req := request("approval_cross")
	done := make(chan approval.Decision, 1)
	go func() {
		got, _ := waiter.Request(context.Background(), req)
		done <- got
	}()
	waitForRecord(t, store, req.ID)
	if err := submitter.Submit(context.Background(), approval.Decision{
		RequestID: req.ID, Action: approval.DecisionReject,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.Action != approval.DecisionReject {
			t.Fatalf("decision action = %q", got.Action)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not consume decision")
	}
}

func TestStoreBrokerDurablyResolvesExpiry(t *testing.T) {
	store := memory.NewStore()
	broker := newBroker(t, store)
	req := request("approval_expiry")
	expires := time.Now().UTC().Add(5 * time.Millisecond)
	req.ExpiresAt = &expires

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	decision, err := broker.Request(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != approval.DecisionReject || decision.Reason != approval.ErrorExpired {
		t.Fatalf("expired decision = %+v", decision)
	}
	record, err := store.Get(context.Background(), req.ID)
	if err != nil || record.Status != approval.StatusResolved || record.Decision == nil {
		t.Fatalf("durable expired record = (%+v, %v)", record, err)
	}
}

func newBroker(t *testing.T, store approval.PendingStore) *approval.StoreBroker {
	t.Helper()
	broker, err := approval.NewStoreBroker(store, approval.StoreBrokerOptions{
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func waitForRecord(t *testing.T, store approval.PendingStore, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.Get(context.Background(), id); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("request was not registered")
}

func request(id string) approval.Request {
	return approval.Request{
		ID: id, RunID: "run_1", Operation: "write", Title: "Write",
		Risk: approval.RiskHigh, Options: approval.DefaultOptions(),
		CreatedAt: time.Now().UTC(),
	}
}
