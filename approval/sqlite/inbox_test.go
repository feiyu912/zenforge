package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

func TestInboxRegisterConcurrentAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	a := openInbox(t, path)
	defer a.Close()
	b := openInbox(t, path)
	defer b.Close()
	req := testRequest("request", time.Time{})

	errs := concurrently(
		func() error { return a.Register(context.Background(), req) },
		func() error { return b.Register(context.Background(), req) },
	)
	for _, err := range errs {
		if err != nil {
			t.Fatalf("idempotent Register error = %v", err)
		}
	}
	changed := req
	changed.Title = "different"
	if err := b.Register(context.Background(), changed); !errors.Is(err, approval.ErrRequestConflict) {
		t.Fatalf("conflicting Register error = %v", err)
	}
}

func TestInboxResolveConcurrentWinnersAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	a := openInbox(t, path)
	defer a.Close()
	b := openInbox(t, path)
	defer b.Close()
	ctx := context.Background()
	req := testRequest("request", time.Time{})
	if err := a.Register(ctx, req); err != nil {
		t.Fatal(err)
	}
	approve := testDecision(req.ID, approval.DecisionApprove)
	reject := testDecision(req.ID, approval.DecisionReject)
	errs := concurrently(
		func() error { return a.Resolve(ctx, approve) },
		func() error { return b.Resolve(ctx, reject) },
	)
	var won, conflicted int
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, approval.ErrDecisionConflict):
			conflicted++
		default:
			t.Fatalf("Resolve error = %v", err)
		}
	}
	if won != 1 || conflicted != 1 {
		t.Fatalf("winner/conflict = %d/%d", won, conflicted)
	}
	record, err := a.Get(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry := *record.Decision
	retry.DecidedAt = retry.DecidedAt.Add(time.Hour)
	if err := b.Resolve(ctx, retry); err != nil {
		t.Fatalf("semantic retry error = %v", err)
	}
	conflict := testDecision(req.ID, approval.DecisionAbort)
	if err := b.Resolve(ctx, conflict); !errors.Is(err, approval.ErrDecisionConflict) {
		t.Fatalf("second decision error = %v", err)
	}
}

func TestInboxConcurrentIdenticalResolveIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	a := openInbox(t, path)
	defer a.Close()
	b := openInbox(t, path)
	defer b.Close()
	req := testRequest("request", time.Time{})
	if err := a.Register(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	decision := testDecision(req.ID, approval.DecisionApprove)
	for _, err := range concurrently(
		func() error { return a.Resolve(context.Background(), decision) },
		func() error { return b.Resolve(context.Background(), decision) },
	) {
		if err != nil {
			t.Fatalf("identical Resolve error = %v", err)
		}
	}
}

func TestInboxExpiryListAndCanonicalResolution(t *testing.T) {
	store := openInbox(t, filepath.Join(t.TempDir(), "inbox.db"))
	defer store.Close()
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Second)
	expired := testRequest("expired", past)
	pending := testRequest("pending", time.Time{})
	pending.RunID = "other-run"
	if err := store.Register(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err := store.Register(ctx, pending); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListPending(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != pending.ID {
		t.Fatalf("ListPending = %#v", list)
	}
	list, err = store.ListPending(ctx, "run")
	if err != nil || len(list) != 0 {
		t.Fatalf("filtered ListPending = %#v, %v", list, err)
	}
	if err := store.Resolve(ctx, testDecision(expired.ID, approval.DecisionApprove)); !errors.Is(err, approval.ErrRequestExpired) {
		t.Fatalf("expired user decision error = %v", err)
	}
	canonical := testDecision(expired.ID, approval.DecisionReject)
	canonical.Reason = approval.ErrorExpired
	if err := store.Resolve(ctx, canonical); err != nil {
		t.Fatalf("canonical expired decision error = %v", err)
	}
}

func TestInboxReopenPreservesResolvedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store := openInbox(t, path)
	req := testRequest("request", time.Time{})
	if err := store.Register(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := store.Resolve(context.Background(), testDecision(req.ID, approval.DecisionApprove)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openInbox(t, path)
	defer store.Close()
	record, err := store.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != approval.StatusResolved || record.Decision == nil {
		t.Fatalf("reopened record = %#v", record)
	}
}

func TestInboxContextClosedAndMalformed(t *testing.T) {
	store := openInbox(t, filepath.Join(t.TempDir(), "inbox.db"))
	req := testRequest("request", time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Register(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Register error = %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`
INSERT INTO approval_inbox
    (request_id, run_id, request_json, status, created_at, updated_at)
VALUES ('broken', 'run', '{', 'pending', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "broken"); err == nil ||
		!strings.Contains(err.Error(), "invalid sqlite approval request") {
		t.Fatalf("malformed Get error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), req.ID); err == nil {
		t.Fatal("Get on closed store succeeded")
	}
}

func openInbox(t *testing.T, path string) *InboxStore {
	t.Helper()
	store, err := OpenInbox(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenInbox: %v", err)
	}
	return store
}

func testRequest(id string, expires time.Time) approval.Request {
	req := approval.Request{
		ID: id, RunID: "run", Operation: "write", Title: "Write",
		Risk: approval.RiskHigh, Options: approval.DefaultOptions(),
		CreatedAt: time.Now().UTC(), Payload: map[string]any{"nested": map[string]any{"ok": true}},
	}
	if !expires.IsZero() {
		req.ExpiresAt = &expires
		if !expires.After(req.CreatedAt) {
			req.CreatedAt = expires.Add(-time.Second)
		}
	}
	return req
}

func testDecision(id string, action approval.DecisionAction) approval.Decision {
	return approval.Decision{
		RequestID: id, Action: action, Scope: approval.ScopeOnce,
		DecidedAt: time.Now().UTC(), Payload: map[string]any{"answer": "yes"},
	}
}

func concurrently(fns ...func() error) []error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func(i int, fn func() error) {
			defer wg.Done()
			errs[i] = fn()
		}(i, fn)
	}
	wg.Wait()
	return errs
}
