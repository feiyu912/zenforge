package memory

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

type Store struct {
	mu      sync.Mutex
	records map[string]approval.Record
	now     func() time.Time
}

func NewStore() *Store {
	return NewStoreWithClock(time.Now)
}

func NewStoreWithClock(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{records: make(map[string]approval.Record), now: now}
}

func (s *Store) Register(ctx context.Context, req approval.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	req = cloneRequest(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[req.ID]; ok {
		if reflect.DeepEqual(existing.Request, req) {
			return nil
		}
		return approval.ErrRequestConflict
	}
	now := s.now().UTC()
	s.records[req.ID] = approval.Record{
		Request: req, Status: approval.StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	return nil
}

func (s *Store) Get(ctx context.Context, requestID string) (approval.Record, error) {
	if err := ctx.Err(); err != nil {
		return approval.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[requestID]
	if !ok {
		return approval.Record{}, approval.ErrRequestNotFound
	}
	return cloneRecord(record), nil
}

func (s *Store) ListPending(ctx context.Context, runID string) ([]approval.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]approval.Request, 0)
	for _, record := range s.records {
		req := record.Request
		if record.Status != approval.StatusPending || (runID != "" && req.RunID != runID) {
			continue
		}
		if req.ExpiresAt != nil && !now.Before(*req.ExpiresAt) {
			continue
		}
		out = append(out, cloneRequest(req))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) Resolve(ctx context.Context, decision approval.Decision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	decision = normalizeDecision(decision, s.now)
	if err := decision.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[decision.RequestID]
	if !ok {
		return approval.ErrRequestNotFound
	}
	if err := approval.ValidateDecisionForRequest(record.Request, decision); err != nil {
		return err
	}
	if record.Status == approval.StatusResolved {
		if decisionsEqual(*record.Decision, decision) {
			return nil
		}
		return approval.ErrDecisionConflict
	}
	if record.Request.ExpiresAt != nil && !s.now().Before(*record.Request.ExpiresAt) &&
		!isExpiredDecision(decision) {
		return approval.ErrRequestExpired
	}
	stored := cloneDecision(decision)
	record.Decision = &stored
	record.Status = approval.StatusResolved
	record.UpdatedAt = s.now().UTC()
	s.records[decision.RequestID] = record
	return nil
}

func decisionsEqual(a, b approval.Decision) bool {
	a.DecidedAt = time.Time{}
	b.DecidedAt = time.Time{}
	return reflect.DeepEqual(a, b)
}

func isExpiredDecision(d approval.Decision) bool {
	return d.Action == approval.DecisionReject && d.Scope == approval.ScopeOnce &&
		d.Reason == approval.ErrorExpired
}

func normalizeDecision(d approval.Decision, now func() time.Time) approval.Decision {
	if d.Scope == "" {
		d.Scope = approval.ScopeOnce
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = now().UTC()
	}
	return d
}

func cloneRecord(r approval.Record) approval.Record {
	r.Request = cloneRequest(r.Request)
	if r.Decision != nil {
		d := cloneDecision(*r.Decision)
		r.Decision = &d
	}
	return r
}

func cloneRequest(r approval.Request) approval.Request {
	r.Options = append([]approval.Option(nil), r.Options...)
	r.Payload = cloneMap(r.Payload)
	if r.ExpiresAt != nil {
		v := *r.ExpiresAt
		r.ExpiresAt = &v
	}
	return r
}

func cloneDecision(d approval.Decision) approval.Decision {
	d.Payload = cloneMap(d.Payload)
	return d
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch value := v.(type) {
		case map[string]any:
			out[k] = cloneMap(value)
		case []any:
			items := make([]any, len(value))
			for i, item := range value {
				if nested, ok := item.(map[string]any); ok {
					items[i] = cloneMap(nested)
				} else {
					items[i] = item
				}
			}
			out[k] = items
		case []string:
			out[k] = append([]string(nil), value...)
		default:
			out[k] = value
		}
	}
	return out
}

var _ approval.PendingStore = (*Store)(nil)
