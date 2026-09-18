package dshstream

import "context"

// runControl serves the session/control logical stream.
//
// The first item is exactly one baseline frame, which the client's snapshot
// stream requires before it will accept any later frame
// (api/session-controller/src/client/transport.ts createSessionControlStream).
// Upstream's baseline carries {jobs, projections} keyed by session id; both are
// sent empty here, which upstream itself documents as a legal minimal baseline
// (types.ts SessionControlBaseline, and the recon's §3(f)).
//
// This host cannot populate them truthfully. harnesshttp.RunManager models one
// run, not a per-session background-job list, and the repository has no
// session-projection providers, so a non-empty jobs map or projection value
// would be an invention. The stream stays open after the baseline because the
// client treats an end after the baseline as a lost carrier and retries; it
// ends only when the client cancels it or the socket closes.
func (h *Handler) runControl(ctx context.Context, payload []byte, send func(any) error) error {
	args, failure := endpointArgs(payload)
	if failure != nil {
		return failure
	}
	if !emptyArgs(args) {
		return streamFail(codeArgumentsInvalid,
			"the session/control stream takes no arguments",
			map[string]any{"endpoint": "session/control"})
	}
	baseline := controlBaseline{
		Jobs:        map[string][]sessionJob{},
		Projections: map[string]any{},
	}
	if err := send(controlBaselineFrame{Type: "baseline", Value: baseline}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
