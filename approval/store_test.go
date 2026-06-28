package approval

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryGrantStoreIsolationExpiryAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGrantStore()
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	namespace := Namespace{Tenant: "tenant-a", Subject: "user-1"}
	expires := now.Add(time.Hour)
	grant := Grant{
		Namespace: namespace, RuleKey: "rule-1", Fingerprint: "fp-1",
		Action: DecisionApprove, GrantedAt: now, ExpiresAt: &expires,
	}
	if err := store.Put(ctx, grant); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if _, err := store.Get(ctx, Namespace{Tenant: "tenant-b", Subject: "user-1"}, "rule-1", "fp-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("tenant-isolated Get error = %v", err)
	}
	if _, err := store.Get(ctx, namespace, "rule-1", "fp-other"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("fingerprint-isolated Get error = %v", err)
	}
	now = expires
	if _, err := store.Get(ctx, namespace, "rule-1", "fp-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("expired Get error = %v", err)
	}

	grant.ExpiresAt = nil
	grant.GrantedAt = now
	if err := store.Put(ctx, grant); err != nil {
		t.Fatalf("second Put returned error: %v", err)
	}
	if err := store.Revoke(ctx, namespace, "rule-1", "fp-1"); err != nil {
		t.Fatalf("Revoke returned error: %v", err)
	}
	if _, err := store.Get(ctx, namespace, "rule-1", "fp-1"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("revoked Get error = %v", err)
	}
}

func TestMemoryGrantStoreHonorsCancellation(t *testing.T) {
	store := NewMemoryGrantStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Get(ctx, Namespace{Tenant: "t", Subject: "s"}, "r", "f"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v", err)
	}
}
