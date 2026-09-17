package compaction

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

// Reason classifies why one compaction ran. It is persisted in the durable
// CompactionRecord so replay explains every shadowed range.
type Reason string

const (
	// ReasonPressure is automatic step-boundary compaction at the threshold.
	ReasonPressure Reason = "pressure"
	// ReasonOverflow is recovery after a provider context-overflow error.
	ReasonOverflow Reason = "overflow"
	// ReasonManual is an explicit host or command triggered compaction.
	ReasonManual Reason = "manual"
)

func (r Reason) valid() bool {
	switch r {
	case ReasonPressure, ReasonOverflow, ReasonManual:
		return true
	default:
		return false
	}
}

// Config assembles a Compactor. Policy is validated with defaults; a
// Summarizer is required because compaction without one cannot shadow a
// range safely.
type Config struct {
	Policy     Policy
	Summarizer Summarizer
	Estimator  Estimator
}

// Compactor executes context compaction over harness run-state messages.
// It is stateless between calls; durable provenance lives in the returned
// harness.CompactionRecord that the caller persists in run state.
type Compactor struct {
	policy     Policy
	summarizer Summarizer
	estimator  Estimator
}

// New validates the configuration and returns a ready Compactor.
func New(config Config) (*Compactor, error) {
	policy, err := config.Policy.WithDefaults()
	if err != nil {
		return nil, err
	}
	if config.Summarizer == nil {
		return nil, errors.New("compaction summarizer is required")
	}
	estimator := config.Estimator
	if estimator == nil {
		estimator = NewHeuristicEstimator()
	}
	return &Compactor{policy: policy, summarizer: config.Summarizer, estimator: estimator}, nil
}

// Policy returns the resolved policy.
func (c *Compactor) Policy() Policy { return c.policy }

// Estimator returns the resolved estimator.
func (c *Compactor) Estimator() Estimator { return c.estimator }

// Evaluate measures pressure for one prospective model request.
func (c *Compactor) Evaluate(systemTokens int, tools []model.ToolSpec, messages []harness.MessageState) Pressure {
	return Evaluate(c.policy, c.estimator, systemTokens, tools, messages)
}

// Request describes one compaction invocation.
type Request struct {
	// ID optionally pre-assigns the compaction identity so callers can emit
	// lifecycle events before invoking Compact. Empty generates a new ID.
	ID string
	// RunID is the owning run, recorded for correlation.
	RunID string
	// RunInput is the original task input; the summary must carry it.
	RunInput string
	Step     int
	Reason   Reason
	Messages []harness.MessageState
}

// Result carries the rewritten message list and durable provenance.
type Result struct {
	// Messages is the complete replacement history: one summary user
	// message followed by the retained recent context.
	Messages []harness.MessageState
	// Record is the durable provenance entry to append to run state.
	Record harness.CompactionRecord
	// Summary is the summarizer output.
	Summary Summary
	// Prune reports the model-free pruning applied to the shadowed range.
	Prune PruneStats
	// TokensBefore and TokensAfter are conversation-only estimates.
	TokensBefore int
	TokensAfter  int
}

// ErrNothingToCompact reports that no message range can be shadowed, for
// example when retention already covers the whole history.
var ErrNothingToCompact = errors.New("compaction has no shadowable message range")

// ErrNoReduction reports that a compaction would not shrink the estimated
// context. Like the reference harnesses, ZenForge fails such a transaction
// instead of committing a pointless shadow.
var ErrNoReduction = errors.New("compaction did not reduce the estimated context")

// Prune applies the model-free tool-result pruner to a complete history.
// It runs before summarization so a pruning pass alone can bring a run back
// under the pressure threshold. Inputs are copied, never mutated.
func (c *Compactor) Prune(messages []harness.MessageState) ([]harness.MessageState, PruneStats) {
	return PruneToolResults(messages, c.policy.Prune)
}

// Compact shadows the oldest messages outside the recent-context budget and
// replaces them with one summary user message. The cut boundary never
// separates an assistant tool-call turn from its tool results. Overflow
// recovery bypasses the retention budget and keeps only the newest
// well-formed turn group, matching the reference maximal head reduction.
func (c *Compactor) Compact(ctx context.Context, req Request) (*Result, error) {
	if !req.Reason.valid() {
		return nil, fmt.Errorf("unsupported compaction reason %q", req.Reason)
	}
	if len(req.Messages) == 0 {
		return nil, ErrNothingToCompact
	}
	messages, pruneStats := PruneToolResults(req.Messages, c.policy.Prune)
	budget := c.policy.retainBudget()
	if req.Reason == ReasonOverflow {
		budget = 0
	}
	boundary := c.retainBoundary(messages, budget)
	if boundary <= 0 {
		return nil, ErrNothingToCompact
	}
	shadowed := messages[:boundary]
	retained := messages[boundary:]
	summary, err := c.summarizer.Summarize(ctx, SummarizeRequest{
		RunInput:         req.RunInput,
		Messages:         shadowed,
		MaxSummaryTokens: c.policy.MaxSummaryTokens,
	})
	if err != nil {
		return nil, err
	}
	id := req.ID
	if id == "" {
		id = NewCompactionID()
	}
	replacement := harness.MessageState{
		ID:      id,
		Role:    "user",
		Content: SummaryPrefix + "\n\n" + summary.Text,
		Meta: map[string]any{
			"compaction.id":     id,
			"compaction.reason": string(req.Reason),
		},
	}
	out := make([]harness.MessageState, 0, len(retained)+1)
	out = append(out, replacement)
	out = append(out, retained...)
	tokensBefore := c.estimator.EstimateMessages(req.Messages)
	tokensAfter := c.estimator.EstimateMessages(out)
	if tokensAfter >= tokensBefore {
		return nil, fmt.Errorf("%w: before %d tokens, after %d tokens", ErrNoReduction, tokensBefore, tokensAfter)
	}
	record := harness.CompactionRecord{
		ID:              id,
		Step:            req.Step,
		Reason:          string(req.Reason),
		TokensBefore:    tokensBefore,
		TokensAfter:     tokensAfter,
		ShadowedCount:   len(shadowed),
		PrunedResults:   pruneStats.Pruned,
		CharsRemoved:    pruneStats.CharsRemoved,
		SummarizerModel: summary.Model,
		SummaryUsage: harness.UsageState{
			InputTokens:  summary.Usage.PromptTokens,
			OutputTokens: summary.Usage.CompletionTokens,
			TotalTokens:  summary.Usage.TotalTokens,
		},
		CreatedAt: time.Now().UTC(),
	}
	return &Result{
		Messages:     out,
		Record:       record,
		Summary:      summary,
		Prune:        pruneStats,
		TokensBefore: tokensBefore,
		TokensAfter:  tokensAfter,
	}, nil
}

// RetainBoundary returns the index of the first retained message under the
// configured retention budget: messages before it are shadowed, messages
// from it onward are kept. It returns 0 when nothing can be shadowed and
// len(messages) when the whole history is shadowable. The boundary never
// lands on a tool result whose assistant tool-call turn would be shadowed,
// so retained history stays well-formed for both provider protocols.
func (c *Compactor) RetainBoundary(messages []harness.MessageState) int {
	return c.retainBoundary(messages, c.policy.retainBudget())
}

func (c *Compactor) retainBoundary(messages []harness.MessageState, budget int) int {
	if len(messages) == 0 {
		return 0
	}
	boundary := len(messages)
	accumulated := 0
	for i := len(messages) - 1; i >= 0; i-- {
		cost := c.estimator.EstimateMessages(messages[i : i+1])
		if accumulated+cost > budget && boundary < len(messages) {
			break
		}
		accumulated += cost
		boundary = i
	}
	for boundary > 0 && boundary < len(messages) && messages[boundary].Role == "tool" {
		boundary--
	}
	return boundary
}

// NewCompactionID returns a unique durable compaction identity.
func NewCompactionID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("cmpct_%d", time.Now().UnixNano())
	}
	return "cmpct_" + hex.EncodeToString(buffer)
}
