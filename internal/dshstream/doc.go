// Package dshstream implements the transport-streams half of the upstream
// console host protocol: the WebSocket mux at GET /api/remote.mux with its
// three logical streams ($events, session/control, session/follow) and the
// out-of-band approval answer route POST /api/$events/result.
//
// It is the counterpart to internal/dshapi (the unary half) and
// internal/dshboot (the boot half). Boot hands the shell a module graph, the
// unary handler answers the RPCs the activated plugins make, and this package
// carries everything that is not a request/response pair: approvals, the
// host-wide control baseline, and a run's durable event tail. Like both of
// them it is a protocol adapter over what the repository already has, and it
// deliberately does not mount itself: the caller decides where the two routes
// live.
//
// The console's "session" is this repository's run, exactly as in
// internal/dshapi. A session/follow snapshot is the durable event log read
// through harnesshttp.RunManager and eventlog, subsequent frames are the same
// log's appends, and an approval waterfall is an entry in approval.Inbox.
//
// The wire shapes are copied from docs/dsh-console-protocol-recon.md, whose
// claims cite path:line in the upstream sources, and were re-checked against
// packages/api/gateway/src/stream-protocol.ts, stream-server.ts, index.ts and
// packages/api/session-controller/src/{history.ts,types.ts} at revision
// ddefc45. Two properties of that client are worth stating here because they
// drive the implementation:
//
//   - Mux frames are exact-key. The shipped client parses every host frame
//     with parseRemoteStreamServerMessage and closes the socket with 4002 on
//     anything else, so an extra or missing key is a hang, not a warning. The
//     frame structs in wire.go therefore carry no fields the protocol does not
//     define.
//   - session/follow is strictly ordered. The client rejects an event whose
//     seq is not exactly the previous cursor + 1 and rejects a snapshot whose
//     last record is not the snapshot cursor. The snapshot is therefore a
//     contiguous suffix of the durable log ending at that log's tail, and the
//     live tail is taken from eventlog.Follow through
//     RunManager.Attach, which guarantees gap-free delivery.
//
// Anything the console wants that this repository cannot supply truthfully is
// reported in the package's own doc comments and nowhere fabricated: the
// session/control baseline is the legal empty one because the run manager has
// no background-job model, and no assistant-stream frames are minted because
// the harness's streamed model output is durable model.delta events rather
// than the console's process-local revision/index protocol.
package dshstream
