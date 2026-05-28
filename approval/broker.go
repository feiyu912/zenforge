package approval

import (
	"context"
	"fmt"
	"time"
)

type Broker interface {
	Request(ctx context.Context, req Request) (Decision, error)
}

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
