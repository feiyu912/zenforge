// Package diff renders line-based unified diffs in the classic
// git/patch format (three lines of context, @@ hunk headers). It backs
// the turn-diff tracker, which mirrors codex's per-turn TurnDiff
// reporting, and any later review tooling that needs to show what a
// run changed.
//
// The Myers shortest-edit-script algorithm is used with a hard line
// budget: inputs whose combined line count exceeds MaxDiffLines fall
// back to common prefix/suffix trimming plus a single replace hunk, so
// diffing never becomes quadratic-time or quadratic-memory on huge
// generated files.
package diff

import (
	"fmt"
	"strings"
)

// MaxDiffLines is the combined old+new line count above which the
// precise Myers pass is replaced by the coarse prefix/suffix fallback.
const MaxDiffLines = 4000

// ContextLines is the number of unchanged lines rendered around each
// change group, matching the unified-diff convention.
const ContextLines = 3

type opKind int

const (
	opEqual opKind = iota
	opDelete
	opInsert
)

type op struct {
	kind opKind
	line string
}

// Unified renders the unified diff from oldText to newText under the
// given labels. It returns an empty string when the texts are equal.
func Unified(oldText, newText, oldLabel, newLabel string) string {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	ops := diffLines(oldLines, newLines)
	if !hasChange(ops) {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n", labelOr(oldLabel, "old"))
	fmt.Fprintf(&out, "+++ %s\n", labelOr(newLabel, "new"))
	for _, hunk := range hunks(ops, ContextLines) {
		out.WriteString(hunk)
	}
	return out.String()
}

func labelOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func hasChange(ops []op) bool {
	for _, current := range ops {
		if current.kind != opEqual {
			return true
		}
	}
	return false
}

// splitLines splits on newline boundaries. A trailing newline does not
// produce a final empty line, matching how diff tools treat complete
// files.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(text, "\n")
	return strings.Split(trimmed, "\n")
}

func diffLines(a, b []string) []op {
	if len(a)+len(b) > MaxDiffLines {
		return coarseDiff(a, b)
	}
	return myersDiff(a, b)
}

// coarseDiff trims the common prefix and suffix and renders everything
// between as one delete+insert group.
func coarseDiff(a, b []string) []op {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix &&
		a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	ops := make([]op, 0, len(a)+len(b))
	for _, line := range a[:prefix] {
		ops = append(ops, op{kind: opEqual, line: line})
	}
	for _, line := range a[prefix : len(a)-suffix] {
		ops = append(ops, op{kind: opDelete, line: line})
	}
	for _, line := range b[prefix : len(b)-suffix] {
		ops = append(ops, op{kind: opInsert, line: line})
	}
	for _, line := range a[len(a)-suffix:] {
		ops = append(ops, op{kind: opEqual, line: line})
	}
	return ops
}

// maxMyersEditDistance bounds the shortest-edit-script search: files
// differing by more edits than this render through the coarse
// prefix/suffix fallback instead, keeping trace memory bounded.
const maxMyersEditDistance = 1500

// myersDiff computes the shortest edit script with the classic Myers
// O(ND) algorithm and a per-round trace replay (Coglan formulation).
func myersDiff(a, b []string) []op {
	n, m := len(a), len(b)
	max := n + m
	if max == 0 {
		return nil
	}
	offset := max
	v := make([]int, 2*max+1)
	var trace [][]int
	found := -1
	limit := max
	if limit > maxMyersEditDistance {
		limit = maxMyersEditDistance
	}
	for d := 0; d <= limit; d++ {
		// Snapshot only the diagonals that can carry meaningful values
		// at the start of round d: k in [-d, d].
		snapshot := make([]int, 2*d+1)
		copy(snapshot, v[offset-d:offset+d+1])
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if found >= 0 {
			break
		}
	}
	if found < 0 {
		return coarseDiff(a, b)
	}

	// Backtrack through the recorded trace to rebuild the edit script.
	var reversed []op
	x, y := n, m
	for d := found; d >= 0; d-- {
		vd := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && traceValue(vd, d, k-1) < traceValue(vd, d, k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := traceValue(vd, d, prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			reversed = append(reversed, op{kind: opEqual, line: a[x]})
		}
		if d > 0 {
			if x == prevX {
				y--
				reversed = append(reversed, op{kind: opInsert, line: b[y]})
			} else {
				x--
				reversed = append(reversed, op{kind: opDelete, line: a[x]})
			}
		}
	}
	ops := make([]op, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		ops = append(ops, reversed[i])
	}
	return ops
}

// traceValue reads diagonal k from the round-d window snapshot. Values
// outside the window were never written by the forward pass and read as
// zero, matching the full-array zero initialization of the classic
// algorithm.
func traceValue(window []int, d, k int) int {
	idx := k + d
	if idx < 0 || idx >= len(window) {
		return 0
	}
	return window[idx]
}

// hunks groups ops into unified-diff hunks with the requested context.
func hunks(ops []op, context int) []string {
	type indexed struct {
		pos  int
		kind opKind
	}
	var changes []int
	for i, current := range ops {
		if current.kind != opEqual {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return nil
	}

	// Group changes separated by more than 2*context equal lines.
	groups := [][2]int{{changes[0], changes[0]}}
	for _, idx := range changes[1:] {
		last := &groups[len(groups)-1]
		gap := 0
		for j := last[1] + 1; j < idx; j++ {
			gap++
		}
		if gap > 2*context {
			groups = append(groups, [2]int{idx, idx})
		} else {
			last[1] = idx
		}
	}

	var out []string
	for _, group := range groups {
		start := group[0] - context
		if start < 0 {
			start = 0
		}
		end := group[1] + context
		if end > len(ops)-1 {
			end = len(ops) - 1
		}
		oldStart, oldCount, newStart, newCount := 1, 0, 1, 0
		// Line numbers count consumed old/new lines before the hunk.
		for i := 0; i < start; i++ {
			switch ops[i].kind {
			case opEqual:
				oldStart++
				newStart++
			case opDelete:
				oldStart++
			case opInsert:
				newStart++
			}
		}
		var body strings.Builder
		for i := start; i <= end; i++ {
			switch ops[i].kind {
			case opEqual:
				body.WriteString(" " + ops[i].line + "\n")
				oldCount++
				newCount++
			case opDelete:
				body.WriteString("-" + ops[i].line + "\n")
				oldCount++
			case opInsert:
				body.WriteString("+" + ops[i].line + "\n")
				newCount++
			}
		}
		// An empty side starts at line 0, matching patch conventions
		// (@@ -0,0 +1,N @@ for created files).
		if oldCount == 0 {
			oldStart = 0
		}
		if newCount == 0 {
			newStart = 0
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@\n%s", oldStart, oldCount, newStart, newCount, body.String()))
	}
	return out
}
