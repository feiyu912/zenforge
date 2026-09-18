package dshstream

import (
	"os"
	"time"
)

// Config is the trust and liveness seam for the transport handlers. It is
// configuration rather than a hard-coded address check so the process that
// owns the listener decides the policy: zenforge serve can pass its own
// --allow-remote decision here once it mounts these routes, and a test can
// shrink the heartbeat without touching the package.
type Config struct {
	// AllowRemote admits requests whose RemoteAddr is not a loopback address.
	// The zero value refuses them, which is the safe default for a console
	// that sends no credentials of its own. The WebSocket upgrade is fenced by
	// the same check as the unary handler, so a foreign peer is refused before
	// the socket is handed to the mux.
	AllowRemote bool

	// HeartbeatInterval is the interval between server-initiated WebSocket
	// Ping control frames. Upstream's default is 2000 ms
	// (api/gateway/src/index.ts DEFAULT_WEBSOCKET_HEARTBEAT_INTERVAL_MS).
	// Zero selects that default.
	HeartbeatInterval time.Duration

	// ApprovalPollInterval is how often the $events stream re-reads
	// approval.Inbox for pending requests. The repository has no global
	// approval notification source (eventlog.Bus is per-run and the pending
	// broker exposes no change feed), so the stream reconciles the inbox
	// instead of inventing one. Zero selects 200 ms.
	ApprovalPollInterval time.Duration

	// WriteTimeout bounds one frame or control write. A dead peer must not be
	// able to pin a stream goroutine forever. Zero selects 5 s.
	WriteTimeout time.Duration

	// Home is the value of the $events ready frame's host.home, used by the
	// console only to abbreviate displayed filesystem paths. Empty resolves
	// os.UserHomeDir(), falling back to "" when the process has no home.
	Home string
}

const (
	defaultHeartbeatInterval    = 2000 * time.Millisecond
	defaultApprovalPollInterval = 200 * time.Millisecond
	defaultWriteTimeout         = 5 * time.Second

	// maxMissedHeartbeats mirrors upstream's MAX_MISSED_HEARTBEATS: a client
	// that has not answered this many pings is terminated. The read deadline is
	// therefore (maxMissedHeartbeats+1) intervals, so the first missed pong does
	// not close a merely slow client.
	maxMissedHeartbeats = 2
)

// withDefaults returns a copy with every zero field resolved. It is applied
// once in New so the request paths never re-check for zero.
func (c Config) withDefaults() Config {
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = defaultHeartbeatInterval
	}
	if c.ApprovalPollInterval <= 0 {
		c.ApprovalPollInterval = defaultApprovalPollInterval
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = defaultWriteTimeout
	}
	if c.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			c.Home = home
		}
	}
	return c
}

// pongWait is the read deadline applied to a connection and refreshed by every
// pong. It is unexported because it is derived, not configured.
func (c Config) pongWait() time.Duration {
	return time.Duration(maxMissedHeartbeats+1) * c.HeartbeatInterval
}
