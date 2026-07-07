package approval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type StoreBrokerOptions struct {
	PollInterval time.Duration
}

func DefaultStoreBrokerOptions() StoreBrokerOptions {
	return StoreBrokerOptions{PollInterval: 100 * time.Millisecond}
}

func (o StoreBrokerOptions) Validate() error {
	if o.PollInterval < 0 {
		return fmt.Errorf("approval store broker poll interval cannot be negative")
	}
	return nil
}

type StoreBroker struct {
	store        PendingStore
	pollInterval time.Duration
}

func NewStoreBroker(store PendingStore, options StoreBrokerOptions) (*StoreBroker, error) {
	if store == nil || nilValue(store) {
		return nil, fmt.Errorf("approval pending store is not configured")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if options.PollInterval == 0 {
		options = DefaultStoreBrokerOptions()
	}
	return &StoreBroker{store: store, pollInterval: options.PollInterval}, nil
}

func (b *StoreBroker) RegisterRequest(ctx context.Context, req Request) error {
	if b == nil || b.store == nil {
		return fmt.Errorf("approval store broker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	return b.store.Register(ctx, req)
}

func (b *StoreBroker) Request(ctx context.Context, req Request) (Decision, error) {
	if b == nil || b.store == nil {
		return Decision{}, fmt.Errorf("approval store broker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if err := b.RegisterRequest(ctx, req); err != nil {
		return Decision{}, err
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		case <-timer.C:
		}

		record, err := b.store.Get(ctx, req.ID)
		if err != nil {
			return Decision{}, err
		}
		if record.Status == StatusResolved {
			return cloneDecision(*record.Decision), nil
		}
		if req.ExpiresAt != nil && !time.Now().Before(*req.ExpiresAt) {
			decision := expiredDecision(req)
			if err := b.store.Resolve(ctx, decision); err == nil {
				return decision, nil
			} else if !errors.Is(err, ErrDecisionConflict) {
				return Decision{}, err
			}
			continue
		}
		timer.Reset(b.pollInterval)
	}
}

func (b *StoreBroker) Lookup(ctx context.Context, requestID string) (Request, error) {
	if b == nil || b.store == nil {
		return Request{}, fmt.Errorf("approval store broker is not configured")
	}
	if err := ctx.Err(); err != nil {
		return Request{}, err
	}
	record, err := b.store.Get(ctx, requestID)
	if err != nil {
		return Request{}, err
	}
	return cloneRequest(record.Request), nil
}

func (b *StoreBroker) List(ctx context.Context, runID string) ([]Request, error) {
	if b == nil || b.store == nil {
		return nil, fmt.Errorf("approval store broker is not configured")
	}
	return b.store.ListPending(ctx, runID)
}

func (b *StoreBroker) Submit(ctx context.Context, decision Decision) error {
	if b == nil || b.store == nil {
		return fmt.Errorf("approval store broker is not configured")
	}
	return b.store.Resolve(ctx, decision)
}

func nilValue(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
