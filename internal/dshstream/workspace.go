package dshstream

import (
	"context"
	"sync"
)

// WorkspaceView is one registered console workspace. It mirrors upstream's
// WorkspaceView field for field
// (api/workspace-controller/lib/types/types.d.ts:12-24): the console's
// Workspace browser renders these rows directly, so a missing or renamed field
// is a blank column, not a hidden one.
type WorkspaceView struct {
	WorkspaceID string   `json:"workspaceId"`
	Path        string   `json:"path"`
	Title       string   `json:"title"`
	SessionIDs  []string `json:"sessionIds"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// WorkspaceBaseline is the value of the follow stream's first frame
// (types.d.ts WorkspaceBaseline): every registered row plus the archived
// session set, so a reconnecting client rebuilds the whole surface from one
// frame.
type WorkspaceBaseline struct {
	Items              []WorkspaceView `json:"items"`
	ArchivedSessionIDs []string        `json:"archivedSessionIds"`
}

// WorkspaceUpdate is one registry change delivered after the baseline. Kind
// selects which upstream increment frame it becomes: "upsert", "remove",
// "order" or "archived".
type WorkspaceUpdate struct {
	Kind               string
	Workspace          *WorkspaceView
	WorkspaceID        string
	WorkspaceIDs       []string
	ArchivedSessionIDs []string
}

// Error codes the workspace registry reports by name. The first three mirror
// upstream's published detail codes
// (api/workspace-controller/lib/types/types.d.ts RemoteErrorDetailsMap);
// WorkspaceCodeRootImmutable is this host's own, explained where it is raised.
const (
	WorkspaceCodeInvalidPath  = "workspace/invalid-path"
	WorkspaceCodeNameConflict = "workspace/name-conflict"
	WorkspaceCodeNotFound     = "workspace/not-found"
	// WorkspaceCodeMoveInvalid is upstream's detail code for a manual-order move
	// whose session or anchor is not accounted to the workspace
	// (api/workspace-controller/lib/types/types.d.ts RemoteErrorDetailsMap).
	WorkspaceCodeMoveInvalid   = "workspace/move-invalid"
	WorkspaceCodeRootImmutable = "workspace/root-immutable"
)

// WorkspaceError is a registry refusal carrying the protocol's detail code
// (workspace/invalid-path, workspace/name-conflict, ...), so the unary
// namespace can hand the console the same code upstream's host does instead of
// a generic failure. The registry returns it, the RPC layer maps it, and the
// stream layer never needs it.
type WorkspaceError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *WorkspaceError) Error() string { return e.Message }

type workspaceBaselineFrame struct {
	Type  string            `json:"type"`
	Value WorkspaceBaseline `json:"value"`
}

type workspaceUpsertFrame struct {
	Type      string        `json:"type"`
	Workspace WorkspaceView `json:"workspace"`
}

type workspaceRemoveFrame struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspaceId"`
}

type workspaceOrderFrame struct {
	Type         string   `json:"type"`
	WorkspaceIDs []string `json:"workspaceIds"`
}

type workspaceArchivedFrame struct {
	Type               string   `json:"type"`
	ArchivedSessionIDs []string `json:"archivedSessionIds"`
}

// runWorkspaceFollow serves the workspace/follow logical stream: exactly one
// baseline frame, then one frame per registry change
// (api/workspace-controller/src/client/index.ts opens it through the snapshot
// stream, which refuses any increment that arrives before a baseline).
//
// The stream stays open after the baseline for the same reason session/control
// does: the client treats an end after the baseline as a lost carrier and
// reconnects, so ending early would spin it. It ends only when the client
// cancels it or the socket closes.
func (h *Handler) runWorkspaceFollow(ctx context.Context, payload []byte, send func(any) error) error {
	args, failure := endpointArgs(payload)
	if failure != nil {
		return failure
	}
	if !emptyArgs(args) {
		return streamFail(codeArgumentsInvalid,
			"the workspace/follow stream takes no arguments",
			map[string]any{"endpoint": "workspace/follow"})
	}
	// Subscription and baseline are read in that order, so a change that lands
	// while the baseline is assembled arrives as a frame instead of falling
	// into the gap between the two. The reverse overlap -- a change visible in
	// both -- is harmless: the client applies the later frame over the row it
	// already has.
	var (
		mu     sync.Mutex
		queue  []WorkspaceUpdate
		signal = make(chan struct{}, 1)
	)
	var unsubscribe func()
	if h.cfg.WorkspaceUpdates != nil {
		unsubscribe = h.cfg.WorkspaceUpdates(func(update WorkspaceUpdate) {
			mu.Lock()
			// Registrations are few and changes are rare, so the queue holds
			// every change rather than coalescing: a coalesced "order" or
			// "remove" is a different state, not a fresher value of the same
			// one, and the client's own store applies them in sequence.
			queue = append(queue, update)
			mu.Unlock()
			select {
			case signal <- struct{}{}:
			default:
			}
		})
		defer unsubscribe()
	}
	baseline := WorkspaceBaseline{Items: []WorkspaceView{}, ArchivedSessionIDs: []string{}}
	if h.cfg.Workspaces != nil {
		baseline = h.cfg.Workspaces()
	}
	if baseline.Items == nil {
		baseline.Items = []WorkspaceView{}
	}
	if baseline.ArchivedSessionIDs == nil {
		baseline.ArchivedSessionIDs = []string{}
	}
	if err := send(workspaceBaselineFrame{Type: "baseline", Value: baseline}); err != nil {
		return err
	}
	if unsubscribe == nil {
		// Nothing can ever change a registry this host does not have, and the
		// client still expects a live carrier: hold the stream open until it
		// goes away.
		<-ctx.Done()
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signal:
		}
		for {
			mu.Lock()
			if len(queue) == 0 {
				mu.Unlock()
				break
			}
			update := queue[0]
			queue = queue[1:]
			mu.Unlock()
			if err := send(workspaceFrame(update)); err != nil {
				return err
			}
		}
	}
}

// workspaceFrame renders one update as the increment frame the client's store
// understands.
func workspaceFrame(update WorkspaceUpdate) any {
	switch update.Kind {
	case "upsert":
		view := WorkspaceView{}
		if update.Workspace != nil {
			view = *update.Workspace
		}
		return workspaceUpsertFrame{Type: "upsert", Workspace: view}
	case "remove":
		return workspaceRemoveFrame{Type: "remove", WorkspaceID: update.WorkspaceID}
	case "order":
		ids := update.WorkspaceIDs
		if ids == nil {
			ids = []string{}
		}
		return workspaceOrderFrame{Type: "order", WorkspaceIDs: ids}
	default:
		ids := update.ArchivedSessionIDs
		if ids == nil {
			ids = []string{}
		}
		return workspaceArchivedFrame{Type: "archived", ArchivedSessionIDs: ids}
	}
}
