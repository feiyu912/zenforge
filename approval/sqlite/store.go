package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/approval"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite approval grant path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Get(ctx context.Context, namespace approval.Namespace, ruleKey, fingerprint string) (approval.Grant, error) {
	if err := s.ready(); err != nil {
		return approval.Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := namespace.Validate(); err != nil {
		return approval.Grant{}, err
	}
	var grant approval.Grant
	var grantedAt string
	var expiresAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT action, request_id, granted_at, expires_at
FROM approval_grants
WHERE tenant = ? AND subject = ? AND rule_key = ? AND fingerprint = ?`,
		namespace.Tenant, namespace.Subject, ruleKey, fingerprint).
		Scan(&grant.Action, &grant.RequestID, &grantedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Grant{}, approval.ErrGrantNotFound
	}
	if err != nil {
		return approval.Grant{}, err
	}
	grant.Namespace = namespace
	grant.RuleKey = ruleKey
	grant.Fingerprint = fingerprint
	grant.GrantedAt, err = time.Parse(time.RFC3339Nano, grantedAt)
	if err != nil {
		return approval.Grant{}, err
	}
	if expiresAt.Valid {
		expires, parseErr := time.Parse(time.RFC3339Nano, expiresAt.String)
		if parseErr != nil {
			return approval.Grant{}, parseErr
		}
		grant.ExpiresAt = &expires
	}
	if err := grant.Validate(); err != nil {
		return approval.Grant{}, fmt.Errorf("invalid sqlite approval grant: %w", err)
	}
	if grant.Expired(time.Now().UTC()) {
		_, deleteErr := s.db.ExecContext(ctx, `
DELETE FROM approval_grants
WHERE tenant = ? AND subject = ? AND rule_key = ? AND fingerprint = ?`,
			namespace.Tenant, namespace.Subject, ruleKey, fingerprint)
		if deleteErr != nil {
			return approval.Grant{}, deleteErr
		}
		return approval.Grant{}, approval.ErrGrantNotFound
	}
	return grant, nil
}

func (s *Store) Put(ctx context.Context, grant approval.Grant) error {
	if err := s.ready(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := grant.Validate(); err != nil {
		return err
	}
	var expiresAt any
	if grant.ExpiresAt != nil {
		expiresAt = grant.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO approval_grants
    (tenant, subject, rule_key, fingerprint, action, request_id, granted_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(tenant, subject, rule_key, fingerprint) DO UPDATE SET
    action = excluded.action,
    request_id = excluded.request_id,
    granted_at = excluded.granted_at,
    expires_at = excluded.expires_at`,
		grant.Namespace.Tenant, grant.Namespace.Subject, grant.RuleKey, grant.Fingerprint,
		grant.Action, grant.RequestID, grant.GrantedAt.UTC().Format(time.RFC3339Nano), expiresAt)
	return err
}

func (s *Store) Revoke(ctx context.Context, namespace approval.Namespace, ruleKey, fingerprint string) error {
	if err := s.ready(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := namespace.Validate(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
DELETE FROM approval_grants
WHERE tenant = ? AND subject = ? AND rule_key = ? AND fingerprint = ?`,
		namespace.Tenant, namespace.Subject, ruleKey, fingerprint)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return approval.ErrGrantNotFound
	}
	return nil
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS approval_grants (
    tenant TEXT NOT NULL,
    subject TEXT NOT NULL,
    rule_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    action TEXT NOT NULL,
    request_id TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    expires_at TEXT,
    PRIMARY KEY (tenant, subject, rule_key, fingerprint)
)`)
	return err
}

func (s *Store) ready() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite approval grant store is not open")
	}
	return nil
}
