package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/feiyu912/zenforge/approval"
	_ "modernc.org/sqlite"
)

// InboxStore is a durable approval.PendingStore backed by SQLite.
type InboxStore struct {
	db *sql.DB
}

// OpenInbox opens an independently closable durable approval inbox.
func OpenInbox(ctx context.Context, path string) (*InboxStore, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite approval inbox path is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// PRAGMAs are connection-local. A dedicated connection also gives each
	// handle predictable transaction ordering while separate handles contend
	// through SQLite's locking.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &InboxStore{db: db}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *InboxStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *InboxStore) Register(ctx context.Context, request approval.Request) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	requestJSON, err := canonicalJSON(request)
	if err != nil {
		return fmt.Errorf("encode approval request: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var expiresAt any
	if request.ExpiresAt != nil {
		expiresAt = request.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}

	result, err := s.db.ExecContext(ctx, `
INSERT INTO approval_inbox
    (request_id, run_id, request_json, status, created_at, updated_at, expires_at)
VALUES (?, ?, ?, 'pending', ?, ?, ?)
ON CONFLICT(request_id) DO NOTHING`,
		request.ID, request.RunID, string(requestJSON), now, now, expiresAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	record, err := s.Get(ctx, request.ID)
	if err != nil {
		return err
	}
	storedJSON, err := canonicalJSON(record.Request)
	if err != nil {
		return err
	}
	if !bytes.Equal(requestJSON, storedJSON) {
		return approval.ErrRequestConflict
	}
	return nil
}

func (s *InboxStore) Get(ctx context.Context, requestID string) (approval.Record, error) {
	if err := s.ready(); err != nil {
		return approval.Record{}, err
	}
	if err := ctx.Err(); err != nil {
		return approval.Record{}, err
	}
	var requestJSON string
	var decisionJSON sql.NullString
	var status, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT request_json, decision_json, status, created_at, updated_at
FROM approval_inbox
WHERE request_id = ?`, requestID).
		Scan(&requestJSON, &decisionJSON, &status, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Record{}, approval.ErrRequestNotFound
	}
	if err != nil {
		return approval.Record{}, err
	}
	return decodeRecord(requestJSON, decisionJSON, status, createdAt, updatedAt)
}

func (s *InboxStore) ListPending(ctx context.Context, runID string) ([]approval.Request, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := `
SELECT request_json
FROM approval_inbox
WHERE status = 'pending'
  AND (expires_at IS NULL OR expires_at > ?)`
	args := []any{time.Now().UTC().Format(time.RFC3339Nano)}
	if runID != "" {
		query += ` AND run_id = ?`
		args = append(args, runID)
	}
	query += ` ORDER BY request_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	requests := make([]approval.Request, 0)
	for rows.Next() {
		var requestJSON string
		if err := rows.Scan(&requestJSON); err != nil {
			return nil, err
		}
		var request approval.Request
		if err := decodeJSON(requestJSON, &request); err != nil {
			return nil, fmt.Errorf("invalid sqlite approval request: %w", err)
		}
		if err := request.Validate(); err != nil {
			return nil, fmt.Errorf("invalid sqlite approval request: %w", err)
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *InboxStore) Resolve(ctx context.Context, decision approval.Decision) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC()
	decision = normalizeDecision(decision, now)
	if err := decision.Validate(); err != nil {
		return err
	}

	// Serialize resolvers with a write lock before validating the stored
	// request. This makes validation and winner selection one transaction
	// across independently opened handles.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	var requestJSON string
	var storedDecision sql.NullString
	var status, createdAt, updatedAt string
	err = conn.QueryRowContext(ctx, `
SELECT request_json, decision_json, status, created_at, updated_at
FROM approval_inbox
WHERE request_id = ?`, decision.RequestID).
		Scan(&requestJSON, &storedDecision, &status, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.ErrRequestNotFound
	}
	if err != nil {
		return err
	}
	record, err := decodeRecord(requestJSON, storedDecision, status, createdAt, updatedAt)
	if err != nil {
		return err
	}
	if err := approval.ValidateDecisionForRequest(record.Request, decision); err != nil {
		return err
	}
	if record.Status == approval.StatusResolved {
		if decisionsSemanticallyEqual(*record.Decision, decision) {
			_, err = conn.ExecContext(ctx, `COMMIT`)
			committed = err == nil
			return err
		}
		return approval.ErrDecisionConflict
	}
	if record.Request.ExpiresAt != nil &&
		!now.Before(*record.Request.ExpiresAt) &&
		!isExpiredDecision(decision) {
		return approval.ErrRequestExpired
	}
	decisionJSON, err := canonicalJSON(decision)
	if err != nil {
		return fmt.Errorf("encode approval decision: %w", err)
	}
	updated := now.Format(time.RFC3339Nano)
	result, err := conn.ExecContext(ctx, `
UPDATE approval_inbox
SET decision_json = ?, status = 'resolved', updated_at = ?
WHERE request_id = ? AND status = 'pending' AND decision_json IS NULL`,
		string(decisionJSON), updated, decision.RequestID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return approval.ErrDecisionConflict
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *InboxStore) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS approval_inbox (
    request_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    request_json TEXT NOT NULL,
    decision_json TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'resolved')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT
);
CREATE INDEX IF NOT EXISTS approval_inbox_pending
ON approval_inbox(status, run_id, request_id)`)
	return err
}

func (s *InboxStore) ready() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite approval inbox store is not open")
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var clone any
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return json.Marshal(clone)
}

func decodeJSON(data string, out any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON data")
		}
		return err
	}
	return nil
}

func decodeRecord(requestJSON string, decisionJSON sql.NullString, status, createdAt, updatedAt string) (approval.Record, error) {
	var request approval.Request
	if err := decodeJSON(requestJSON, &request); err != nil {
		return approval.Record{}, fmt.Errorf("invalid sqlite approval request: %w", err)
	}
	if err := request.Validate(); err != nil {
		return approval.Record{}, fmt.Errorf("invalid sqlite approval request: %w", err)
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return approval.Record{}, fmt.Errorf("invalid sqlite approval created_at: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return approval.Record{}, fmt.Errorf("invalid sqlite approval updated_at: %w", err)
	}
	record := approval.Record{
		Request: request, Status: approval.RecordStatus(status),
		CreatedAt: created, UpdatedAt: updated,
	}
	if decisionJSON.Valid {
		var decision approval.Decision
		if err := decodeJSON(decisionJSON.String, &decision); err != nil {
			return approval.Record{}, fmt.Errorf("invalid sqlite approval decision: %w", err)
		}
		record.Decision = &decision
	}
	if err := record.Validate(); err != nil {
		return approval.Record{}, fmt.Errorf("invalid sqlite approval record: %w", err)
	}
	return record, nil
}

func normalizeDecision(decision approval.Decision, now time.Time) approval.Decision {
	if decision.Scope == "" {
		decision.Scope = approval.ScopeOnce
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = now
	}
	return decision
}

func decisionsSemanticallyEqual(a, b approval.Decision) bool {
	a.DecidedAt = time.Time{}
	b.DecidedAt = time.Time{}
	aJSON, aErr := canonicalJSON(a)
	bJSON, bErr := canonicalJSON(b)
	return aErr == nil && bErr == nil && bytes.Equal(aJSON, bJSON)
}

func isExpiredDecision(decision approval.Decision) bool {
	return decision.Action == approval.DecisionReject &&
		decision.Scope == approval.ScopeOnce &&
		decision.Reason == approval.ErrorExpired
}

var _ approval.PendingStore = (*InboxStore)(nil)
