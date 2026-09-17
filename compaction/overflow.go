package compaction

import (
	"errors"
	"strings"

	"github.com/feiyu912/zenforge/model"
)

// overflowMarkers are conservative substrings that identify a canonical
// provider context-overflow failure across OpenAI-compatible and
// Anthropic-compatible endpoints.
var overflowMarkers = []string{
	"context_length_exceeded",
	"maximum context length",
	"prompt is too long",
	"context window",
	"input is too long",
	"too many tokens",
	"reduce the length",
}

// IsOverflowError reports whether a model-call failure is a canonical
// context-window overflow that compaction can recover from. Typed provider
// status errors require a 400/413 status; untyped in-stream provider errors
// are matched conservatively by message text.
func IsOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var status *model.HTTPStatusError
	if errors.As(err, &status) {
		if status.StatusCode != 400 && status.StatusCode != 413 {
			return false
		}
		return matchesOverflowMarker(status.Response + " " + status.Status)
	}
	return matchesOverflowMarker(err.Error())
}

func matchesOverflowMarker(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range overflowMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
