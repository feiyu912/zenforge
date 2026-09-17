package tool

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// RepeatGuardThresholds mirror the DSH repeat-tool-reminder schedule:
// consecutive identical calls are counted per run and the model receives
// progressively firmer reminders appended to the tool result. The guard
// never blocks or rewrites the real result — polling a background job is
// legitimate — it only makes the loop visible to the model.
var RepeatGuardThresholds = []int{3, 5, 8}

// RepeatGuard returns a Middleware that detects consecutive identical
// tool calls (same run, tool name, and arguments) and appends a reminder
// to the result once the streak reaches the first threshold. Any
// different call in the run resets the streak. Unlike DSH the counter is
// not reset by human input — middleware cannot observe steering messages
// — but a steered conversation virtually always changes the call
// signature anyway.
func RepeatGuard() Middleware {
	var mu sync.Mutex
	type streak struct {
		signature string
		count     int
	}
	streaks := map[string]streak{}
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			runID := call.RunID
			if runID == "" {
				runID = "<unscoped>"
			}
			signature := call.Name + "\x00" + strings.TrimSpace(string(call.Arguments))
			mu.Lock()
			current := streaks[runID]
			if current.signature == signature {
				current.count++
			} else {
				current = streak{signature: signature, count: 1}
			}
			streaks[runID] = current
			mu.Unlock()

			result, err := next.Invoke(ctx, call)
			if current.count < RepeatGuardThresholds[0] {
				return result, err
			}
			reminder := repeatReminder(current.count)
			if result.Output != "" {
				result.Output += "\n\n" + reminder
			} else {
				result.Output = reminder
			}
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			result.Metadata["repeatCount"] = current.count
			return result, err
		})
	}
}

func repeatReminder(count int) string {
	thresholds := RepeatGuardThresholds
	switch {
	case len(thresholds) >= 3 && count >= thresholds[2]:
		return fmt.Sprintf("[repeat-guard: %d consecutive identical calls — stop repeating this exact call; the result will not change. Change strategy: different arguments, a different tool, or report the blocker to the user.]", count)
	case len(thresholds) >= 2 && count >= thresholds[1]:
		return fmt.Sprintf("[repeat-guard: %d consecutive identical calls — repeating the same call is unlikely to help. Try different arguments or another tool.]", count)
	default:
		return fmt.Sprintf("[repeat-guard: %d consecutive identical calls — if you are waiting for a change, the result will not differ; consider a different approach.]", count)
	}
}
