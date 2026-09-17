package compaction

import (
	"encoding/json"
	"math"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/model"
)

// Estimator measures heuristic token counts for request components. The
// harness uses estimates for pressure decisions only; provider-reported
// usage remains the billing truth.
type Estimator interface {
	EstimateText(text string) int
	EstimateMessages(messages []harness.MessageState) int
	EstimateTools(tools []model.ToolSpec) int
}

// DefaultCharsPerToken is the fixed heuristic ratio used when no estimator
// is configured. It matches the common 4-characters-per-token rule of thumb.
const DefaultCharsPerToken = 4

// messageOverheadTokens approximates per-message role/framing overhead.
const messageOverheadTokens = 4

// HeuristicEstimator is a deterministic, dependency-free Estimator that
// prices text at a fixed characters-per-token ratio.
type HeuristicEstimator struct {
	CharsPerToken int
}

// NewHeuristicEstimator returns the default fixed-ratio estimator.
func NewHeuristicEstimator() HeuristicEstimator {
	return HeuristicEstimator{CharsPerToken: DefaultCharsPerToken}
}

func (e HeuristicEstimator) ratio() int {
	if e.CharsPerToken <= 0 {
		return DefaultCharsPerToken
	}
	return e.CharsPerToken
}

// EstimateText prices one text value, counting Unicode code points.
func (e HeuristicEstimator) EstimateText(text string) int {
	if text == "" {
		return 0
	}
	runes := utf8.RuneCountInString(text)
	return int(math.Ceil(float64(runes) / float64(e.ratio())))
}

// EstimateMessages prices conversation state including tool-call arguments.
func (e HeuristicEstimator) EstimateMessages(messages []harness.MessageState) int {
	total := 0
	for _, message := range messages {
		total += messageOverheadTokens
		total += e.EstimateText(message.Content)
		total += e.EstimateText(message.Name)
		for _, call := range message.ToolCalls {
			total += e.EstimateText(call.Name)
			total += e.EstimateText(string(call.Arguments))
			total += messageOverheadTokens
		}
	}
	return total
}

// EstimateTools prices the tool declarations of one request.
func (e HeuristicEstimator) EstimateTools(tools []model.ToolSpec) int {
	total := 0
	for _, spec := range tools {
		total += e.EstimateText(spec.Name)
		total += e.EstimateText(spec.Description)
		if len(spec.Schema) > 0 {
			if encoded, err := json.Marshal(spec.Schema); err == nil {
				total += e.EstimateText(string(encoded))
			}
		}
	}
	return total
}
