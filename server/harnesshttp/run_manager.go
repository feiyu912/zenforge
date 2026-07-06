package harnesshttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/eventlog"
)

// RunStatus is the manager's in-memory view of a detached run.
type RunStatus string

const (
	RunStarting        RunStatus = "starting"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
)

var (
	ErrRunExists      = errors.New("run already exists")
	ErrRunNotFound    = errors.New("run not found")
	ErrManagerClosed  = errors.New("run manager is closed")
	ErrMaxActive      = errors.New("maximum active runs reached")
	ErrRunTerminal    = errors.New("run is already terminal")
	ErrRunActive      = errors.New("run is still active")
	ErrInvalidRunID   = errors.New("run ID is required")
	ErrEventsExist    = fmt.Errorf("%w: durable events already exist", ErrRunExists)
	ErrResumeNotFound = fmt.Errorf("%w: no durable events exist", ErrRunNotFound)
	ErrEventsRequired = errors.New("event store and bus are required")
)

const defaultTerminalRetention = 5 * time.Minute

// RunManagerOptions controls detached execution. TerminalRetention defaults to
// five minutes. A negative value retains terminal records until Forget or the
// manager itself is discarded.
type RunManagerOptions struct {
	MaxActive         int
	RunTimeout        time.Duration
	TerminalRetention time.Duration
	Follow            eventlog.FollowOptions
	NewRunID          func() string
}

// RunInfo is an immutable snapshot of a managed run.
type RunInfo struct {
	RunID      string    `json:"runId"`
	Status     RunStatus `json:"status"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

type managedRun struct {
	info   RunInfo
	cancel context.CancelFunc
	done   chan struct{}
	timer  *time.Timer
}

// RunManager owns detached run contexts and their sole stream drainers.
//
// Duplicate exclusion is deliberately a single-process contract. The durable
// event check prevents reuse of an existing run ID, but is not a distributed
// claim; deployments with multiple managers need an external atomic claim.
type RunManager struct {
	agent  Agent
	events eventlog.Store
	bus    *eventlog.Bus
	opts   RunManagerOptions

	rootCtx context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	runs   map[string]*managedRun
	active int
	closed bool
	wg     sync.WaitGroup
}

func NewRunManager(agent Agent, events eventlog.Store, bus *eventlog.Bus, opts RunManagerOptions) *RunManager {
	rootCtx, cancel := context.WithCancel(context.Background())
	if opts.TerminalRetention == 0 {
		opts.TerminalRetention = defaultTerminalRetention
	}
	if opts.NewRunID == nil {
		opts.NewRunID = randomRunID
	}
	return &RunManager{
		agent: agent, events: events, bus: bus, opts: opts,
		rootCtx: rootCtx, cancel: cancel, runs: make(map[string]*managedRun),
	}
}

// Start reserves a run ID before invoking Agent.Stream. The caller context is
// used only for admission checks; execution is rooted in the manager context.
func (m *RunManager) Start(ctx context.Context, task zenforge.Task) (RunInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextErr(ctx); err != nil {
		return RunInfo{}, err
	}
	if strings.TrimSpace(task.RunID) == "" {
		task.RunID = m.opts.NewRunID()
	}
	task.RunID = strings.TrimSpace(task.RunID)
	return m.start(ctx, task.RunID, false, func(runCtx context.Context) (<-chan zenforge.Event, error) {
		return m.agent.Stream(runCtx, task)
	})
}

// Resume starts a detached resume operation under the manager root context.
func (m *RunManager) Resume(ctx context.Context, runID string) (RunInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runID = strings.TrimSpace(runID)
	return m.start(ctx, runID, true, func(runCtx context.Context) (<-chan zenforge.Event, error) {
		return m.agent.Resume(runCtx, runID)
	})
}

func (m *RunManager) start(
	ctx context.Context,
	runID string,
	resume bool,
	open func(context.Context) (<-chan zenforge.Event, error),
) (RunInfo, error) {
	if runID == "" {
		return RunInfo{}, ErrInvalidRunID
	}
	if m.agent == nil {
		return RunInfo{RunID: runID}, fmt.Errorf("agent is required")
	}
	if m.events == nil || m.bus == nil {
		return RunInfo{RunID: runID}, ErrEventsRequired
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return RunInfo{RunID: runID}, ErrManagerClosed
	}
	if _, exists := m.runs[runID]; exists {
		m.mu.Unlock()
		return RunInfo{RunID: runID}, ErrRunExists
	}
	if m.opts.MaxActive > 0 && m.active >= m.opts.MaxActive {
		m.mu.Unlock()
		return RunInfo{RunID: runID}, ErrMaxActive
	}
	latest, err := m.events.LatestSeq(ctx, runID)
	if err != nil {
		m.mu.Unlock()
		return RunInfo{RunID: runID}, fmt.Errorf("check durable run %q: %w", runID, err)
	}
	if !resume && latest != 0 {
		m.mu.Unlock()
		return RunInfo{RunID: runID}, ErrEventsExist
	}
	if resume && latest == 0 {
		m.mu.Unlock()
		return RunInfo{RunID: runID}, ErrResumeNotFound
	}

	runCtx, cancel := context.WithCancel(m.rootCtx)
	if m.opts.RunTimeout > 0 {
		runCtx, cancel = context.WithTimeout(m.rootCtx, m.opts.RunTimeout)
	}
	now := time.Now().UTC()
	run := &managedRun{
		info:   RunInfo{RunID: runID, Status: RunStarting, StartedAt: now, UpdatedAt: now},
		cancel: cancel, done: make(chan struct{}),
	}
	// Reservation happens before Agent.Stream so concurrent starts fail closed.
	m.runs[runID] = run
	m.active++
	// Register the opener before releasing the lock so Close cannot begin a
	// zero-count Wait while this start is still becoming active.
	m.wg.Add(1)
	m.mu.Unlock()

	events, err := open(runCtx)
	if err != nil {
		cancel()
		err = m.persistTerminal(runID, RunFailed, err)
		m.mu.Lock()
		m.finishLocked(run, RunFailed, err)
		info := run.info
		m.mu.Unlock()
		m.wg.Done()
		return info, err
	}
	if events == nil {
		err = errors.New("agent returned a nil event stream")
		cancel()
		err = m.persistTerminal(runID, RunFailed, err)
		m.mu.Lock()
		m.finishLocked(run, RunFailed, err)
		info := run.info
		m.mu.Unlock()
		m.wg.Done()
		return info, err
	}
	go m.drain(run, runCtx, events)
	m.mu.Lock()
	info := run.info
	m.mu.Unlock()
	return info, nil
}

func (m *RunManager) drain(run *managedRun, ctx context.Context, events <-chan zenforge.Event) {
	defer m.wg.Done()
	for event := range events {
		m.observe(run, event)
	}

	m.mu.Lock()
	if terminal(run.info.Status) {
		m.mu.Unlock()
		return
	}
	runID := run.info.RunID
	err := ctx.Err()
	status := RunFailed
	switch {
	case errors.Is(err, context.Canceled):
		status = RunCancelled
	case errors.Is(err, context.DeadlineExceeded):
		status = RunFailed
	default:
		err = errors.New("agent stream closed without a terminal event")
	}
	m.mu.Unlock()

	err = m.persistTerminal(runID, status, err)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishLocked(run, status, err)
}

func (m *RunManager) observe(run *managedRun, event zenforge.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if terminal(run.info.Status) {
		return
	}
	switch event.Type {
	case zenforge.EventApprovalRequested:
		m.setStatusLocked(run, RunWaitingApproval)
	case zenforge.EventApprovalResolved, zenforge.EventApprovalExpired:
		m.setStatusLocked(run, RunRunning)
	case zenforge.EventRunDone:
		m.finishLocked(run, RunCompleted, nil)
	case zenforge.EventRunError:
		m.finishLocked(run, RunFailed, eventError(event))
	case zenforge.EventRunCancelled:
		err := eventError(event)
		status := RunCancelled
		if errors.Is(err, context.DeadlineExceeded) {
			status = RunFailed
		}
		m.finishLocked(run, status, err)
	default:
		if run.info.Status == RunStarting {
			m.setStatusLocked(run, RunRunning)
		}
	}
}

func (m *RunManager) Get(runID string) (RunInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[strings.TrimSpace(runID)]
	if !ok {
		return RunInfo{}, ErrRunNotFound
	}
	return run.info, nil
}

// Attach replays durable events and then follows live appends. Cancelling the
// attachment context only disconnects this follower, never the run.
func (m *RunManager) Attach(ctx context.Context, runID string, afterSeq int64) (<-chan zenforge.Event, <-chan error, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil, ErrInvalidRunID
	}
	if m.events == nil || m.bus == nil {
		return nil, nil, ErrEventsRequired
	}
	if _, err := m.Get(runID); err != nil {
		latest, storeErr := m.events.LatestSeq(ctx, runID)
		if storeErr != nil {
			return nil, nil, storeErr
		}
		if latest == 0 {
			return nil, nil, err
		}
	}
	return eventlog.Follow(ctx, m.events, m.bus, runID, afterSeq, m.opts.Follow)
}

// Cancel is idempotent for a cancelled run; other terminal states conflict.
func (m *RunManager) Cancel(runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[strings.TrimSpace(runID)]
	if !ok {
		return ErrRunNotFound
	}
	if run.info.Status == RunCancelled {
		return nil
	}
	if terminal(run.info.Status) {
		return ErrRunTerminal
	}
	run.cancel()
	return nil
}

// Forget removes a terminal record early. Durable events are not deleted.
func (m *RunManager) Forget(runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[strings.TrimSpace(runID)]
	if !ok {
		return ErrRunNotFound
	}
	if !terminal(run.info.Status) {
		return ErrRunActive
	}
	if run.timer != nil {
		run.timer.Stop()
	}
	delete(m.runs, run.info.RunID)
	return nil
}

// Close rejects new work, cancels active runs, and waits for all drainers.
func (m *RunManager) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *RunManager) setStatusLocked(run *managedRun, status RunStatus) {
	run.info.Status = status
	run.info.UpdatedAt = time.Now().UTC()
}

func (m *RunManager) finishLocked(run *managedRun, status RunStatus, err error) {
	if terminal(run.info.Status) {
		return
	}
	now := time.Now().UTC()
	run.info.Status = status
	run.info.UpdatedAt = now
	run.info.FinishedAt = now
	if err != nil {
		run.info.Error = err.Error()
	}
	m.active--
	run.cancel()
	close(run.done)
	if m.bus != nil {
		m.bus.CloseRun(run.info.RunID)
	}
	if m.opts.TerminalRetention >= 0 {
		retention := m.opts.TerminalRetention
		runID := run.info.RunID
		run.timer = time.AfterFunc(retention, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if current := m.runs[runID]; current == run {
				delete(m.runs, runID)
			}
		})
	}
}

func terminal(status RunStatus) bool {
	return status == RunCompleted || status == RunFailed || status == RunCancelled
}

func eventError(event zenforge.Event) error {
	if value, ok := event.Payload["error"].(string); ok && value != "" {
		switch value {
		case context.Canceled.Error():
			return context.Canceled
		case context.DeadlineExceeded.Error():
			return context.DeadlineExceeded
		}
		return errors.New(value)
	}
	return nil
}

func (m *RunManager) persistTerminal(runID string, status RunStatus, runErr error) error {
	ctx := context.Background()
	latest, err := m.events.LatestSeq(ctx, runID)
	if err != nil {
		return errors.Join(runErr, fmt.Errorf("read terminal event: %w", err))
	}
	if latest > 0 {
		events, readErr := m.events.Read(ctx, runID, latest-1, 1)
		if readErr != nil {
			return errors.Join(runErr, fmt.Errorf("read terminal event: %w", readErr))
		}
		if len(events) == 1 && terminalEventType(events[0].Type) {
			return runErr
		}
	}
	eventType := zenforge.EventRunError
	if status == RunCancelled {
		eventType = zenforge.EventRunCancelled
	}
	message := "detached run ended without a terminal event"
	if runErr != nil {
		message = runErr.Error()
	}
	if appendErr := m.events.Append(ctx, zenforge.NewEvent(eventType, runID, map[string]any{"error": message})); appendErr != nil {
		return errors.Join(runErr, fmt.Errorf("persist terminal event: %w", appendErr))
	}
	return runErr
}

func terminalEventType(eventType zenforge.EventType) bool {
	return eventType == zenforge.EventRunDone ||
		eventType == zenforge.EventRunError ||
		eventType == zenforge.EventRunCancelled
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func randomRunID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("run_%d", time.Now().UnixNano())
	}
	return "run_" + hex.EncodeToString(raw[:])
}
