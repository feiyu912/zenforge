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

func TestMemoryGrantStoreListsTheNamespacesLiveGrants(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryGrantStore()
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	namespace := Namespace{Tenant: "tenant-a", Subject: "user-1"}
	expired := now.Add(-time.Minute)
	live := []Grant{
		{Namespace: namespace, RuleKey: "rule-b", Action: DecisionApprove, GrantedAt: now},
		{Namespace: namespace, RuleKey: "rule-a", Fingerprint: "fp-1", Action: DecisionApprove, GrantedAt: now},
		{Namespace: namespace, RuleKey: "rule-a", Action: DecisionApprove, GrantedAt: now},
		{Namespace: Namespace{Tenant: "tenant-b", Subject: "user-1"}, RuleKey: "rule-c", Action: DecisionApprove, GrantedAt: now},
		{Namespace: namespace, RuleKey: "rule-d", Action: DecisionApprove, GrantedAt: now.Add(-2 * time.Hour), ExpiresAt: &expired},
	}
	for _, grant := range live {
		if err := store.Put(ctx, grant); err != nil {
			t.Fatalf("Put returned error: %v", err)
		}
	}
	listed, err := store.List(ctx, namespace)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed = %#v", listed)
	}
	// Sorted by rule key, standing grant before the pinned entry for the same
	// rule, other tenants and expired grants absent.
	if listed[0].RuleKey != "rule-a" || listed[0].EffectiveScope() != ScopeRule ||
		listed[1].RuleKey != "rule-a" || listed[1].Fingerprint != "fp-1" || listed[1].EffectiveScope() != ScopeRun ||
		listed[2].RuleKey != "rule-b" {
		t.Fatalf("listed = %#v", listed)
	}
	// The listing is a copy: a caller mutating it must not reach the store.
	listed[0].Action = DecisionReject
	stored, err := store.Get(ctx, namespace, "rule-a", "")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if stored.Action != DecisionApprove || stored.Scope != ScopeRule {
		t.Fatalf("the store was mutated through the listing: %#v", stored)
	}
	if _, err := store.List(ctx, Namespace{Tenant: "tenant-a"}); err == nil {
		t.Fatal("List accepted an incomplete namespace")
	}
}
