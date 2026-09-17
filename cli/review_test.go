package cli

import (
	"context"
	"flag"
	"testing"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/review"
)

// scriptedReviewAdapter satisfies the model interface for wiring tests.
type scriptedReviewAdapter struct{}

func (scriptedReviewAdapter) Generate(context.Context, model.Request) (*model.Response, error) {
	return &model.Response{}, nil
}

func (scriptedReviewAdapter) Stream(context.Context, model.Request) (<-chan model.Event, error) {
	return nil, nil
}

func TestBuildGuardianModes(t *testing.T) {
	guardian, err := buildGuardian(defaultOptions(), nil)
	if err != nil || guardian != nil {
		t.Fatalf("buildGuardian = %#v, %v", guardian, err)
	}
	for name, want := range map[string]review.Mode{"report": review.ModeReport, "enforce": review.ModeEnforce} {
		opts := defaultOptions()
		opts.reviewMode = name
		guardian, err := buildGuardian(opts, &scriptedReviewAdapter{})
		if err != nil {
			t.Fatalf("buildGuardian(%q) returned error: %v", name, err)
		}
		if guardian.Mode != want || guardian.Reviewer == nil {
			t.Fatalf("guardian = %#v", guardian)
		}
	}
	// A typo must be an error even though the reviewer is optional, or a
	// config file could disable review silently.
	opts := defaultOptions()
	opts.reviewMode = "strictly"
	if _, err := buildGuardian(opts, &scriptedReviewAdapter{}); err == nil {
		t.Fatal("an unknown review mode was accepted")
	}
	opts.reviewMode = "enforce"
	if _, err := buildGuardian(opts, nil); err == nil {
		t.Fatal("review without a model was accepted")
	}
}

func TestReviewFlagIsBound(t *testing.T) {
	opts := defaultOptions()
	fs := flag.NewFlagSet("review-test", flag.ContinueOnError)
	bindOptions(fs, &opts)
	if err := fs.Parse([]string{"--review", "enforce"}); err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if opts.reviewMode != "enforce" {
		t.Fatalf("review mode = %q", opts.reviewMode)
	}
}
