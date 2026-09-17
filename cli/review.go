package cli

import (
	"fmt"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/review"
)

// buildGuardian wires the independent reviewer. The mode is validated even
// when it is off, so a typo in a config file is an error rather than a
// silently disabled review.
func buildGuardian(opts options, provider model.Model) (*review.Guardian, error) {
	mode, err := review.ParseMode(opts.reviewMode)
	if err != nil {
		return nil, err
	}
	if mode == review.ModeOff {
		return nil, nil
	}
	if provider == nil {
		return nil, fmt.Errorf("--review requires a model")
	}
	return &review.Guardian{
		Reviewer: review.ModelReviewer{Model: provider},
		Mode:     mode,
	}, nil
}
