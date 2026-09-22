package dshstream

import (
	"github.com/feiyu912/zenforge/internal/dshwire"

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

	// ModelSelections, when set, reports every session's durable model selection:
	// the control baseline publishes them and a follow snapshot carries the one
	// the followed session holds. Nil reports none, which is what a host that
	// cannot select a model should say.
	ModelSelections func() map[string]ModelSelectionState

	// ModelSelectionUpdates, when set, delivers later selections until the
	// returned function is called. The control stream is where they are sent,
	// because the session stream has no projection frame.
	ModelSelectionUpdates func(observe func(ModelSelectionUpdate)) (unsubscribe func())

	// Goals, when set, reports one session's goal projection cell: the follow
	// snapshot publishes it, and it is the null arm for a session with no current
	// goal. Nil reports no capability, which is what a host with no goal store
	// should say. It is not part of the control baseline; control.go explains
	// why the goal cell travels with the session's own snapshot instead.
	Goals func(sessionID string) *GoalProjection

	// GoalUpdates, when set, delivers later goal mutations until the returned
	// function is called. Two live carriers carry them: the control stream sends
	// the projection frame (`goal`), and the forwarded-event stream emits
	// `goal/activation-changed` so the dock's activation hook tracks the same
	// commit.
	GoalUpdates func(observe func(GoalUpdate)) (unsubscribe func())

	// Workspaces, when set, reports the console's workspace registry: the
	// workspace/follow baseline publishes it and later changes arrive through
	// WorkspaceUpdates. Nil reports none, which is what a host with no
	// workspace grouping should say.
	Workspaces func() WorkspaceBaseline

	// WorkspaceUpdates, when set, delivers registry changes until the returned
	// function is called. The workspace/follow stream is where they are sent.
	WorkspaceUpdates func(observe func(WorkspaceUpdate)) (unsubscribe func())

	// DraftSessions, when set, reports whether this host created a session that
	// has not started a turn yet. Such a session's history is empty rather than
	// missing: the console opens a draft's log the moment it creates it, so
	// follow serves the empty snapshot and waits for the first prompt's run. Nil
	// reports that no session is a draft, which keeps a session this host never
	// created a not-found.
	DraftSessions func(sessionID string) bool

	// ModelDefault, when set, names the provider and model this host serves by
	// default. A projected transcript stamps it on assistant messages as their
	// provenance for a session that chose no model of its own. Nil leaves the
	// label unset.
	ModelDefault func() dshwire.Identity

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
