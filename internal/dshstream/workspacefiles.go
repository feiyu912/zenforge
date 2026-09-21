package dshstream

import (
	"context"
	"encoding/json"
)

// This file serves workspaceFiles/changes, the stream a file resource opens before
// it can render anything.
//
// The console's file provider gates on the subscription before it even stats the
// path:
//
//	const notices = changes.follow(sessionId, signal);
//	if (!await notices.ready || aborted()) return;      // before workspaceFiles.stat
//	const first = await stat();
//
// and a stream that fails resolves that ready flag to false, so the resource
// record stays "loading" forever -- the tab opens and never paints (the feed's
// pump() closes on the first failure and the registry has no post-loop state
// change). Upstream's frames are {kind:"ready"} followed by
// {kind:"change", change:{absolutePath, version|absent}}; the ready frame is what
// unblocks rendering, and the change frames are what keep a painted tab current.
//
// This host watches no files, so it sends the ready frame and nothing else. That is
// the honest partial: a file reference renders, and a file the agent later rewrites
// keeps showing its version at open time until the tab is reopened. Sending change
// frames would require a version token this host cannot derive honestly, and a
// fabricated one would make the console repaint on a lie.

// workspaceFileChangesReady is the subscription's opening frame.
type workspaceFileChangesReady struct {
	Kind string `json:"kind"`
}

// runWorkspaceFileChanges serves the workspaceFiles/changes stream.
func (h *Handler) runWorkspaceFileChanges(ctx context.Context, payload json.RawMessage, send func(any) error) error {
	args, failure := endpointArgs(payload)
	if failure != nil {
		return failure
	}
	scope, _, failure := stringArg(args, "workspaceFileScopeId")
	if failure != nil {
		return failure
	}
	if scope == "" {
		return streamFail(codeArgumentsInvalid,
			"workspaceFiles/changes requires a workspaceFileScopeId",
			map[string]any{"argument": "workspaceFileScopeId"})
	}
	// The scope is checked for presence, not for existence. Upstream resolves it to
	// a session object, which exists from the moment a conversation is created;
	// this host's only existence test is whether a turn has started, so refusing a
	// scope here would break the file tree in a conversation that has not been
	// prompted yet -- a strictly narrower answer than upstream's.
	if err := send(workspaceFileChangesReady{Kind: "ready"}); err != nil {
		return err
	}
	// Following no files means there is nothing to wait for but the client leaving.
	<-ctx.Done()
	return ctx.Err()
}
