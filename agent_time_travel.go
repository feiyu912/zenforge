package zenforge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/feiyu912/zenforge/checkpoint"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/trace"
)

// Time-travel support: forking a run from a past checkpoint and
// reverting a run to one. Both are append-only at the log level — a fork
// starts a child log whose first event is `run.started`, and a revert
// appends a `run.reverted` marker instead of truncating history — so an
// audit trail is never rewritten.

const (
	// MetaForkParentRun records the parent run id on a forked state.
	MetaForkParentRun = "zenforge.fork.parent_run_id"
	// MetaForkParentSeq records the parent checkpoint sequence a fork
	// branched from.
	MetaForkParentSeq = "zenforge.fork.parent_seq"
	// MetaRevertedFromSeq records the log sequence a revert rewound to.
	MetaRevertedFromSeq = "zenforge.revert.from_seq"
	// MetaRevertedMarkerSeq records the sequence of the marker event that
	// authorized the revert.
	MetaRevertedMarkerSeq = "zenforge.revert.marker_seq"
)

// Fork creates a new run whose conversation and tool state start from
// the newest checkpoint of parentRunID at or below atSeq (zero selects
// the latest), then continues the run. The child log begins with its own
// `run.started`, so the "run.started is the first persisted event"
// invariant holds, and the child state carries the parent lineage in
// ParentRunID and Meta.
func (a *Agent) Fork(ctx context.Context, parentRunID string, atSeq int64) (string, <-chan Event, error) {
	if a.configErr != nil {
		return "", nil, a.configErr
	}
	if a.config.Checkpoints == nil {
		return "", nil, fmt.Errorf("checkpoint store is not configured")
	}
	if parentRunID == "" {
		return "", nil, fmt.Errorf("fork requires a parent run id")
	}
	parent, err := checkpoint.LoadAt(ctx, a.config.Checkpoints, parentRunID, atSeq)
	if err != nil {
		return "", nil, err
	}
	if parent == nil {
		return "", nil, fmt.Errorf("checkpoint store returned nil checkpoint for runId %q", parentRunID)
	}
	if err := checkpoint.ValidateForLoad(*parent); err != nil {
		return "", nil, err
	}
	if err := a.validateCheckpointSkills(parent.State); err != nil {
		return "", nil, err
	}

	state, err := cloneRunState(parent.State)
	if err != nil {
		return "", nil, err
	}
	childRunID := newRunID()
	state.RunID = childRunID
	state.ParentRunID = parentRunID
	state.UpdatedAt = time.Now().UTC()
	// A fork always resumes work, so a terminal parent state becomes an
	// active child state; a waiting approval does not carry over because
	// its request id names the parent run.
	state.Phase = harness.RunPhaseCreated
	state.Approval = harness.ApprovalState{}
	if state.Meta == nil {
		state.Meta = map[string]any{}
	}
	state.Meta[MetaForkParentRun] = parentRunID
	state.Meta[MetaForkParentSeq] = parent.Seq

	if err := a.openRunControl(childRunID); err != nil {
		return "", nil, err
	}
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer a.closeRunControl(childRunID)
		// The child's first persisted event is its own run.started, which
		// both satisfies the log invariant and records the lineage.
		if _, err := a.record(ctx, EventRunStarted, childRunID, map[string]any{
			"input":         state.Input,
			"forkedFrom":    parentRunID,
			"forkedFromSeq": parent.Seq,
		}); err != nil {
			return
		}
		if a.config.Checkpoints != nil {
			seq, err := a.latestEventSeq(ctx, childRunID)
			if err != nil {
				return
			}
			if err := a.config.Checkpoints.Save(ctx, checkpoint.Checkpoint{
				Version: checkpoint.CheckpointVersion,
				RunID:   childRunID,
				Seq:     seq,
				State:   state,
				SavedAt: time.Now().UTC(),
			}); err != nil {
				return
			}
		}
		if AgentMode(state.Mode) == ModePlanExecute || isPlanExecuteState(state) {
			a.runPlanExecute(ctx, events, childRunID, Task{
				Input: planExecuteOriginalInput(state),
				Meta:  planExecuteUserMeta(state.Meta),
			}, &state)
			return
		}
		a.runLoop(ctx, events, state, true)
	}()
	return childRunID, events, nil
}

// Revert rewinds a run to the newest checkpoint at or below toSeq. It
// appends a `run.reverted` marker and saves the rewound state as the
// run's newest checkpoint, so a following resume continues from there
// while the log keeps every event that led to the abandoned branch.
func (a *Agent) Revert(ctx context.Context, runID string, toSeq int64) (Event, error) {
	if a.configErr != nil {
		return Event{}, a.configErr
	}
	return RevertRun(ctx, TimeTravelStores{
		Checkpoints: a.config.Checkpoints,
		Events:      a.config.Events,
		Trace:       a.config.Trace,
	}, runID, toSeq)
}

// TimeTravelStores are the stores a store-level time-travel operation
// needs. A revert touches no model and no tool, so hosts can rewind a run
// without constructing an Agent.
type TimeTravelStores struct {
	Checkpoints checkpoint.Store
	Events      EventStore
	Trace       trace.Sink
}

// RevertRun rewinds runID to the newest checkpoint at or below toSeq.
func RevertRun(ctx context.Context, stores TimeTravelStores, runID string, toSeq int64) (Event, error) {
	if stores.Checkpoints == nil {
		return Event{}, fmt.Errorf("checkpoint store is not configured")
	}
	if runID == "" {
		return Event{}, fmt.Errorf("revert requires a run id")
	}
	cp, err := checkpoint.LoadAt(ctx, stores.Checkpoints, runID, toSeq)
	if err != nil {
		return Event{}, err
	}
	if cp == nil {
		return Event{}, fmt.Errorf("checkpoint store returned nil checkpoint for runId %q", runID)
	}
	if err := checkpoint.ValidateForLoad(*cp); err != nil {
		return Event{}, err
	}
	state, err := cloneRunState(cp.State)
	if err != nil {
		return Event{}, err
	}
	marker, err := recordEvent(ctx, stores.Events, stores.Trace, EventRunReverted, runID, map[string]any{
		"toSeq":         toSeq,
		"checkpointSeq": cp.Seq,
	})
	if err != nil {
		return Event{}, err
	}
	state.UpdatedAt = time.Now().UTC()
	if state.Meta == nil {
		state.Meta = map[string]any{}
	}
	state.Meta[MetaRevertedFromSeq] = cp.Seq
	state.Meta[MetaRevertedMarkerSeq] = marker.Seq
	if err := stores.Checkpoints.Save(ctx, checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   runID,
		Seq:     marker.Seq,
		State:   state,
		SavedAt: time.Now().UTC(),
	}); err != nil {
		return Event{}, err
	}
	return marker, nil
}

// RevertedSeq reports the sequence a state was reverted to, if any.
func RevertedSeq(state harness.RunState) (int64, bool) {
	return metaInt64(state.Meta, MetaRevertedFromSeq)
}

// ForkedFrom reports the parent run and sequence of a forked state.
func ForkedFrom(state harness.RunState) (string, int64, bool) {
	parent, ok := state.Meta[MetaForkParentRun].(string)
	if !ok || parent == "" {
		return "", 0, false
	}
	seq, _ := metaInt64(state.Meta, MetaForkParentSeq)
	return parent, seq, true
}

// record persists and traces an event without a delivery channel, for
// lifecycle operations that happen outside a running loop.
func (a *Agent) record(ctx context.Context, eventType EventType, runID string, data map[string]any) (Event, error) {
	return recordEvent(ctx, a.config.Events, a.config.Trace, eventType, runID, data)
}

// recordEvent persists and optionally traces one event.
func recordEvent(ctx context.Context, events EventStore, sink trace.Sink, eventType EventType, runID string, data map[string]any) (Event, error) {
	event := NewEvent(eventType, runID, data)
	persistCtx := ctx
	if ctx.Err() != nil {
		persistCtx = context.WithoutCancel(ctx)
	}
	if events != nil {
		latest, err := events.LatestSeq(persistCtx, runID)
		if err != nil {
			return Event{}, fmt.Errorf("load latest event sequence: %w", err)
		}
		event = event.WithSeq(NextEventSeq(latest))
		if err := events.Append(persistCtx, event); err != nil {
			return Event{}, fmt.Errorf("append event %s: %w", eventType, err)
		}
	}
	if sink != nil {
		_ = sink.Emit(persistCtx, traceEvent(event))
	}
	return event, nil
}

// latestEventSeq returns the newest persisted sequence for a run, or 0
// when the log is empty.
func (a *Agent) latestEventSeq(ctx context.Context, runID string) (int64, error) {
	if a.config.Events == nil {
		return 0, nil
	}
	latest, err := a.config.Events.LatestSeq(ctx, runID)
	if err != nil {
		return 0, err
	}
	return latest, nil
}

// cloneRunState deep-copies a state through JSON so a fork or revert
// cannot alias the stored checkpoint it was derived from.
func cloneRunState(state harness.RunState) (harness.RunState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return harness.RunState{}, err
	}
	var cloned harness.RunState
	if err := json.Unmarshal(data, &cloned); err != nil {
		return harness.RunState{}, err
	}
	return cloned, nil
}

func metaInt64(meta map[string]any, key string) (int64, bool) {
	if meta == nil {
		return 0, false
	}
	switch value := meta[key].(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case float64:
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
