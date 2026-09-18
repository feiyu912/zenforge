package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/adapters/mcp"
)

// Served-run statuses. `running` is the only non-terminal one; the rest match
// the blocking run tool's vocabulary so one status word means one thing.
const (
	servedRunRunning    = "running"
	servedRunCompleted  = "completed"
	servedRunUnknown    = "unknown"
	servedRunStatusName = "zenforge_run_status"
)

// maxServedRunRecords bounds the registry's memory. Only terminal records are
// evicted, oldest first, so a live run is never forgotten while it runs: the
// durable checkpoint store remains the long-term record.
const maxServedRunRecords = 100

// servedRunState is what the server knows about one run it started.
//
// For a completed run Output is the answer. For any other terminal status
// Message is the human explanation, already carrying the run id, because a
// caller that reads `status` should not have to reassemble the sentence.
type servedRunState struct {
	RunID      string
	Status     string
	Output     string
	Message    string
	Refused    []string
	Detached   bool
	StartedAt  time.Time
	FinishedAt time.Time
}

// servedRunRegistry tracks the runs this server started, so a caller that
// detached one — or that lost the connection to a blocking one — can still ask
// what happened. It is in-process state, deliberately: a served run lives in
// this process, and the durable checkpoint store already answers for runs that
// outlive it.
type servedRunRegistry struct {
	baseCtx    context.Context
	mu         sync.Mutex
	runs       map[string]servedRunState
	order      []string
	cancellers map[string]context.CancelFunc
}

func newServedRunRegistry(baseCtx context.Context) *servedRunRegistry {
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	return &servedRunRegistry{
		baseCtx:    baseCtx,
		runs:       map[string]servedRunState{},
		cancellers: map[string]context.CancelFunc{},
	}
}

// context is the lifetime a detached run is bound to. A detached run must not
// inherit the tool call's context: that call is already over when the run is
// still working.
func (r *servedRunRegistry) context() context.Context {
	return r.baseCtx
}

func (r *servedRunRegistry) start(runID string, cancel context.CancelFunc) error {
	if r == nil {
		if cancel != nil {
			cancel()
		}
		return fmt.Errorf("this server has no run registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runs[runID]; exists {
		if cancel != nil {
			cancel()
		}
		return fmt.Errorf("run %s is already registered", runID)
	}
	r.runs[runID] = servedRunState{
		RunID:     runID,
		Status:    servedRunRunning,
		StartedAt: time.Now().UTC(),
		Detached:  true,
	}
	r.order = append(r.order, runID)
	if cancel != nil {
		r.cancellers[runID] = cancel
	}
	r.evictLocked()
	return nil
}

// finish records a run's terminal state. A run that was never registered —
// which is what a blocking run that failed before start looks like — is
// registered here, because the caller may still ask about it.
func (r *servedRunRegistry) finish(state servedRunState) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.runs[state.RunID]; ok {
		state.StartedAt = existing.StartedAt
		state.Detached = existing.Detached
	} else {
		r.order = append(r.order, state.RunID)
	}
	if state.Status == "" {
		state.Status = servedRunCompleted
	}
	state.FinishedAt = time.Now().UTC()
	r.runs[state.RunID] = state
	delete(r.cancellers, state.RunID)
	r.evictLocked()
}

func (r *servedRunRegistry) get(runID string) (servedRunState, bool) {
	if r == nil {
		return servedRunState{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.runs[runID]
	return state, ok
}

// evictLocked drops the oldest terminal records once the registry is over its
// cap. A live run is never evicted, so the cap can be exceeded while many runs
// are in flight — which the server's single run slot makes impossible in
// practice.
func (r *servedRunRegistry) evictLocked() {
	for len(r.order) > maxServedRunRecords {
		evicted := false
		for index := 0; index < len(r.order); index++ {
			runID := r.order[index]
			state, ok := r.runs[runID]
			if !ok || state.Status == servedRunRunning {
				// A live run is skipped, not treated as a stopping point: one
				// long run at the head of the order must not stop every older
				// terminal record from being evicted.
				continue
			}
			delete(r.runs, runID)
			r.order = append(r.order[:index], r.order[index+1:]...)
			evicted = true
			break
		}
		if !evicted {
			// Only live runs remain, and a live run is never forgotten.
			return
		}
	}
}

// shutdown cancels every live run and waits a bounded time for them to settle,
// so a server does not exit while a detached run is still writing to its
// workspace.
func (r *servedRunRegistry) shutdown(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancellers := make([]context.CancelFunc, 0, len(r.cancellers))
	for _, cancel := range r.cancellers {
		cancellers = append(cancellers, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancellers {
		cancel()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.mu.Lock()
		live := 0
		for _, state := range r.runs {
			if state.Status == servedRunRunning {
				live++
			}
		}
		r.mu.Unlock()
		if live == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// newMCPRunStatusTool reports what is known about one run. It is read-only and
// needs no operator grant: it reports state, it does not create any, and the
// runs it can name are the ones this install already recorded or this server
// already started.
func newMCPRunStatusTool(ctx context.Context, storeType, path string, registry *servedRunRegistry) mcp.ServerTool {
	return mcp.ServerTool{
		Name:        servedRunStatusName,
		Description: "Report what is known about one run by id: the live state of a run this server started (with detach or not), or the durable checkpoint summary of a run this install recorded. Read-only.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runId": map[string]any{
					"type":        "string",
					"description": "The run id to report on, as zenforge_run or zenforge_runs returned it.",
				},
			},
			"required": []string{"runId"},
		},
		ReadOnly: true,
		Handler: func(ctx context.Context, arguments json.RawMessage) (mcp.CallResult, error) {
			var input struct {
				RunID string `json:"runId"`
			}
			if len(arguments) > 0 {
				if err := json.Unmarshal(arguments, &input); err != nil {
					return mcp.CallResult{}, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			runID := strings.TrimSpace(input.RunID)
			if runID == "" {
				return mcp.CallResult{}, fmt.Errorf("runId is required")
			}
			if state, ok := registry.get(runID); ok {
				return servedRunStateResult(state), nil
			}
			summaries, closeStore, err := listRuns(ctx, storeType, path)
			if err != nil {
				return mcp.CallResult{}, err
			}
			defer func() { _ = closeStore() }()
			for _, summary := range summaries {
				if summary.RunID != runID {
					continue
				}
				return mcp.CallResult{
					Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf(
						"run %s is recorded with status %q at phase %q (step %d); this server did not run it, so its live state is not known here",
						summary.RunID, summary.Status, summary.Phase, summary.Step)}},
					StructuredContent: map[string]any{
						"runId":    summary.RunID,
						"status":   summary.Status,
						"phase":    summary.Phase,
						"step":     summary.Step,
						"savedAt":  summary.SavedAt,
						"recorded": true,
					},
				}, nil
			}
			// An id this server does not know is a fact, not a failure: the
			// caller can tell "no such run" from "the status call broke".
			return mcp.CallResult{
				Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf(
					"no run %s in this server's registry or in its recorded runs", runID)}},
				StructuredContent: map[string]any{
					"runId":  runID,
					"status": servedRunUnknown,
				},
			}, nil
		},
	}
}

// servedRunStateResult renders a registry entry. A live run is reported by
// what it is doing; a finished one carries the answer the blocking tool would
// have returned.
func servedRunStateResult(state servedRunState) mcp.CallResult {
	if state.Status == servedRunRunning {
		return mcp.CallResult{
			Content: []mcp.Content{{Type: "text", Text: fmt.Sprintf(
				"run %s is running (started %s)", state.RunID, state.StartedAt.Format(time.RFC3339))}},
			StructuredContent: map[string]any{
				"runId":     state.RunID,
				"status":    state.Status,
				"detached":  state.Detached,
				"startedAt": state.StartedAt,
			},
		}
	}
	structured := map[string]any{
		"runId":      state.RunID,
		"status":     state.Status,
		"detached":   state.Detached,
		"startedAt":  state.StartedAt,
		"finishedAt": state.FinishedAt,
	}
	if state.Status == servedRunCompleted {
		structured["output"] = state.Output
	} else {
		structured["message"] = state.Message
	}
	if len(state.Refused) > 0 {
		structured["refusedToolCalls"] = state.Refused
	}
	text := state.Output
	if state.Status != servedRunCompleted {
		text = state.Message
	}
	return mcp.CallResult{
		Content:           []mcp.Content{{Type: "text", Text: text}},
		StructuredContent: structured,
	}
}
