package dshstream

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The trust fence below is a shape-identical copy of internal/dshapi's
// unexported fence, originMatchesHost and remoteIsLoopback. The WebSocket
// upgrade must be refused under exactly the same policy as every unary /api
// request — upstream fences the mux route with connection.requestRejection
// before handing the socket to the mux (api/gateway/src/index.ts:220-228) —
// and dshapi is another task's package, so the small functions are duplicated
// rather than exported. Keep the two in sync.

// fence applies the Host/Origin/RemoteAddr trust checks the console expects
// from a host. It runs before routing and before the body is read: a request
// that fails trust learns nothing about which endpoints exist, and a WebSocket
// handshake is refused before the connection is upgraded.
func (h *Handler) fence(w http.ResponseWriter, r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		writeForbidden(w, "cross-site requests are refused")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !originMatchesHost(origin, r.Host) {
		writeForbidden(w, fmt.Sprintf("Origin %q does not match Host %q", origin, r.Host))
		return false
	}
	if !h.cfg.AllowRemote && !remoteIsLoopback(r.RemoteAddr) {
		writeForbidden(w, "non-loopback remote addresses are refused unless remote access is configured")
		return false
	}
	return true
}

// originMatchesHost compares the authority of an Origin header against the
// request Host. A serialized "null" origin parses to an empty host and is a
// mismatch, which is the correct answer: it identifies no same-origin page.
func originMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}

// remoteIsLoopback reports whether a RemoteAddr names this machine. It accepts
// the host:port form net/http produces as well as a bare host, so a test or a
// proxy that omits the port still gets a real answer.
func remoteIsLoopback(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
