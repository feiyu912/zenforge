package compaction

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/harness"
)

// PruneStats reports the aggregate effect of one pruning pass.
type PruneStats struct {
	// Pruned is the number of tool-result messages rewritten.
	Pruned int
	// CharsRemoved is the total Unicode code points removed.
	CharsRemoved int
}

// PruneToolResults deterministically shrinks oversized tool results inside
// the shadowed range by retaining a head and tail budget around an explicit
// omission marker. Inputs are copied, never mutated. Messages below the
// threshold and all non-tool messages pass through unchanged.
func PruneToolResults(messages []harness.MessageState, policy PrunePolicy) ([]harness.MessageState, PruneStats) {
	out := make([]harness.MessageState, 0, len(messages))
	var stats PruneStats
	if !policy.Enabled {
		return append(out, messages...), stats
	}
	for _, message := range messages {
		if message.Role != "tool" {
			out = append(out, message)
			continue
		}
		pruned, changed := pruneOne(message, policy)
		if changed {
			stats.Pruned++
			stats.CharsRemoved += utf8.RuneCountInString(message.Content) - utf8.RuneCountInString(pruned.Content)
		}
		out = append(out, pruned)
	}
	return out, stats
}

func pruneOne(message harness.MessageState, policy PrunePolicy) (harness.MessageState, bool) {
	content := message.Content
	runes := []rune(content)
	if len(runes) <= policy.ThresholdChars {
		return message, false
	}
	head := string(runes[:policy.HeadChars])
	tail := string(runes[len(runes)-policy.TailChars:])
	omitted := len(runes) - policy.HeadChars - policy.TailChars
	marker := fmt.Sprintf("\n... [%d characters omitted by context compaction; the durable checkpoint retains this pruned form] ...\n", omitted)
	pruned := message
	pruned.Content = head + marker + tail
	meta := map[string]any{}
	for key, value := range message.Meta {
		meta[key] = value
	}
	meta["compaction.pruned"] = map[string]any{
		"charsBefore": len(runes),
		"charsAfter":  utf8.RuneCountInString(pruned.Content),
	}
	pruned.Meta = meta
	return pruned, true
}

// pruneMarkerPrefix recognizes compaction pruning markers in persisted text.
const pruneMarkerFragment = "characters omitted by context compaction"

// HasPruneMarker reports whether text carries a compaction pruning marker.
func HasPruneMarker(text string) bool {
	return strings.Contains(text, pruneMarkerFragment)
}
