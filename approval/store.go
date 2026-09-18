package approval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrGrantNotFound = errors.New("approval grant not found")

// Namespace is host-owned identity used to isolate persistent grants.
type Namespace struct {
	Tenant  string `json:"tenant"`
	Subject string `json:"subject"`
}

func (n Namespace) Validate() error {
	if strings.TrimSpace(n.Tenant) == "" {
		return fmt.Errorf("approval grant tenant is required")
	}
	if strings.TrimSpace(n.Subject) == "" {
		return fmt.Errorf("approval grant subject is required")
	}
	return nil
}

// Grant authorizes a rule's calls. RuleKey names the tool (or operation) the
// operator approved. A grant that pins a fingerprint authorizes exactly the
// call that fingerprint covers; a grant that pins none authorizes every call
// to the rule, whatever its arguments — that is what an in-run rule grant
// means, and what "always allow this tool" must mean once it outlives the run.
type Grant struct {
	Namespace   Namespace      `json:"namespace"`
	Scope       DecisionScope  `json:"scope,omitempty"`
	RuleKey     string         `json:"ruleKey"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Action      DecisionAction `json:"action"`
	RequestID   string         `json:"requestId,omitempty"`
	GrantedAt   time.Time      `json:"grantedAt"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
}

// EffectiveScope is the scope a grant's shape authorizes. A grant that names
// no scope is the original payload-pinned form, so an existing store keeps
// its meaning.
func (g Grant) EffectiveScope() DecisionScope {
	if g.Scope != "" {
		return g.Scope
	}
	if strings.TrimSpace(g.Fingerprint) == "" {
		return ScopeRule
	}
	return ScopeRun
}

func (g Grant) Validate() error {
	if err := g.Namespace.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(g.RuleKey) == "" {
		return fmt.Errorf("approval grant ruleKey is required")
	}
	switch g.EffectiveScope() {
	case ScopeRule:
		// The absence of a fingerprint is the rule form; pinning one here
		// would narrow what the scope promises without saying so.
		if strings.TrimSpace(g.Fingerprint) != "" {
			return fmt.Errorf("approval grant rule scope must not pin a fingerprint")
		}
	case ScopeRun:
		if strings.TrimSpace(g.Fingerprint) == "" {
			return fmt.Errorf("approval grant fingerprint is required")
		}
	default:
		return fmt.Errorf("unsupported approval grant scope %q", g.Scope)
	}
	if !IsApprovedAction(g.Action) {
		return fmt.Errorf("approval grant action must approve")
	}
	if g.GrantedAt.IsZero() {
		return fmt.Errorf("approval grant grantedAt is required")
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(g.GrantedAt) {
		return fmt.Errorf("approval grant expiresAt must be after grantedAt")
	}
	return nil
}

func (g Grant) Expired(now time.Time) bool {
	return g.ExpiresAt != nil && !now.Before(*g.ExpiresAt)
}

// GrantStore persists reusable rule grants. Lookup is an exact match across
// tenant, subject, rule key, and fingerprint, so a rule grant (no fingerprint)
// and a payload-pinned grant for the same rule are distinct entries.
type GrantStore interface {
	Get(ctx context.Context, namespace Namespace, ruleKey, fingerprint string) (Grant, error)
	Put(ctx context.Context, grant Grant) error
	Revoke(ctx context.Context, namespace Namespace, ruleKey, fingerprint string) error
}

type MemoryGrantStore struct {
	mu     sync.RWMutex
	grants map[grantKey]Grant
	now    func() time.Time
}

type grantKey struct {
	tenant, subject, ruleKey, fingerprint string
}

func NewMemoryGrantStore() *MemoryGrantStore {
	return &MemoryGrantStore{grants: make(map[grantKey]Grant), now: time.Now}
}

func (s *MemoryGrantStore) Get(ctx context.Context, namespace Namespace, ruleKey, fingerprint string) (Grant, error) {
	if err := ctx.Err(); err != nil {
		return Grant{}, err
	}
	key, err := makeGrantKey(namespace, ruleKey, fingerprint)
	if err != nil {
		return Grant{}, err
	}
	if s == nil {
		return Grant{}, ErrGrantNotFound
	}
	s.mu.RLock()
	grant, ok := s.grants[key]
	s.mu.RUnlock()
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	if grant.Expired(s.now().UTC()) {
		s.mu.Lock()
		if current, exists := s.grants[key]; exists && current.Expired(s.now().UTC()) {
			delete(s.grants, key)
		}
		s.mu.Unlock()
		return Grant{}, ErrGrantNotFound
	}
	return cloneGrant(grant), nil
}

func (s *MemoryGrantStore) Put(ctx context.Context, grant Grant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("approval memory grant store is not configured")
	}
	if err := grant.Validate(); err != nil {
		return err
	}
	key, _ := makeGrantKey(grant.Namespace, grant.RuleKey, grant.Fingerprint)
	s.mu.Lock()
	s.grants[key] = cloneGrant(grant)
	s.mu.Unlock()
	return nil
}

func (s *MemoryGrantStore) Revoke(ctx context.Context, namespace Namespace, ruleKey, fingerprint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := makeGrantKey(namespace, ruleKey, fingerprint)
	if err != nil {
		return err
	}
	if s == nil {
		return ErrGrantNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.grants[key]; !ok {
		return ErrGrantNotFound
	}
	delete(s.grants, key)
	return nil
}

func makeGrantKey(namespace Namespace, ruleKey, fingerprint string) (grantKey, error) {
	if err := namespace.Validate(); err != nil {
		return grantKey{}, err
	}
	// The fingerprint is part of the key rather than a required field: a rule
	// grant is looked up by its rule key with no fingerprint, and the empty
	// string is what selects that entry.
	if strings.TrimSpace(ruleKey) == "" {
		return grantKey{}, fmt.Errorf("approval grant ruleKey is required")
	}
	return grantKey{namespace.Tenant, namespace.Subject, ruleKey, fingerprint}, nil
}

func cloneGrant(grant Grant) Grant {
	if grant.ExpiresAt != nil {
		expires := *grant.ExpiresAt
		grant.ExpiresAt = &expires
	}
	return grant
}
