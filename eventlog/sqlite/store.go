package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/feiyu912/zenforge"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite event log path is required")
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

func (s *Store) Append(ctx context.Context, event zenforge.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	runID := event.RunID()

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	latest, err := latestSeq(ctx, tx, runID)
	if err != nil {
		return err
	}
	next := zenforge.NextEventSeq(latest)
	if event.Seq == 0 {
		event.Seq = next
	}
	if event.Seq != next {
		return fmt.Errorf("event seq must be %d, got %d", next, event.Seq)
	}
	if err := event.ValidatePersisted(); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO events (run_id, seq, type, timestamp, event_json)
VALUES (?, ?, ?, ?, ?)`,
		runID, event.Seq, string(event.Type), event.Timestamp, encoded); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	if runID == "" {
		return nil, fmt.Errorf("runID is required")
	}

	query := `SELECT event_json FROM events WHERE run_id = ? AND seq > ? ORDER BY seq`
	args := []any{runID, afterSeq}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []zenforge.Event
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event zenforge.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		if err := event.ValidatePersisted(); err != nil {
			return nil, err
		}
		if event.RunID() != runID {
			return nil, fmt.Errorf("read event: runID mismatch %q", event.RunID())
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) LatestSeq(ctx context.Context, runID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.ready(); err != nil {
		return 0, err
	}
	if runID == "" {
		return 0, fmt.Errorf("runID is required")
	}
	return latestSeq(ctx, s.db, runID)
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS events (
    run_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    type TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    event_json BLOB NOT NULL,
    PRIMARY KEY (run_id, seq)
)`)
	return err
}

func (s *Store) ready() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite event log store is not open")
	}
	return nil
}

func latestSeq(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, runID string) (int64, error) {
	var latest sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT MAX(seq) FROM events WHERE run_id = ?`, runID).Scan(&latest)
	if err != nil {
		return 0, err
	}
	if !latest.Valid {
		return 0, nil
	}
	return latest.Int64, nil
}
