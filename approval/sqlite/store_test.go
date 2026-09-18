package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

func TestStoreRoundTripAndRevoke(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "grants.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	grant := approval.Grant{
		Namespace: approval.Namespace{Tenant: "tenant", Subject: "subject"},
		RuleKey:   "rule", Fingerprint: "fingerprint", Action: approval.DecisionApprove,
		RequestID: "request", GrantedAt: now, ExpiresAt: &expires,
	}
	if err := store.Put(ctx, grant); err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	got, err := store.Get(ctx, grant.Namespace, grant.RuleKey, grant.Fingerprint)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.RequestID != grant.RequestID || !got.GrantedAt.Equal(now) ||
		got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("round trip grant = %#v", got)
	}
	if err := store.Revoke(ctx, grant.Namespace, grant.RuleKey, grant.Fingerprint); err != nil {
		t.Fatalf("Revoke returned error: %v", err)
	}
	if _, err := store.Get(ctx, grant.Namespace, grant.RuleKey, grant.Fingerprint); err != approval.ErrGrantNotFound {
		t.Fatalf("revoked Get error = %v", err)
	}
}

func TestStoreConcurrentPutAndGet(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	namespace := approval.Namespace{Tenant: "tenant", Subject: "subject"}
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%02d", i)
			grant := approval.Grant{
				Namespace: namespace, RuleKey: key, Fingerprint: key,
				Action: approval.DecisionApprove, GrantedAt: time.Now().UTC(),
			}
			if err := store.Put(ctx, grant); err != nil {
				errs <- err
				return
			}
			if _, err := store.Get(ctx, namespace, key, key); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent operation: %v", err)
	}
}

func TestStoreGetRejectsMalformedPersistedGrant(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "malformed.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO approval_grants
    (tenant, subject, rule_key, fingerprint, action, request_id, granted_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"tenant", "subject", "rule", "fingerprint", string(approval.DecisionApprove),
		"request", time.Time{}.UTC().Format(time.RFC3339Nano), nil); err != nil {
		t.Fatalf("insert malformed grant: %v", err)
	}
	_, err = store.Get(ctx, approval.Namespace{Tenant: "tenant", Subject: "subject"}, "rule", "fingerprint")
	if err == nil || !strings.Contains(err.Error(), "invalid sqlite approval grant") {
		t.Fatalf("Get error = %v", err)
	}
}

func TestStoreListsTheNamespacesLiveGrants(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "grants.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	expired := now.Add(-time.Minute)
	namespace := approval.Namespace{Tenant: "tenant", Subject: "subject"}
	live := []approval.Grant{
		{Namespace: namespace, Scope: approval.ScopeRule, RuleKey: "rule-b", Action: approval.DecisionApprove, GrantedAt: now},
		{Namespace: namespace, Scope: approval.ScopeRun, RuleKey: "rule-a", Fingerprint: "fp-1", Action: approval.DecisionApprove, GrantedAt: now},
		{Namespace: namespace, Scope: approval.ScopeRule, RuleKey: "rule-a", Action: approval.DecisionApprove, GrantedAt: now},
		{Namespace: approval.Namespace{Tenant: "other", Subject: "subject"}, RuleKey: "rule-c", Action: approval.DecisionApprove, GrantedAt: now},
		{Namespace: namespace, RuleKey: "rule-d", Action: approval.DecisionApprove, GrantedAt: now.Add(-2 * time.Hour), ExpiresAt: &expired},
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
	if listed[0].RuleKey != "rule-a" || listed[0].EffectiveScope() != approval.ScopeRule || listed[0].Fingerprint != "" ||
		listed[1].RuleKey != "rule-a" || listed[1].EffectiveScope() != approval.ScopeRun || listed[1].Fingerprint != "fp-1" ||
		listed[2].RuleKey != "rule-b" {
		t.Fatalf("listed = %#v", listed)
	}
	// A standing grant and a pinned grant for the same rule are one revoke
	// apart, which is what the command relies on.
	if err := store.Revoke(ctx, namespace, "rule-a", ""); err != nil {
		t.Fatalf("Revoke returned error: %v", err)
	}
	listed, err = store.List(ctx, namespace)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(listed) != 2 || listed[0].Fingerprint != "fp-1" {
		t.Fatalf("after revoking the standing grant: %#v", listed)
	}
	// Every grant in the namespace can be taken back by walking the listing.
	for _, grant := range listed {
		if err := store.Revoke(ctx, namespace, grant.RuleKey, grant.Fingerprint); err != nil {
			t.Fatalf("Revoke(%s) returned error: %v", grant.RuleKey, err)
		}
	}
	listed, err = store.List(ctx, namespace)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("listed after revoking everything = %#v", listed)
	}
}
