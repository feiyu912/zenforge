package dshstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// Handler serves the console's transport streams: the WebSocket mux and the
// out-of-band approval result route. It is a plain http.Handler with no
// knowledge of where the routes are mounted, so serve can wire it later and
// tests can drive it with httptest.
type Handler struct {
	manager *harnesshttp.RunManager
	events  eventlog.Store
	inbox   approval.Inbox
	cfg     Config

	upgrader websocket.Upgrader

	// clients counts active $events generations by clientId. The result route
	// refuses an answer that names no live generation, which is upstream's
	// correlation rule (api/gateway/src/index.ts dispatchRpc: an unknown
	// clientId identifies no active event stream) and one more fail-closed
	// check on the approval path.
	mu      sync.Mutex
	clients map[string]int
}

// New builds the transport handler from the run manager, durable store, and
// approval inbox the caller already owns. All three are required: follow
// cannot be answered without the log, and an approval answer cannot reach a
// broker this handler does not hold.
func New(manager *harnesshttp.RunManager, events eventlog.Store, inbox approval.Inbox, cfg Config) (*Handler, error) {
	if manager == nil {
		return nil, fmt.Errorf("run manager is required")
	}
	if events == nil || nilInterface(events) {
		return nil, fmt.Errorf("event store is required")
	}
	if inbox == nil || nilInterface(inbox) {
		return nil, fmt.Errorf("approval inbox is required")
	}
	return &Handler{
		manager: manager,
		events:  events,
		inbox:   inbox,
		cfg:     cfg.withDefaults(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// The Origin/Host/RemoteAddr fence already ran in ServeHTTP. Doing
			// it there rather than here keeps one policy for the mux and the
			// unary routes and makes the refusal an ordinary 403 response.
			CheckOrigin: func(*http.Request) bool { return true },
		},
		clients: make(map[string]int),
	}, nil
}

// ServeHTTP dispatches one request for either owned route. The handler expects
// the full path; no StripPrefix applies. Serve registers it at exactly
// MuxPath and EventsResultPath, and anything else 404s, which leaves the
// unary /api handler and the static shell free to own every other path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.fence(w, r) {
		return
	}
	switch r.URL.Path {
	case MuxPath:
		h.serveMux(w, r)
	case EventsResultPath:
		h.serveResult(w, r)
	default:
		writeNotFound(w)
	}
}

// runStream dispatches one opened logical stream. It returns nil for a clean
// end (the mux writes an end frame) and a *streamError for a failed stream
// (the mux writes an error frame). Everything the endpoint writes before that
// goes through send as an item value.
func (h *Handler) runStream(ctx context.Context, endpoint string, payload json.RawMessage, send func(any) error) error {
	switch endpoint {
	case eventStreamEndpoint:
		return h.runEvents(ctx, payload, send)
	case "session/control":
		return h.runControl(ctx, payload, send)
	case "session/follow":
		return h.runFollow(ctx, payload, send)
	default:
		return streamFail(codeNotFound, "stream endpoint not found: "+endpoint,
			map[string]any{"endpoint": endpoint})
	}
}

// registerClient records one $events generation.
func (h *Handler) registerClient(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[clientID]++
}

// unregisterClient drops one generation, removing the id when its last
// generation ends.
func (h *Handler) unregisterClient(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[clientID] <= 1 {
		delete(h.clients, clientID)
		return
	}
	h.clients[clientID]--
}

// clientActive reports whether a $events generation with this id is live.
func (h *Handler) clientActive(clientID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[clientID] > 0
}

// newClientID mints an opaque, unique identity for one $events generation.
// Upstream uses a UUID; the client treats it as an opaque non-empty string.
func newClientID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("client_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
