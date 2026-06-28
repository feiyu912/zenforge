package eventlog

import (
	"context"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge"
)

const (
	defaultFollowBuffer       = 128
	defaultFollowReadBatch    = 256
	defaultFollowPollInterval = 100 * time.Millisecond
)

// FollowOptions controls durable replay and the ephemeral live subscription.
type FollowOptions struct {
	LiveBuffer   int
	ReadBatch    int
	PollInterval time.Duration
}

// Follow subscribes before taking a durable watermark, replays through that
// watermark, then drains live notifications while de-duplicating by Seq.
//
// The Store remains the source of truth: live notifications only trigger a
// durable catch-up. Closed or overflowed subscriptions are replaced before a
// new watermark is taken, and polling also discovers appends that bypass Bus.
func Follow(
	ctx context.Context,
	store Store,
	bus *Bus,
	runID string,
	afterSeq int64,
	opts FollowOptions,
) (<-chan zenforge.Event, <-chan error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if store == nil {
		return nil, nil, fmt.Errorf("event log store is required")
	}
	if bus == nil {
		return nil, nil, fmt.Errorf("event bus is required")
	}
	if runID == "" {
		return nil, nil, fmt.Errorf("runID is required")
	}
	if afterSeq < 0 {
		return nil, nil, fmt.Errorf("afterSeq must be non-negative")
	}
	opts, err := normalizeFollowOptions(opts)
	if err != nil {
		return nil, nil, err
	}

	live, unsubscribe, err := bus.Subscribe(ctx, runID, opts.LiveBuffer)
	if err != nil {
		return nil, nil, err
	}
	events := make(chan zenforge.Event)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		if err := follow(ctx, store, bus, runID, afterSeq, opts, live, unsubscribe, events); err != nil &&
			ctx.Err() == nil {
			errs <- err
		}
	}()
	return events, errs, nil
}

func normalizeFollowOptions(opts FollowOptions) (FollowOptions, error) {
	if opts.LiveBuffer == 0 {
		opts.LiveBuffer = defaultFollowBuffer
	}
	if opts.ReadBatch == 0 {
		opts.ReadBatch = defaultFollowReadBatch
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = defaultFollowPollInterval
	}
	if opts.LiveBuffer < 0 {
		return opts, fmt.Errorf("live buffer must be non-negative")
	}
	if opts.ReadBatch < 0 {
		return opts, fmt.Errorf("read batch must be non-negative")
	}
	if opts.PollInterval < 0 {
		return opts, fmt.Errorf("poll interval must be non-negative")
	}
	return opts, nil
}

func follow(
	ctx context.Context,
	store Store,
	bus *Bus,
	runID string,
	afterSeq int64,
	opts FollowOptions,
	live <-chan zenforge.Event,
	unsubscribe func(),
	out chan<- zenforge.Event,
) error {
	defer func() {
		unsubscribe()
	}()
	last := afterSeq
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	for {
		watermark, err := store.LatestSeq(ctx, runID)
		if err != nil {
			return err
		}
		terminal, err := replayThrough(ctx, store, runID, &last, watermark, opts.ReadBatch, out)
		if err != nil || terminal {
			return err
		}
		if watermark <= last {
			terminal, err = terminalAt(ctx, store, runID, watermark)
			if err != nil || terminal {
				return err
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-live:
			if !ok {
				unsubscribe()
				if bus.RunClosed(runID) {
					return nil
				}
				live, unsubscribe, err = bus.Subscribe(ctx, runID, opts.LiveBuffer)
				if err != nil {
					return err
				}
				continue
			}
			if event.Seq <= last {
				continue
			}
			terminal, err = replayThrough(ctx, store, runID, &last, event.Seq, opts.ReadBatch, out)
			if err != nil || terminal {
				return err
			}
		case <-ticker.C:
		}
	}
}

func replayThrough(
	ctx context.Context,
	store Store,
	runID string,
	last *int64,
	watermark int64,
	batch int,
	out chan<- zenforge.Event,
) (bool, error) {
	for *last < watermark {
		events, err := store.Read(ctx, runID, *last, batch)
		if err != nil {
			return false, err
		}
		progressed := false
		for _, event := range events {
			if event.Seq <= *last {
				continue
			}
			if event.Seq > watermark {
				break
			}
			if event.Seq != *last+1 {
				return false, fmt.Errorf("event log sequence gap after %d: got %d", *last, event.Seq)
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case out <- event:
			}
			*last = event.Seq
			progressed = true
			if terminalEvent(event.Type) {
				return true, nil
			}
		}
		if !progressed {
			return false, fmt.Errorf("event log watermark %d is not readable after %d", watermark, *last)
		}
	}
	return false, nil
}

func terminalAt(ctx context.Context, store Store, runID string, seq int64) (bool, error) {
	if seq <= 0 {
		return false, nil
	}
	events, err := store.Read(ctx, runID, seq-1, 1)
	if err != nil {
		return false, err
	}
	return len(events) == 1 && events[0].Seq == seq && terminalEvent(events[0].Type), nil
}
