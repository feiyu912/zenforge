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
