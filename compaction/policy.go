package compaction

import (
	"errors"
	"fmt"

	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

// Policy defaults mirror the reference harnesses: compact at 80% of the
// model window and retain roughly 16% of the window as recent context.
const (
	DefaultThresholdRatio     = 0.8
	DefaultRetainRatio        = 0.16
	DefaultMaxSummaryTokens   = 8192
	DefaultCompactionRetries  = 1
	DefaultMaxOverflowRetries = 1

	DefaultPruneThresholdChars = 8192
	DefaultPruneHeadChars      = 4096
	DefaultPruneTailChars      = 1024
)

// PrunePolicy configures model-free tool-result pruning inside the shadowed
// range. Pruning bounds the summarizer input and keeps durable checkpoints
// small; it never rewrites the retained recent context.
type PrunePolicy struct {
	Enabled        bool
	ThresholdChars int
	HeadChars      int
	TailChars      int
}

// Policy configures when and how compaction runs. Zero values take the
// documented defaults through WithDefaults; invalid combinations fail.
type Policy struct {
	// ContextWindow is the model context window in tokens. Automatic
	// pressure compaction requires a positive window.
	ContextWindow int
	// ThresholdRatio compacts when the estimated request reaches this
	// fraction of ContextWindow. Defaults to 0.8.
	ThresholdRatio float64
	// RetainRatio is the recent-context budget as a fraction of
	// ContextWindow. Mutually exclusive with RetainTokens. Defaults to 0.16.
	RetainRatio float64
	// RetainTokens is an absolute recent-context budget in tokens.
	RetainTokens int
	// MaxSummaryTokens caps summary generation. Defaults to 8192.
	MaxSummaryTokens int
	// CompactionRetries are extra compaction attempts while pressure stays
	// above the threshold after one compaction. Defaults to 1.
	CompactionRetries int
	// MaxOverflowRetries bounds recovery attempts after a provider reports
	// a canonical context overflow. Defaults to 1; 0 disables recovery.
	MaxOverflowRetries int
	// Prune configures tool-result pruning of the shadowed range.
	Prune PrunePolicy
}

// WithDefaults validates the policy and fills zero fields with defaults.
// RetainRatio and RetainTokens are mutually exclusive; setting both fails.
func (p Policy) WithDefaults() (Policy, error) {
	if p.ContextWindow < 0 {
		return Policy{}, errors.New("compaction policy contextWindow must be non-negative")
	}
	if p.ThresholdRatio == 0 {
		p.ThresholdRatio = DefaultThresholdRatio
	}
	if p.ThresholdRatio <= 0 || p.ThresholdRatio > 1 {
		return Policy{}, fmt.Errorf("compaction policy thresholdRatio %v must be in (0, 1]", p.ThresholdRatio)
	}
	if p.RetainRatio != 0 && p.RetainTokens != 0 {
		return Policy{}, errors.New("compaction policy retainRatio and retainTokens are mutually exclusive")
	}
	if p.RetainRatio == 0 && p.RetainTokens == 0 {
		p.RetainRatio = DefaultRetainRatio
	}
	if p.RetainRatio < 0 || p.RetainRatio >= 1 {
		return Policy{}, fmt.Errorf("compaction policy retainRatio %v must be in (0, 1)", p.RetainRatio)
	}
	if p.RetainTokens < 0 {
		return Policy{}, errors.New("compaction policy retainTokens must be non-negative")
	}
	if p.MaxSummaryTokens == 0 {
		p.MaxSummaryTokens = DefaultMaxSummaryTokens
	}
	if p.MaxSummaryTokens < 0 {
		return Policy{}, errors.New("compaction policy maxSummaryTokens must be non-negative")
	}
	if p.CompactionRetries == 0 {
		p.CompactionRetries = DefaultCompactionRetries
	}
	if p.CompactionRetries < 0 {
		return Policy{}, errors.New("compaction policy compactionRetries must be non-negative")
	}
	if p.MaxOverflowRetries == 0 {
		p.MaxOverflowRetries = DefaultMaxOverflowRetries
	}
	if p.MaxOverflowRetries < 0 {
		return Policy{}, errors.New("compaction policy maxOverflowRetries must be non-negative")
	}
	if p.Prune.Enabled {
		if p.Prune.ThresholdChars == 0 {
			p.Prune.ThresholdChars = DefaultPruneThresholdChars
		}
		if p.Prune.HeadChars == 0 {
			p.Prune.HeadChars = DefaultPruneHeadChars
		}
		if p.Prune.TailChars == 0 {
			p.Prune.TailChars = DefaultPruneTailChars
		}
		if p.Prune.ThresholdChars < 0 || p.Prune.HeadChars < 0 || p.Prune.TailChars < 0 {
			return Policy{}, errors.New("compaction prune policy values must be non-negative")
		}
		if p.Prune.HeadChars+p.Prune.TailChars >= p.Prune.ThresholdChars {
			return Policy{}, fmt.Errorf("compaction prune head %d + tail %d must be below threshold %d",
				p.Prune.HeadChars, p.Prune.TailChars, p.Prune.ThresholdChars)
		}
	}
	return p, nil
}

// retainBudget resolves the recent-context budget in tokens.
func (p Policy) retainBudget() int {
	if p.RetainTokens > 0 {
		return p.RetainTokens
	}
	return int(float64(p.ContextWindow) * p.RetainRatio)
}

// thresholdTokens is the estimated-request size that triggers compaction.
func (p Policy) thresholdTokens() int {
	return int(float64(p.ContextWindow) * p.ThresholdRatio)
}

// Pressure is one context-window measurement of a prospective model request.
type Pressure struct {
	// WindowTokens is the configured model context window; 0 when unset.
	WindowTokens int
	// Estimated is the heuristic token estimate of the whole request:
	// system material, tool declarations, and conversation messages.
	Estimated int
	// MessagesTokens is the conversation-only component of Estimated.
	MessagesTokens int
	// Ratio is Estimated / WindowTokens; 0 when no window is configured.
	Ratio float64
	// Threshold is the configured compaction threshold ratio.
	Threshold float64
	// ShouldCompact reports whether automatic compaction must run before
	// the next model call.
	ShouldCompact bool
}

// Evaluate measures one prospective model request against the policy.
// Without a positive ContextWindow no automatic compaction is ever due, but
// forced compaction (overflow recovery, manual) remains available.
func Evaluate(policy Policy, estimator Estimator, systemTokens int, tools []model.ToolSpec, messages []harness.MessageState) Pressure {
	if estimator == nil {
		estimator = NewHeuristicEstimator()
	}
	messagesTokens := estimator.EstimateMessages(messages)
	toolsTokens := estimator.EstimateTools(tools)
	estimated := systemTokens + toolsTokens + messagesTokens
	pressure := Pressure{
		WindowTokens:   policy.ContextWindow,
		Estimated:      estimated,
		MessagesTokens: messagesTokens,
		Threshold:      policy.ThresholdRatio,
	}
	if policy.ContextWindow > 0 {
		pressure.Ratio = float64(estimated) / float64(policy.ContextWindow)
		pressure.ShouldCompact = estimated >= policy.thresholdTokens()
	}
	return pressure
}
