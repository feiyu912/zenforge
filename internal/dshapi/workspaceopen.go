package dshapi

import (
	"context"
	"encoding/json"
)

// The console declares two methods about the *host desktop*: one question and one
// operation.
//
// `session/canOpenWorkspacePath` reports whether this deployment can hand a
// session workspace path to a native desktop, and `session/openWorkspacePath`
// performs it -- a path, with `action: "reveal"` for file-manager navigation and
// omission for the default application, opened on the machine serving the console
// (api/session-controller/lib/types.d.ts:334-343, lib/index.d.ts:95-119).
//
// Neither has a caller in the pinned bundle, and that is the point: the caller
// upstream is the *desktop carrier* (`dshDesktopBoot`, apps/web/src/main.ts:4-34),
// which this host does not implement -- the protocol recon already declared it an
// explicit non-goal (docs/dsh-console-protocol-recon.md: "Desktop/worker carrier
// ... not needed"). So the answer splits in two: a capability *question* has a
// truthful answer, and an *operation* this host cannot perform is refused by name
// instead of being left as a 404 or faked by spawning something (ADR 0126).

// workspaceDesktopReason is the one sentence this family's two halves have to
// agree on: the probe answers `false` because of it, and the operation refuses
// with it. Writing it once is what keeps the machine-readable answer and the
// human-readable refusal from drifting apart.
const workspaceDesktopReason = "this host serves the console in a browser and has no desktop carrier to open a path on; workspaceFiles/list and workspaceFiles/read show a file inside the session instead"

// sessionCanOpenWorkspacePath answers the desktop capability question with a bare
// boolean. The declared result is `boolean()`, not an object, so `false` is the
// shape and not a failure: a caller branches on it, and an RPC error would read
// as "this host is broken" instead of "this host has no desktop", which is the
// difference between a disabled button and a broken page. Like every zero-argument
// method here it keeps the upstream exact-arguments rule.
func (h *Handler) sessionCanOpenWorkspacePath(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments("session/canOpenWorkspacePath", args); failure != nil {
		return nil, failure
	}
	return false, nil
}

// sessionOpenWorkspacePath refuses the desktop operation by name. The request's
// own fields are declared (`path`, and `action` when the caller asked for a
// file-manager reveal) so a typo is still reported as the typo it is; what is
// missing is not an argument but the host's desktop, so a *valid* request is
// refused too and the details name the capability rather than the request.
func (h *Handler) sessionOpenWorkspacePath(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnknownArguments(args, "path", "action"); failure != nil {
		return nil, failure
	}
	return nil, fail(codeUnimplemented, workspaceDesktopReason,
		map[string]any{"capability": "a desktop carrier to open a path on"})
}
