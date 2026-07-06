package harnesshttp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/eventlog"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
)

func TestRunManagerConcurrentDuplicateAndDurableDuplicate(t *testing.T) {
	manager, agent, store := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)

	const callers = 24
	var started atomic.Int32
	var duplicate atomic.Int32
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Start(context.Background(), zenforge.Task{RunID: "same", Input: "hi"})
			switch {
			case err == nil:
				started.Add(1)
			case errors.Is(err, ErrRunExists):
				duplicate.Add(1)
			default:
				t.Errorf("Start error = %v", err)
			}
		}()
	}
	wg.Wait()
	if started.Load() != 1 || duplicate.Load() != callers-1 {
		t.Fatalf("started=%d duplicate=%d", started.Load(), duplicate.Load())
	}
	agent.finish("same", zenforge.EventRunDone)
	waitStatus(t, manager, "same", RunCompleted)

	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "durable", nil)); err != nil {
		t.Fatal(err)
	}
	info, err := manager.Start(context.Background(), zenforge.Task{RunID: "durable", Input: "hi"})
	if !errors.Is(err, ErrEventsExist) || info.RunID != "durable" {
		t.Fatalf("Start durable = (%+v, %v)", info, err)
	}
}

func TestRunManagerDrainsWithoutSubscribers(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "many", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	for i := range 5000 {
		agent.send("many", zenforge.NewEvent(zenforge.EventModelDelta, "many", map[string]any{"i": i}))
	}
	agent.finish("many", zenforge.EventRunDone)
	waitStatus(t, manager, "many", RunCompleted)
}

func TestRunManagerAttachDisconnectDoesNotCancel(t *testing.T) {
	manager, agent, store := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "attach", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	agent.send("attach", zenforge.NewEvent(zenforge.EventRunStarted, "attach", nil))
	waitLatest(t, store, "attach", 1)

	attachCtx, disconnect := context.WithCancel(context.Background())
	events, _, err := manager.Attach(attachCtx, "attach", 0)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("attach did not replay")
	}
	disconnect()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("attach event channel remained open after disconnect")
		}
	case <-time.After(time.Second):
		t.Fatal("attach event channel did not close after disconnect")
	}
	if agent.cancelled("attach") {
		t.Fatal("attachment disconnect cancelled agent context")
	}
	agent.finish("attach", zenforge.EventRunDone)
	waitStatus(t, manager, "attach", RunCompleted)
}

func TestRunManagerApprovalAndCancel(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "approval", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	agent.send("approval", zenforge.NewEvent(zenforge.EventApprovalRequested, "approval", nil))
	waitStatus(t, manager, "approval", RunWaitingApproval)
	agent.send("approval", zenforge.NewEvent(zenforge.EventApprovalResolved, "approval", nil))
	waitStatus(t, manager, "approval", RunRunning)
	agent.send("approval", zenforge.NewEvent(zenforge.EventApprovalRequested, "approval", nil))
	waitStatus(t, manager, "approval", RunWaitingApproval)
	agent.send("approval", zenforge.NewEvent(zenforge.EventApprovalExpired, "approval", nil))
	waitStatus(t, manager, "approval", RunRunning)

	if err := manager.Cancel("approval"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, manager, "approval", RunCancelled)
	if err := manager.Cancel("approval"); err != nil {
		t.Fatalf("second Cancel = %v", err)
	}

	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "done", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	agent.finish("done", zenforge.EventRunDone)
	waitStatus(t, manager, "done", RunCompleted)
	if err := manager.Cancel("done"); !errors.Is(err, ErrRunTerminal) {
		t.Fatalf("Cancel completed = %v", err)
	}
}

func TestRunManagerResumeRequiresAndUsesDurableRun(t *testing.T) {
	durable := eventlogmemory.New()
	bus := eventlog.NewBus()
	store := eventlog.NewFanoutStore(durable, bus)
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "resume", nil)); err != nil {
		t.Fatal(err)
	}
	agent := &managerTestAgent{
		streams: make(map[string]chan zenforge.Event),
		ctxs:    make(map[string]context.Context),
		store:   store,
	}
	manager := NewRunManager(agent, store, bus, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)

	if _, err := manager.Resume(context.Background(), "missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Resume missing = %v", err)
	}
	if _, err := manager.Resume(context.Background(), "resume"); err != nil {
		t.Fatal(err)
	}
	agent.finish("resume", zenforge.EventRunDone)
	waitStatus(t, manager, "resume", RunCompleted)
}

func TestRunManagerCloseAndRetention(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: 10 * time.Millisecond})
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "close", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, manager, "close", RunCancelled)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "late", Input: "hi"}); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Start after Close = %v", err)
	}
	if !agent.cancelled("close") {
		t.Fatal("Close did not cancel run")
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := manager.Get("close"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Get after retention = %v", err)
	}
}

func TestRunManagerTimeoutIsFailed(t *testing.T) {
	manager, _, _ := newTestRunManager(t, RunManagerOptions{
		RunTimeout:        5 * time.Millisecond,
		TerminalRetention: -1,
	})
	defer closeManager(t, manager)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "timeout", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, manager, "timeout", RunFailed)
	info, err := manager.Get("timeout")
	if err != nil {
		t.Fatal(err)
	}
	if info.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("timeout error = %q", info.Error)
	}
}

func TestRunManagerRequestContextMaxActiveAndForget(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{
		MaxActive:         1,
		TerminalRetention: -1,
	})
	defer closeManager(t, manager)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	if _, err := manager.Start(requestCtx, zenforge.Task{RunID: "detached", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	time.Sleep(10 * time.Millisecond)
	if agent.cancelled("detached") {
		t.Fatal("request cancellation reached detached run")
	}
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "limited", Input: "hi"}); !errors.Is(err, ErrMaxActive) {
		t.Fatalf("Start beyond MaxActive = %v", err)
	}
	if err := manager.Forget("detached"); !errors.Is(err, ErrRunActive) {
		t.Fatalf("Forget active = %v", err)
	}
	agent.finish("detached", zenforge.EventRunDone)
	waitStatus(t, manager, "detached", RunCompleted)
	if err := manager.Forget("detached"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get("detached"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("Get forgotten = %v", err)
	}
}

func TestRunManagerPersistsTerminalWhenStreamCannotOpen(t *testing.T) {
	durable := eventlogmemory.New()
	bus := eventlog.NewBus()
	store := eventlog.NewFanoutStore(durable, bus)
	manager := NewRunManager(
		managerErrorAgent{err: errors.New("open failed")},
		store,
		bus,
		RunManagerOptions{TerminalRetention: time.Millisecond},
	)
	defer closeManager(t, manager)

	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "open_error", Input: "hi"}); err == nil {
		t.Fatal("Start returned nil error")
	}
	events, err := durable.Read(context.Background(), "open_error", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != zenforge.EventRunError {
		t.Fatalf("durable events = %#v, want one run.error", events)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "open_error", Input: "again"}); !errors.Is(err, ErrEventsExist) {
		t.Fatalf("reused failed run ID error = %v, want ErrEventsExist", err)
	}
}

type managerErrorAgent struct {
	err error
}

func (a managerErrorAgent) Stream(context.Context, zenforge.Task) (<-chan zenforge.Event, error) {
	return nil, a.err
}

func (a managerErrorAgent) Resume(context.Context, string) (<-chan zenforge.Event, error) {
	return nil, a.err
}

type managerTestAgent struct {
	mu      sync.Mutex
	streams map[string]chan zenforge.Event
	ctxs    map[string]context.Context
	store   eventlog.Store
}

func newTestRunManager(t *testing.T, opts RunManagerOptions) (*RunManager, *managerTestAgent, eventlog.Store) {
	t.Helper()
	durable := eventlogmemory.New()
	bus := eventlog.NewBus()
	store := eventlog.NewFanoutStore(durable, bus)
	agent := &managerTestAgent{
		streams: make(map[string]chan zenforge.Event),
		ctxs:    make(map[string]context.Context),
		store:   store,
	}
	return NewRunManager(agent, store, bus, opts), agent, store
}

func (a *managerTestAgent) Stream(ctx context.Context, task zenforge.Task) (<-chan zenforge.Event, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.streams[task.RunID]; ok {
		return nil, fmt.Errorf("duplicate agent stream")
	}
	ch := make(chan zenforge.Event, 1)
	a.streams[task.RunID] = ch
	a.ctxs[task.RunID] = ctx
	go func() {
		<-ctx.Done()
		a.mu.Lock()
		current := a.streams[task.RunID]
		a.mu.Unlock()
		if current != nil {
			a.finish(task.RunID, zenforge.EventRunCancelled)
		}
	}()
	return ch, nil
}

func (a *managerTestAgent) Resume(ctx context.Context, runID string) (<-chan zenforge.Event, error) {
	return a.Stream(ctx, zenforge.Task{RunID: runID})
}

func (a *managerTestAgent) send(runID string, event zenforge.Event) {
	if err := a.store.Append(context.Background(), event); err != nil {
		panic(err)
	}
	a.mu.Lock()
	ch := a.streams[runID]
	a.mu.Unlock()
	ch <- event
}

func (a *managerTestAgent) finish(runID string, eventType zenforge.EventType) {
	a.mu.Lock()
	ch := a.streams[runID]
	if ch == nil {
		a.mu.Unlock()
		return
	}
	delete(a.streams, runID)
	a.mu.Unlock()
	event := zenforge.NewEvent(eventType, runID, nil)
	if eventType == zenforge.EventRunCancelled {
		event.Payload["error"] = context.Canceled.Error()
		a.mu.Lock()
		if ctx := a.ctxs[runID]; ctx != nil && ctx.Err() != nil {
			event.Payload["error"] = ctx.Err().Error()
		}
		a.mu.Unlock()
	}
	if err := a.store.Append(context.Background(), event); err != nil {
		panic(err)
	}
	ch <- event
	close(ch)
}

func (a *managerTestAgent) cancelled(runID string) bool {
	a.mu.Lock()
	ctx := a.ctxs[runID]
	a.mu.Unlock()
	return ctx != nil && ctx.Err() != nil
}

func waitStatus(t *testing.T, manager *RunManager, runID string, want RunStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := manager.Get(runID)
		if err == nil && info.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	info, err := manager.Get(runID)
	t.Fatalf("status = %+v, err=%v, want %s", info, err, want)
}

func waitLatest(t *testing.T, store eventlog.Store, runID string, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := store.LatestSeq(context.Background(), runID)
		if err == nil && got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("latest sequence did not reach %d", want)
}

func closeManager(t *testing.T, manager *RunManager) {
	t.Helper()
	if err := manager.Close(context.Background()); err != nil {
		t.Errorf("Close = %v", err)
	}
}
