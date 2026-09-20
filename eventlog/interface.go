package eventlog

import (
	"context"

	"github.com/feiyu912/zenforge"
)

type Store interface {
	Append(ctx context.Context, event zenforge.Event) error
	Read(ctx context.Context, runID string, afterSeq int64, limit int) ([]zenforge.Event, error)
	LatestSeq(ctx context.Context, runID string) (int64, error)
}

// RunLister is an optional Store extension: a store that can enumerate the runs
// it holds. A durable listing of runs needs it -- a caller that wants to know
// which conversations survived a restart has no other way to ask -- but a store
// that cannot enumerate is not broken, so an optional interface keeps the
// package's own Store contract unchanged for implementations outside this
// repository.
type RunLister interface {
	Store
	RunIDs(ctx context.Context) ([]string, error)
}
