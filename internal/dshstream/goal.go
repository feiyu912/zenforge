package dshstream

// The session's goal, as the console reads it.
//
// Two shapes travel on the transport, and they are upstream's own:
//
//   - the `goal` **projection cell** (`GoalProjection | null`), which the
//     durable dock renders from (`client/ui-goal/src/client/GoalBar.tsx` reads
//     `useProjection("goal")` and then `projection.goal`), and which is seeded
//     by the follow snapshot's projections block and updated by the control
//     stream's projection frames under the client's one-higher-seq-wins rule;
//   - the `goal/activation-changed` **emit**, which carries the exact
//     `{id, revision, activation}` snapshot (or no goal after a clear) and is
//     what the dock's process-local activation hook listens for.
//
// The projection module of the goal package declares the cell:
// `SessionProjectionMap.goal: GoalProjection | null`
// (@deepseek-ai/dsh-goal/lib/types/types.d.ts), and the emitted payload is
// `GoalActivationChanged` there. Both are re-derived from the vendored console
// bundle by internal/dshapi's boundary test, so a console upgrade that changes
// either shape fails a test rather than silently mis-rendering the dock.

// goalProjectionKey is the projection name the console's goal dock looks up.
const goalProjectionKey = "goal"

// GoalBlockedReason mirrors the console's GoalBlockReason: the machine-routable
// classification and the human-readable explanation of a blocked goal.
type GoalBlockedReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// GoalSnapshot is GoalSnapshot: the durable half of a goal. Activation is not
// here on purpose -- it is process-local, and the projection reflects the
// durable phase alone.
type GoalSnapshot struct {
	ID            string             `json:"id"`
	Revision      int                `json:"revision"`
	Objective     string             `json:"objective"`
	Phase         string             `json:"phase"`
	BlockedReason *GoalBlockedReason `json:"blockedReason,omitempty"`
	MaxGoalRounds int                `json:"maxGoalRounds"`
}

// GoalProjection is the `goal` cell's non-null arm: the current durable goal
// with its replay counters (GoalProjection). A session with no current goal
// serves the cell's null arm instead, which is what upstream's
// `GoalProjection | null` means.
type GoalProjection struct {
	Goal          GoalSnapshot `json:"goal"`
	RoundsStarted int          `json:"roundsStarted"`
	CreatedAt     int64        `json:"createdAt"`
	UpdatedAt     int64        `json:"updatedAt"`
}

// GoalActivation is the `{id, revision, activation}` snapshot the
// `goal/activation-changed` emit carries.
type GoalActivation struct {
	ID         string `json:"id"`
	Revision   int    `json:"revision"`
	Activation string `json:"activation"`
}

// GoalActivationChanged is GoalActivationChanged: the session whose activation
// changed, and the exact current activation, absent when no goal is current.
// It is the single argument of the forwarded event, so it crosses the emit
// frame's `args` array as one object.
type GoalActivationChanged struct {
	SessionID string          `json:"sessionId"`
	Goal      *GoalActivation `json:"goal,omitempty"`
}

// GoalUpdate is one committed goal mutation, as the two live carriers need it:
// the control stream turns it into a projection frame, and the forwarded-event
// stream turns it into a `goal/activation-changed` emit.
type GoalUpdate struct {
	SessionID string
	// Projection is nil after a clear.
	Projection *GoalProjection
	// Seq orders the frames a client receives. It is this host's own monotone
	// counter rather than a session-log watermark, and it deliberately outranks
	// any session cursor: the console's history seed installs the projections
	// block at the cursor's watermark, and a frame numbered below it would be
	// discarded by the client's one-higher-seq-wins rule. A goal is not a
	// session-log event -- a mutation commits without appending anything to the
	// conversation -- so it has no cursor of its own to be numbered by.
	Seq int64
	// Activation is "armed" or "disarmed" for a live goal, and empty when the
	// session has no goal to activate.
	Activation string
}

// activationChanged projects an update onto the forwarded event's payload.
func (u GoalUpdate) activationChanged() GoalActivationChanged {
	changed := GoalActivationChanged{SessionID: u.SessionID}
	if u.Projection != nil {
		changed.Goal = &GoalActivation{
			ID:         u.Projection.Goal.ID,
			Revision:   u.Projection.Goal.Revision,
			Activation: u.Activation,
		}
	}
	return changed
}

// goalCell renders a goal projection as the cell's JSON value: the projection,
// or nil for the null arm. A nil projection marshals as `null` exactly as
// upstream's `GoalProjection | null` requires, and the key stays present so the
// client's seed treats the capability as known rather than absent.
func goalCell(projection *GoalProjection) any {
	if projection == nil {
		return nil
	}
	return *projection
}
