package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Summary struct {
	RunID   string    `json:"runId"`
	Seq     int64     `json:"seq"`
	Phase   string    `json:"phase"`
	Status  string    `json:"status"`
	Step    int       `json:"step"`
	SavedAt time.Time `json:"savedAt"`
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite checkpoint path is required")
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

func (s *Store) Save(ctx context.Context, cp checkpoint.Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if err := checkpoint.Validate(cp); err != nil {
		return err
	}

	encoded, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	savedAt := cp.SavedAt.UTC().Format(time.RFC3339Nano)
	phase := string(cp.State.Phase)
	status := string(cp.State.Control.Status)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
INSERT INTO latest_checkpoints (run_id, seq, phase, status, step, saved_at, checkpoint_json)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
    seq = excluded.seq,
    phase = excluded.phase,
    status = excluded.status,
    step = excluded.step,
    saved_at = excluded.saved_at,
    checkpoint_json = excluded.checkpoint_json
WHERE excluded.seq > latest_checkpoints.seq`,
		cp.RunID, cp.Seq, phase, status, cp.State.Step, savedAt, encoded)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: runId %q got seq %d", checkpoint.ErrStaleCheckpoint, cp.RunID, cp.Seq)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO checkpoints (run_id, seq, saved_at, checkpoint_json)
VALUES (?, ?, ?, ?)`,
		cp.RunID, cp.Seq, savedAt, encoded); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Load(ctx context.Context, runID string) (*checkpoint.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, checkpoint.ErrNotFound
	}

	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT checkpoint_json FROM latest_checkpoints WHERE run_id = ?`, runID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, checkpoint.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeCheckpoint(raw)
}

func (s *Store) Delete(ctx context.Context, runID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if runID == "" {
		return checkpoint.ErrNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM latest_checkpoints WHERE run_id = ?`, runID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return checkpoint.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM checkpoints WHERE run_id = ?`, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) List(ctx context.Context) ([]Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT run_id, seq, phase, status, step, saved_at
FROM latest_checkpoints
ORDER BY saved_at DESC, run_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Summary
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var summary Summary
		var savedAt string
		if err := rows.Scan(&summary.RunID, &summary.Seq, &summary.Phase, &summary.Status, &summary.Step, &savedAt); err != nil {
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, savedAt)
		if err != nil {
			return nil, err
		}
		summary.SavedAt = parsed
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS checkpoints (
    run_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    saved_at TEXT NOT NULL,
    checkpoint_json BLOB NOT NULL,
    PRIMARY KEY (run_id, seq)
);
CREATE TABLE IF NOT EXISTS latest_checkpoints (
    run_id TEXT PRIMARY KEY,
    seq INTEGER NOT NULL,
    phase TEXT NOT NULL,
    status TEXT NOT NULL,
    step INTEGER NOT NULL,
    saved_at TEXT NOT NULL,
    checkpoint_json BLOB NOT NULL
)`)
	return err
}

func (s *Store) ready() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite checkpoint store is not open")
	}
	return nil
}

func decodeCheckpoint(raw []byte) (*checkpoint.Checkpoint, error) {
	var cp checkpoint.Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, err
	}
	if err := checkpoint.ValidateForLoad(cp); err != nil {
		return nil, err
	}
	return &cp, nil
}
