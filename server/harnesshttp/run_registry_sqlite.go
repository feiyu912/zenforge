package harnesshttp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteRunRegistry is a durable RunRegistry backed by SQLite.
type SQLiteRunRegistry struct {
	db *sql.DB
}

func OpenSQLiteRunRegistry(ctx context.Context, path string) (*SQLiteRunRegistry, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite run registry path is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	registry := &SQLiteRunRegistry{db: db}
	if err := registry.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return registry, nil
}

func (r *SQLiteRunRegistry) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *SQLiteRunRegistry) Claim(ctx context.Context, claim RunClaim) (RunLease, error) {
	if err := r.ready(); err != nil {
		return RunLease{}, err
	}
	if err := ctx.Err(); err != nil {
		return RunLease{}, err
	}
	if err := validateRunClaim(claim); err != nil {
		return RunLease{}, err
	}
	leaseToken := randomLeaseToken()
	res, err := r.db.ExecContext(ctx, `
INSERT INTO detached_runs
    (run_id, status, error, owner_id, lease_token, lease_until, started_at, updated_at, finished_at, cancel_requested)
VALUES (?, ?, '', ?, ?, ?, ?, ?, '', 0)
ON CONFLICT(run_id) DO UPDATE SET
    status = excluded.status,
    error = '',
    owner_id = excluded.owner_id,
    lease_token = excluded.lease_token,
    lease_until = excluded.lease_until,
    started_at = excluded.started_at,
    updated_at = excluded.updated_at,
    finished_at = '',
    cancel_requested = CASE WHEN ? THEN detached_runs.cancel_requested ELSE 0 END
WHERE detached_runs.finished_at != ''
   OR detached_runs.lease_until <= ?`,
		claim.RunID, string(claim.Status), claim.OwnerID, leaseToken,
		formatTime(claim.LeaseUntil), formatTime(claim.StartedAt), formatTime(claim.UpdatedAt),
		claim.Resume,
		formatTime(time.Now().UTC()))
	if err != nil {
		return RunLease{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return RunLease{}, err
	}
	if affected == 0 {
		return RunLease{}, ErrRunClaimed
	}
	return RunLease{RunID: claim.RunID, OwnerID: claim.OwnerID, Token: leaseToken}, nil
}

func (r *SQLiteRunRegistry) RequestCancel(ctx context.Context, runID string) error {
	if err := r.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ErrInvalidRunID
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE detached_runs SET cancel_requested = 1 WHERE run_id = ? AND finished_at = ''`, runID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	info, err := r.Get(ctx, runID)
	if err != nil {
		return err
	}
	if info.Status == RunCancelled {
		return nil
	}
	return ErrRunTerminal
}

func (r *SQLiteRunRegistry) CancelRequested(ctx context.Context, lease RunLease) (bool, error) {
	if err := r.ready(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateRunLease(lease); err != nil {
		return false, err
	}
	var requested int
	err := r.db.QueryRowContext(ctx, `
SELECT cancel_requested FROM detached_runs
WHERE run_id = ? AND owner_id = ? AND lease_token = ? AND finished_at = ''`,
		lease.RunID, lease.OwnerID, lease.Token).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrRunLeaseLost
	}
	if err != nil {
		return false, err
	}
	return requested != 0, nil
}

func (r *SQLiteRunRegistry) Update(ctx context.Context, lease RunLease, info RunInfo) error {
	if err := r.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRunLease(lease); err != nil {
		return err
	}
	if info.LeaseUntil == nil {
		return fmt.Errorf("run leaseUntil is required")
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE detached_runs
SET status = ?, error = ?, lease_until = ?, updated_at = ?
WHERE run_id = ?
  AND owner_id = ?
  AND lease_token = ?
  AND finished_at = ''`,
		string(info.Status), info.Error, formatTime(*info.LeaseUntil), formatTime(info.UpdatedAt),
		lease.RunID, lease.OwnerID, lease.Token)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrRunLeaseLost
	}
	return nil
}

func (r *SQLiteRunRegistry) Release(ctx context.Context, lease RunLease, info RunInfo) error {
	if err := r.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRunLease(lease); err != nil {
		return err
	}
	if !terminal(info.Status) {
		return fmt.Errorf("release status must be terminal")
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE detached_runs
SET status = ?, error = ?, lease_token = '', lease_until = '', updated_at = ?, finished_at = ?
WHERE run_id = ?
  AND owner_id = ?
  AND lease_token = ?
  AND finished_at = ''`,
		string(info.Status), info.Error, formatTime(info.UpdatedAt), formatTime(info.FinishedAt),
		lease.RunID, lease.OwnerID, lease.Token)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrRunLeaseLost
	}
	return nil
}

func (r *SQLiteRunRegistry) Get(ctx context.Context, runID string) (RunInfo, error) {
	if err := r.ready(); err != nil {
		return RunInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return RunInfo{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return RunInfo{}, ErrInvalidRunID
	}
	info, _, ok, err := selectRunRegistryRecord(ctx, r.db, runID)
	if err != nil {
		return RunInfo{}, err
	}
	if !ok {
		return RunInfo{}, ErrRunNotFound
	}
	return info, nil
}

func (r *SQLiteRunRegistry) List(ctx context.Context) ([]RunInfo, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT run_id, status, error, owner_id, lease_token, lease_until, started_at, updated_at, finished_at
FROM detached_runs
ORDER BY updated_at DESC, run_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RunInfo
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := scanRunRegistryRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *SQLiteRunRegistry) init(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS detached_runs (
    run_id TEXT PRIMARY KEY,
    status TEXT NOT NULL,
    error TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    lease_token TEXT NOT NULL,
    lease_until TEXT NOT NULL,
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    cancel_requested INTEGER NOT NULL DEFAULT 0
)`)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `ALTER TABLE detached_runs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return err
	}
	return nil
}

func (r *SQLiteRunRegistry) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("sqlite run registry is not open")
	}
	return nil
}

func selectRunRegistryRecord(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID string) (RunInfo, string, bool, error) {
	var status, errorText, ownerID, token, leaseUntil, startedAt, updatedAt, finishedAt string
	err := q.QueryRowContext(ctx, `
SELECT status, error, owner_id, lease_token, lease_until, started_at, updated_at, finished_at
FROM detached_runs
WHERE run_id = ?`, runID).
		Scan(&status, &errorText, &ownerID, &token, &leaseUntil, &startedAt, &updatedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunInfo{}, "", false, nil
	}
	if err != nil {
		return RunInfo{}, "", false, err
	}
	info, err := runInfoFromRegistryFields(runID, status, errorText, ownerID, leaseUntil, startedAt, updatedAt, finishedAt)
	if err != nil {
		return RunInfo{}, "", false, err
	}
	return info, token, true, nil
}

type runRegistryScanner interface {
	Scan(dest ...any) error
}

func scanRunRegistryRecord(scanner runRegistryScanner) (RunInfo, error) {
	var runID, status, errorText, ownerID, token, leaseUntil, startedAt, updatedAt, finishedAt string
	if err := scanner.Scan(&runID, &status, &errorText, &ownerID, &token, &leaseUntil, &startedAt, &updatedAt, &finishedAt); err != nil {
		return RunInfo{}, err
	}
	return runInfoFromRegistryFields(runID, status, errorText, ownerID, leaseUntil, startedAt, updatedAt, finishedAt)
}

func runInfoFromRegistryFields(runID, status, errorText, ownerID, leaseUntil, startedAt, updatedAt, finishedAt string) (RunInfo, error) {
	info := RunInfo{RunID: runID, Status: RunStatus(status), Error: errorText, OwnerID: ownerID}
	if leaseUntil != "" {
		parsed, err := time.Parse(time.RFC3339Nano, leaseUntil)
		if err != nil {
			return RunInfo{}, err
		}
		info.LeaseUntil = &parsed
	}
	parsed, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return RunInfo{}, err
	}
	info.StartedAt = parsed
	parsed, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return RunInfo{}, err
	}
	info.UpdatedAt = parsed
	if finishedAt != "" {
		parsed, err = time.Parse(time.RFC3339Nano, finishedAt)
		if err != nil {
			return RunInfo{}, err
		}
		info.FinishedAt = parsed
	}
	return info, nil
}

func validateRunLease(lease RunLease) error {
	if strings.TrimSpace(lease.RunID) == "" || strings.TrimSpace(lease.OwnerID) == "" || strings.TrimSpace(lease.Token) == "" {
		return ErrRunLeaseLost
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

var _ RunRegistry = (*SQLiteRunRegistry)(nil)
var _ RunRegistryLister = (*SQLiteRunRegistry)(nil)
var _ RunCancellationRegistry = (*SQLiteRunRegistry)(nil)
