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
		if timeout <= 0 {
			return next.Request(ctx, req)
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		decision, err := next.Request(waitCtx, req)
		if err == nil {
			return decision, nil
		}
		if waitCtx.Err() == context.DeadlineExceeded {
			return Decision{
				RequestID: req.ID,
				Action:    DecisionReject,
				Scope:     ScopeOnce,
				Reason:    ErrorExpired,
				DecidedAt: time.Now().UTC(),
			}, nil
		}
		return Decision{}, err
	})
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
	case b.Requests <- req:
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
	select {
	case decision := <-b.Decisions:
		if decision.RequestID == "" {
			decision.RequestID = req.ID
		}
		if decision.DecidedAt.IsZero() {
			decision.DecidedAt = time.Now().UTC()
		}
		return decision, decision.Validate()
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
		req:      req,
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
	case b.requests <- req:
	default:
	}

	select {
	case decision := <-waiting.decision:
		return decision, nil
	case <-ctx.Done():
		b.remove(req.ID, waiting)
		return Decision{}, ctx.Err()
	}
}

func (b *PendingBroker) Submit(ctx context.Context, decision Decision) error {
	if b == nil {
		return fmt.Errorf("approval pending broker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if decision.Scope == "" {
		decision.Scope = ScopeOnce
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = time.Now().UTC()
	}
	if err := decision.Validate(); err != nil {
		return err
	}

	b.mu.Lock()
	waiting, ok := b.pending[decision.RequestID]
	if ok {
		delete(b.pending, decision.RequestID)
	}
	b.mu.Unlock()
	if !ok {
		return ErrRequestNotFound
	}

	waiting.decision <- decision
	return nil
}

func (b *PendingBroker) Pending(requestID string) (Request, bool) {
	if b == nil || requestID == "" {
		return Request{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	waiting, ok := b.pending[requestID]
	return waiting.req, ok
}

func (b *PendingBroker) ListPending() []Request {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.pending))
	for _, waiting := range b.pending {
		out = append(out, waiting.req)
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
			out = append(out, waiting.req)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func (b *PendingBroker) remove(requestID string, waiting pendingRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	current, ok := b.pending[requestID]
	if ok && current.decision == waiting.decision {
		delete(b.pending, requestID)
	}
}
