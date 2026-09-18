package dshapi

import (
	"log/slog"
	"strings"
)

// logUnservedEndpoint records one line for a request whose /api/<namespace>/<method>
// endpoint this host does not serve. It is the host-side replacement for
// reading a browser's network panel: the line names the namespace and method,
// so the next person can implement the namespaces the console actually calls,
// in that order.
//
// Only the two path segments are recorded. The request body is never passed
// here, so its arguments, its rpcId, and any credential it carries cannot
// appear in the log — a property that matters because this diagnostic is read
// while an operator has an API key configured. The endpoint has already been
// validated against the wire alphabet by endpointFromPath.
//
// A nil Logger is silent. The zero Config must not print, so this never falls
// back to slog.Default; that would make every unconfigured handler and every
// existing test emit lines it never asked for.
func (h *Handler) logUnservedEndpoint(endpoint string) {
	logger := h.cfg.Logger
	if logger == nil {
		return
	}
	namespace, method, found := strings.Cut(endpoint, "/")
	if !found {
		// endpointFromPath admits only two-segment endpoints, so this arm is
		// unreachable today. It stays so a future change cannot silently drop
		// the diagnostic or log a name that is not one.
		logger.Info("console rpc endpoint is not served by this host",
			slog.String("namespace", endpoint))
		return
	}
	logger.Info("console rpc endpoint is not served by this host",
		slog.String("namespace", namespace),
		slog.String("method", method))
}
