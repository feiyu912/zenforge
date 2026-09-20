package dshstream

import (
	"encoding/json"

	"github.com/feiyu912/zenforge/internal/dshwire"
)

// Route paths. These are the exact upstream constants
// (api/gateway/src/stream-protocol.ts REMOTE_STREAM_MUX_PATH and
// REMOTE_EVENT_RESULT_ENDPOINT). Serve registers Handler at both; the handler
// expects the full path and no StripPrefix applies.
const (
	// MuxPath is the WebSocket multiplex route.
	MuxPath = "/api/remote.mux"
	// EventsResultPath is the unary route that answers one forwarded approval.
	EventsResultPath = "/api/$events/result"
)

// eventStreamEndpoint is the Gateway-internal logical stream carrying forwarded
// events. It is not a <namespace>/<method> endpoint.
const eventStreamEndpoint = "$events"

// Error codes on the wire. gateway/* spellings mirror the upstream gateway's
// own vocabulary; the session/* and approval/* codes are this host's additions
// for failures a panel surfaces. The client accepts any string code.
const (
	codeBadRequest         = "gateway/bad-request"
	codeArgumentsInvalid   = "gateway/arguments-invalid"
	codeInternal           = "gateway/internal"
	codeNotFound           = "gateway/not-found"
	codeServiceUnavailable = "gateway/service-unavailable"
	// codeUnimplemented is the honest answer for a capability this host knows
	// about but cannot implement without inventing semantics.
	codeUnimplemented    = "unimplemented"
	codeSessionNotFound  = "session/not-found"
	codeApprovalNotFound = "approval/not-found"
	codeApprovalConflict = "approval/conflict"
)

// maxRequestBodyBytes bounds one unary result request. The body is a small
// correlation triple; a body this large is a bug or an allocation attack, and
// it is cheaper to refuse it before decoding than after.
const maxRequestBodyBytes = 1 << 20

// serverFrame is one host-to-browser mux frame. The shipped client parses
// every frame with parseRemoteStreamServerMessage, which accepts exactly:
//
//	{type:"item",  streamId}            or {type:"item", streamId, value}
//	{type:"error", streamId, error}
//	{type:"end",   streamId}
//
// so Value and Error are both omitempty and mutually exclusive: a live item
// frame never grows an error key, and a terminal error frame never grows a
// value key.
type serverFrame struct {
	Type     string     `json:"type"`
	StreamID string     `json:"streamId"`
	Value    any        `json:"value,omitempty"`
	Error    *wireError `json:"error,omitempty"`
}

// wireError is the exact error object upstream validates: code, message, and a
// details object (never absent — the client reads isRecord(details)).
type wireError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// streamError is one logical stream's failure. It becomes an error frame on the
// mux; a nil *streamError is clean completion, which becomes an end frame.
type streamError struct {
	code    string
	message string
	details map[string]any
}

func (e *streamError) Error() string { return e.message }

func (e *streamError) wire() *wireError {
	details := e.details
	if details == nil {
		details = map[string]any{}
	}
	return &wireError{Code: e.code, Message: e.message, Details: details}
}

func streamFail(code, message string, details map[string]any) *streamError {
	return &streamError{code: code, message: message, details: details}
}

// readyValue is the first item of the $events stream. Exact keys per
// stream-protocol.ts RemoteEventReadyFrame and the client's
// parseRemoteEventReady: {type, clientId, host:{home}} and nothing else.
type readyValue struct {
	Type     string   `json:"type"`
	ClientID string   `json:"clientId"`
	Host     hostInfo `json:"host"`
}

// hostInfo carries the only host fact the opening frame publishes: the account
// home used to abbreviate displayed paths.
type hostInfo struct {
	Home string `json:"home"`
}

// emitValue is a forwarded notification (RemoteEventEmitFrame).
type emitValue struct {
	Type  string `json:"type"`
	Event string `json:"event"`
	Args  []any  `json:"args"`
}

// waterfallValue is one pending, answerable forwarded request
// (RemoteEventInvocationFrame). The client validates exact outer keys and
// rejects a request object that carries "agent" or "signal", so the request
// map must never contain either.
type waterfallValue struct {
	Type    string         `json:"type"`
	Event   string         `json:"event"`
	EventID string         `json:"eventId"`
	AgentID string         `json:"agentId"`
	Request map[string]any `json:"request"`
}

// cancelValue withdraws a previously delivered waterfall under the same id
// (RemoteEventCancellationFrame).
type cancelValue struct {
	Type    string `json:"type"`
	EventID string `json:"eventId"`
}

// controlBaselineFrame is the first and, on this host, only session/control
// item. SessionControlFrame's baseline arm is {type:"baseline", value:{jobs,
// projections}}; an empty jobs map and an empty projections map are the legal
// minimal baseline (session-controller/src/types.ts:541-558).
type controlBaselineFrame struct {
	Type  string          `json:"type"`
	Value controlBaseline `json:"value"`
}

type controlBaseline struct {
	Jobs        map[string][]sessionJob `json:"jobs"`
	Projections map[string]any          `json:"projections"`
}

// sessionJob is the browser-safe background-job row the console expects. This
// host has no job model that produces one, so the type exists only to keep the
// baseline honest about its key: the maps marshal as {} rather than null.
type sessionJob struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

// snapshotFrame is the single opening item of session/follow
// (SessionFollowFrame's snapshot arm).
type snapshotFrame struct {
	Type            string             `json:"type"`
	Header          sessionHeader      `json:"header"`
	Cursor          int64              `json:"cursor"`
	Records         []eventRecord      `json:"records"`
	HasMore         bool               `json:"hasMore"`
	Projections     projectionBaseline `json:"projections"`
	AssistantStream *assistantBaseline `json:"assistantStream,omitempty"`
}

// sessionHeader is SessionWireHeader. Only the four mandatory fields are
// written: the run manager has no cwd, parent session, origin, delegation
// depth, or agent preset to report, and an omitted optional field is honest
// where a fabricated one is not.
type sessionHeader struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	CreatedAt int64  `json:"createdAt"`
	IsSeeded  bool   `json:"isSeeded"`
}

// projectionBaseline is SessionProjectionBaseline. The repository has no
// projection providers, so values is always empty; asOfSeq is the snapshot
// cursor, which is exactly the watermark an empty baseline must cite.
type projectionBaseline struct {
	AsOfSeq int64          `json:"asOfSeq"`
	Values  map[string]any `json:"values"`
}

// assistantBaseline is SessionAssistantStreamBaseline. The follow request from
// the shipped client always sets assistantStream:true, and the client throws
// when the opening snapshot then omits the baseline, so the field is written
// whenever the request opted in. revision 0 is upstream's own fallback for a
// session with no active accumulator (session-controller/src/history.ts:185).
type assistantBaseline struct {
	Revision int `json:"revision"`
}

// eventRecord is one SessionHistoryRecord: a durable event wrapped in the
// {type:"event", event} envelope used by both the snapshot records and the
// live event items.
type eventRecord struct {
	Type  string        `json:"type"`
	Event dshwire.Event `json:"event"`
}

// marshalFrame encodes one frame for the socket. It exists so every frame goes
// through the same encoder; a JSON error is a host bug, not a client error.
func marshalFrame(frame serverFrame) ([]byte, error) {
	return json.Marshal(frame)
}
