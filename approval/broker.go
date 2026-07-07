package approval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Broker interface {
	Request(ctx context.Context, req Request) (Decision, error)
}

var ErrRequestNotFound = errors.New("approval request not found")

type BrokerFunc func(context.Context, Request) (Decision, error)

func (f BrokerFunc) Request(ctx context.Context, req Request) (Decision, error) {
	return f(ctx, req)
}

func AlwaysAllow() Broker {
	return BrokerFunc(func(ctx context.Context, req Request) (Decision, error) {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		if err := req.Validate(); err != nil {
			return Decision{}, err
		}
		return Decision{
			RequestID: req.ID,
			Action:    DecisionApprove,
			Scope:     ScopeOnce,
			DecidedAt: time.Now().UTC(),
		}, nil
	})
}

func AlwaysDeny(reason string) Broker {
	return BrokerFunc(func(ctx context.Context, req Request) (Decision, error) {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		if err := req.Validate(); err != nil {
			return Decision{}, err
		}
		return Decision{
			RequestID: req.ID,
			Action:    DecisionReject,
			Scope:     ScopeOnce,
			Reason:    reason,
			DecidedAt: time.Now().UTC(),
		}, nil
	})
}

func WithTimeout(next Broker, timeout time.Duration) Broker {
	return BrokerFunc(func(ctx context.Context, req Request) (Decision, error) {
		if next == nil {
			return Decision{}, fmt.Errorf("approval timeout broker is not configured")
		}
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		if err := req.Validate(); err != nil {
			return Decision{}, err
		}
		deadline := time.Time{}
		if timeout > 0 {
			deadline = time.Now().Add(timeout)
		}
		if req.ExpiresAt != nil && (deadline.IsZero() || req.ExpiresAt.Before(deadline)) {
			deadline = *req.ExpiresAt
		}
		if deadline.IsZero() {
			return next.Request(ctx, req)
		}
		if !deadline.After(time.Now()) {
			return expiredDecision(req), nil
		}
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		decision, err := next.Request(waitCtx, req)
		if err == nil {
			return decision, nil
		}
		if ctx.Err() != nil {
			return Decision{}, ctx.Err()
		}
		if waitCtx.Err() == context.DeadlineExceeded {
			return expiredDecision(req), nil
		}
		return Decision{}, err
	})
}

func expiredDecision(req Request) Decision {
	return Decision{
		RequestID: req.ID,
		Action:    DecisionReject,
		Scope:     ScopeOnce,
		Reason:    ErrorExpired,
		DecidedAt: time.Now().UTC(),
	}
}

type ChannelBroker struct {
	Requests  chan<- Request
	Decisions <-chan Decision
}

func NewChannelBroker(requests chan<- Request, decisions <-chan Decision) ChannelBroker {
	return ChannelBroker{Requests: requests, Decisions: decisions}
}

func (b ChannelBroker) Request(ctx context.Context, req Request) (Decision, error) {
	if b.Requests == nil || b.Decisions == nil {
		return Decision{}, fmt.Errorf("approval channel broker is not configured")
	}
	if err := req.Validate(); err != nil {
		return Decision{}, err
	}
	select {
	case b.Requests <- cloneRequest(req):
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
	select {
	case decision, ok := <-b.Decisions:
		if !ok {
			return Decision{}, fmt.Errorf("approval decision channel is closed")
		}
		decision = normalizeDecision(decision)
		if err := ValidateDecisionForRequest(req, decision); err != nil {
			return Decision{}, err
		}
		return cloneDecision(decision), nil
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
}

type PendingBroker struct {
	mu       sync.Mutex
	pending  map[string]pendingRequest
	requests chan Request
}

type pendingRequest struct {
	req      Request
	decision chan Decision
}

func NewPendingBroker(buffer int) *PendingBroker {
	if buffer < 0 {
		buffer = 0
	}
	return &PendingBroker{
		pending:  make(map[string]pendingRequest),
		requests: make(chan Request, buffer),
	}
}

func (b *PendingBroker) Requests() <-chan Request {
	if b == nil {
		return nil
	}
	return b.requests
}

func (b *PendingBroker) Request(ctx context.Context, req Request) (Decision, error) {
	if b == nil {
		return Decision{}, fmt.Errorf("approval pending broker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if err := req.Validate(); err != nil {
		return Decision{}, err
	}
	waiting := pendingRequest{
		req:      cloneRequest(req),
		decision: make(chan Decision, 1),
	}
	b.mu.Lock()
	if _, exists := b.pending[req.ID]; exists {
		b.mu.Unlock()
		return Decision{}, fmt.Errorf("approval request %q is already pending", req.ID)
	}
	b.pending[req.ID] = waiting
	b.mu.Unlock()

	select {
	case b.requests <- cloneRequest(req):
	default:
	}

	select {
	case decision := <-waiting.decision:
		return cloneDecision(decision), nil
	case <-ctx.Done():
		if b.remove(req.ID, waiting) {
			return Decision{}, ctx.Err()
		}
		return cloneDecision(<-waiting.decision), nil
	}
}

func (b *PendingBroker) Submit(ctx context.Context, decision Decision) error {
	if b == nil {
		return fmt.Errorf("approval pending broker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	decision = normalizeDecision(decision)
	if err := decision.Validate(); err != nil {
		return err
	}

	b.mu.Lock()
	waiting, ok := b.pending[decision.RequestID]
	if !ok {
		b.mu.Unlock()
		return ErrRequestNotFound
	}
	if err := ValidateDecisionForRequest(waiting.req, decision); err != nil {
		b.mu.Unlock()
		return err
	}
	delete(b.pending, decision.RequestID)
	b.mu.Unlock()

	waiting.decision <- cloneDecision(decision)
	return nil
}

func (b *PendingBroker) Pending(requestID string) (Request, bool) {
	if b == nil || requestID == "" {
		return Request{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	waiting, ok := b.pending[requestID]
	return cloneRequest(waiting.req), ok
}

func (b *PendingBroker) Lookup(ctx context.Context, requestID string) (Request, error) {
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	req, ok := b.Pending(requestID)
	if !ok {
		return Request{}, ErrRequestNotFound
	}
	return req, nil
}

func (b *PendingBroker) List(ctx context.Context, runID string) ([]Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runID == "" {
		return b.ListPending(), nil
	}
	return b.ListPendingForRun(runID), nil
}

func (b *PendingBroker) ListPending() []Request {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.pending))
	for _, waiting := range b.pending {
		out = append(out, cloneRequest(waiting.req))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func (b *PendingBroker) ListPendingForRun(runID string) []Request {
	if b == nil || runID == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.pending))
	for _, waiting := range b.pending {
		if waiting.req.RunID == runID {
			out = append(out, cloneRequest(waiting.req))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func (b *PendingBroker) remove(requestID string, waiting pendingRequest) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	current, ok := b.pending[requestID]
	if ok && current.decision == waiting.decision {
		delete(b.pending, requestID)
		return true
	}
	return false
}

func normalizeDecision(decision Decision) Decision {
	if decision.Scope == "" {
		decision.Scope = ScopeOnce
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	}
	return decision
}
