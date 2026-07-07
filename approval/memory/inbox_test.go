package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

func TestStoreRegisterConflictCloningAndSorting(t *testing.T) {
	store := NewStore()
	b := request("b")
	a := request("a")
	a.Payload = map[string]any{"nested": map[string]any{"key": "original"}}
	if err := store.Register(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := store.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	a.Payload["nested"].(map[string]any)["key"] = "mutated"
	got, _ := store.Get(context.Background(), "a")
	if got.Request.Payload["nested"].(map[string]any)["key"] != "original" {
		t.Fatal("store retained caller-owned request data")
	}
	list, _ := store.ListPending(context.Background(), "")
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatalf("unexpected order: %+v", list)
	}
	conflict := got.Request
	conflict.Title = "Different"
	if err := store.Register(context.Background(), conflict); !errors.Is(err, approval.ErrRequestConflict) {
		t.Fatalf("Register conflict = %v", err)
	}
	if err := store.Register(context.Background(), got.Request); err != nil {
		t.Fatalf("exact Register retry = %v", err)
	}
}

func TestStoreResolveSemanticRetryConflictAndOwnership(t *testing.T) {
	store := NewStore()
	req := request("resolve")
	if err := store.Register(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	first := approval.Decision{
		RequestID: req.ID, Action: approval.DecisionApprove,
		Payload: map[string]any{"value": "original"}, DecidedAt: time.Unix(1, 0),
	}
	if err := store.Resolve(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	first.Payload["value"] = "mutated"
	retry := approval.Decision{
		RequestID: req.ID, Action: approval.DecisionApprove,
		Payload: map[string]any{"value": "original"}, DecidedAt: time.Unix(2, 0),
	}
	if err := store.Resolve(context.Background(), retry); err != nil {
		t.Fatalf("semantic retry = %v", err)
	}
	conflict := retry
	conflict.Action = approval.DecisionReject
	if err := store.Resolve(context.Background(), conflict); !errors.Is(err, approval.ErrDecisionConflict) {
		t.Fatalf("decision conflict = %v", err)
	}
	got, _ := store.Get(context.Background(), req.ID)
	if got.Decision.Payload["value"] != "original" {
		t.Fatal("store retained caller-owned decision data")
	}
}

func TestStoreExpiryAndConcurrentResolve(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	store := NewStoreWithClock(func() time.Time { return now })
	expiry := now.Add(time.Second)
	req := request("expiry")
	req.CreatedAt, req.ExpiresAt = now, &expiry
	if err := store.Register(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	now = expiry
	if list, _ := store.ListPending(context.Background(), ""); len(list) != 0 {
		t.Fatal("expired request listed as pending")
	}
	if err := store.Resolve(context.Background(), approval.Decision{
		RequestID: req.ID, Action: approval.DecisionApprove,
	}); !errors.Is(err, approval.ErrRequestExpired) {
		t.Fatalf("expired Resolve = %v", err)
	}

	race := request("race")
	raceStore := NewStore()
	_ = raceStore.Register(context.Background(), race)
	decisions := []approval.Decision{
		{RequestID: race.ID, Action: approval.DecisionApprove},
		{RequestID: race.ID, Action: approval.DecisionReject},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(decisions))
	for i := range decisions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = raceStore.Resolve(context.Background(), decisions[i])
		}(i)
	}
	wg.Wait()
	winners, conflicts := 0, 0
	for _, err := range errs {
		if err == nil {
			winners++
		} else if errors.Is(err, approval.ErrDecisionConflict) {
			conflicts++
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d errors=%v", winners, conflicts, errs)
	}
}

func request(id string) approval.Request {
	return approval.Request{
		ID: id, RunID: "run", Operation: "op", Title: "title",
		Risk: approval.RiskLow, Options: approval.DefaultOptions(),
		CreatedAt: time.Unix(1, 0).UTC(),
	}
}
