// Package dshapi implements the unary half of the upstream console host
// protocol: POST /api/<namespace>/<method>, plus the session namespace the
// console calls first.
//
// It is the counterpart to internal/dshboot. Boot hands the shell a module
// graph; this package answers the RPCs the activated plugins make. Both are
// protocol adapters over what the repository already has, and both are kept
// out of the serve wiring so the caller decides how the route is registered.
// The handler expects the full /api/<namespace>/<method> path, so the caller
// mounts it at /api/ (mux.Handle("/api/", handler)) rather than stripping the
// prefix; a path outside /api/ is a 404, which leaves the static shell route
// unaffected when both share one mux.
//
// The console's "session" is this repository's run. A session summary is a
// RunInfo, a prompt starts or steers a detached run through
// harnesshttp.RunManager, cancel mirrors RunManager.Cancel, and a session's
// history is the durable event log. That mapping is deliberately one-to-one:
// the console's session id is a run id, so a session's identity, status, and
// transcript come from the run manager and the event store rather than from a
// parallel registry invented here.
//
// The wire shapes are copied from docs/dsh-console-protocol-recon.md, whose
// claims cite path:line in the upstream sources. The envelope rules are the
// ones the shipped browser client enforces in
// packages/client/connection/src/client/rpc.ts: the response must carry the
// exact rpcId it was sent, and a failed call is a result.ok:false value at
// HTTP 200, never a 4xx, because the client turns a non-2xx status into a
// transport failure before it ever parses the result.
//
// Everything that cannot be implemented truthfully on top of the run manager
// returns a result.ok:false error with code "unimplemented" and a message that
// names the missing capability. A console panel that shows an error is a
// better failure than one that shows a lie.
package dshapi
