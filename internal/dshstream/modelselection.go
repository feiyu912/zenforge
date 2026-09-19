package dshstream

// The session's durable model selection, as the console reads it.
//
// It travels as a session projection, not as a frame of its own: the follow
// stream's opening snapshot carries the baseline and the control stream carries
// every later value (session-controller/src/control.ts:25-31 broadcasts
// `{type:'projection', sessionId, key, value, seq}` and :78-90 publishes the
// per-session baseline). The model picker renders `projected.next ?? catalog.default`
// (client/ui-model-selection/src/client/directory.ts:144-172), so a host that
// served session/selectModel without publishing the projection would accept a
// choice the composer never shows.

// ModelSelection is one session's chosen provider and model. It mirrors
// ModelSelection (session-controller/lib/types/types.d.ts:88-95).
type ModelSelection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ReasoningEffort is omitted when the adapter exposes no effort choice, which
	// is this host's answer for every model it serves.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// ModelSelectionProjection is the client's view of the durable fold
// (session-controller/lib/types/types.d.ts:97-104): the selection the latest
// recorded model request consumed, and the one the next request should use.
type ModelSelectionProjection struct {
	LastUsed *ModelSelection `json:"lastUsed"`
	Next     *ModelSelection `json:"next"`
}

// ModelSelectionSandState is one session's projection and the local sequence it
// was last written at.
type ModelSelectionState struct {
	Projection ModelSelectionProjection
	// Seq orders updates for a client. It is this host's own monotone counter
	// rather than a session-log watermark, and the ADR records that difference.
	Seq int64
}

// ModelSelectionUpdate is one live projection change: the key is always
// `modelSelection`, the single projection this host defines.
type ModelSelectionUpdate struct {
	SessionID  string
	Projection ModelSelectionProjection
	Seq        int64
}

// projectionFrame is SessionControlFrame's projection arm
// (session-controller/lib/types/types.d.ts:515-524).
type projectionFrame struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Key       string `json:"key"`
	Value     any    `json:"value"`
	Seq       int64  `json:"seq"`
}

// modelSelectionProjectionKey is the projection name the console looks up.
const modelSelectionProjectionKey = "modelSelection"
