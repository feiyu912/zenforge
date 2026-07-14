package harness

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// RunController accepts in-process control messages for active runs. Steers are
// consumed only at a model-turn boundary so they never reorder a tool call and
// its result. Applications that need cross-process delivery must provide their
// own durable control queue and call EnqueueSteer on the owning process.
type RunController struct {
	mu   sync.Mutex
	runs map[string]*controlledRun
}

type controlledRun struct {
	closed bool
	steers []SteerState
}

// NewRunController creates a controller shared by an Agent and its transport.
func NewRunController() *RunController {
	return &RunController{runs: make(map[string]*controlledRun)}
}

// Open marks a run as accepting control messages. It is idempotent while the
// run remains active.
func (c *RunController) Open(runID string) error {
	if c == nil {
		return nil
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.runs[runID]; existing != nil && !existing.closed {
		return nil
	}
	c.runs[runID] = &controlledRun{}
	return nil
}

// Close stops new control messages for a run and releases queued data.
func (c *RunController) Close(runID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.runs[strings.TrimSpace(runID)]; run != nil {
		run.closed = true
		run.steers = nil
	}
}

// EnqueueSteer records a message for the next model-turn boundary. A blank ID
// is assigned by the controller so clients can correlate the emitted event.
func (c *RunController) EnqueueSteer(runID, steerID, message string) (SteerState, bool) {
	if c == nil {
		return SteerState{}, false
	}
	runID = strings.TrimSpace(runID)
	message = strings.TrimSpace(message)
	if runID == "" || message == "" {
		return SteerState{}, false
	}
	steerID = strings.TrimSpace(steerID)
	if steerID == "" {
		steerID = fmt.Sprintf("steer_%d", time.Now().UnixNano())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run := c.runs[runID]
	if run == nil || run.closed {
		return SteerState{}, false
	}
	steer := SteerState{ID: steerID, Message: message, CreatedAt: time.Now().UTC()}
	run.steers = append(run.steers, steer)
	return steer, true
}

// DrainSteers transfers the current FIFO queue to the Agent. It is intentionally
// non-blocking; model and tool work are never performed under this lock.
func (c *RunController) DrainSteers(runID string) []SteerState {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run := c.runs[strings.TrimSpace(runID)]
	if run == nil || run.closed || len(run.steers) == 0 {
		return nil
	}
	steers := append([]SteerState(nil), run.steers...)
	run.steers = nil
	return steers
}
