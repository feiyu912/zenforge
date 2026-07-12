package harnesshttp

import (
	"context"
	"database/sql"
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

func TestRunManagerRegistryClaimsAcrossManagersAndKeepsStatus(t *testing.T) {
	durable := eventlogmemory.New()
	bus1 := eventlog.NewBus()
	bus2 := eventlog.NewBus()
	store1 := eventlog.NewFanoutStore(durable, bus1)
	store2 := eventlog.NewFanoutStore(durable, bus2)
	registry := NewMemoryRunRegistry()
	agent1 := newManagerTestAgent(store1)
	agent2 := newManagerTestAgent(store2)
	manager1 := NewRunManager(agent1, store1, bus1, RunManagerOptions{
		Registry: registry, OwnerID: "owner_1", TerminalRetention: 10 * time.Millisecond,
	})
	manager2 := NewRunManager(agent2, store2, bus2, RunManagerOptions{
		Registry: registry, OwnerID: "owner_2", TerminalRetention: -1,
	})
	defer closeManager(t, manager1)
	defer closeManager(t, manager2)

	info, err := manager1.Start(context.Background(), zenforge.Task{RunID: "claimed", Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if info.OwnerID != "owner_1" || info.LeaseUntil == nil {
		t.Fatalf("claimed info = %+v", info)
	}
	if _, err := manager2.Start(context.Background(), zenforge.Task{RunID: "claimed", Input: "hi"}); !errors.Is(err, ErrRunClaimed) {
		t.Fatalf("second Start = %v, want ErrRunClaimed", err)
	}
	agent1.send("claimed", zenforge.NewEvent(zenforge.EventApprovalRequested, "claimed", nil))
	waitStatus(t, manager2, "claimed", RunWaitingApproval)
	agent1.finish("claimed", zenforge.EventRunDone)
	waitStatus(t, manager2, "claimed", RunCompleted)
	time.Sleep(20 * time.Millisecond)
	if _, err := manager1.Get("claimed"); err != nil {
		t.Fatalf("manager1 Get after retention = %v", err)
	}
	listed, err := manager2.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].RunID != "claimed" || listed[0].Status != RunCompleted {
		t.Fatalf("registry list = %+v", listed)
	}
}

func TestRunManagerCancelsAcrossManagersThroughRegistry(t *testing.T) {
	durable := eventlogmemory.New()
	bus1 := eventlog.NewBus()
	bus2 := eventlog.NewBus()
	store1 := eventlog.NewFanoutStore(durable, bus1)
	store2 := eventlog.NewFanoutStore(durable, bus2)
	registry := NewMemoryRunRegistry()
	agent1 := newManagerTestAgent(store1)
	manager1 := NewRunManager(agent1, store1, bus1, RunManagerOptions{
		Registry: registry, OwnerID: "cancel_owner", TerminalRetention: -1,
		LeaseDuration: 100 * time.Millisecond, HeartbeatInterval: 5 * time.Millisecond,
	})
	manager2 := NewRunManager(newManagerTestAgent(store2), store2, bus2, RunManagerOptions{
		Registry: registry, OwnerID: "cancel_requester", TerminalRetention: -1,
		LeaseDuration: 100 * time.Millisecond, HeartbeatInterval: 5 * time.Millisecond,
	})
	defer closeManager(t, manager1)
	defer closeManager(t, manager2)

	if _, err := manager1.Start(context.Background(), zenforge.Task{RunID: "remote_cancel", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := manager2.Cancel("remote_cancel"); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, manager2, "remote_cancel", RunCancelled)
	if !agent1.cancelled("remote_cancel") {
		t.Fatal("owner context was not cancelled")
	}
	if err := manager2.Cancel("remote_cancel"); err != nil {
		t.Fatalf("repeated remote cancel = %v", err)
	}
}

func TestMemoryRunRegistryCancellationIsLeaseFenced(t *testing.T) {
	now := time.Now().UTC()
	registry := NewMemoryRunRegistry()
	lease, err := registry.Claim(context.Background(), RunClaim{
		RunID: "memory_cancel", OwnerID: "owner", Status: RunRunning,
		LeaseUntil: now.Add(time.Minute), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RequestCancel(context.Background(), "memory_cancel"); err != nil {
		t.Fatal(err)
	}
	requested, err := registry.CancelRequested(context.Background(), lease)
	if err != nil || !requested {
		t.Fatalf("requested=%v err=%v", requested, err)
	}
	stale := lease
	stale.Token = "stale"
	if _, err := registry.CancelRequested(context.Background(), stale); !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("stale cancellation read = %v, want ErrRunLeaseLost", err)
	}
}

func TestMemoryRunRegistryResumeClaimPreservesCancellation(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	registry := newMemoryRunRegistryWithClock(func() time.Time { return now })
	_, err := registry.Claim(context.Background(), RunClaim{
		RunID: "memory_resume_cancel", OwnerID: "old", Status: RunRunning,
		LeaseUntil: now.Add(time.Second), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RequestCancel(context.Background(), "memory_resume_cancel"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	lease, err := registry.Claim(context.Background(), RunClaim{
		RunID: "memory_resume_cancel", OwnerID: "new", Status: RunStarting, Resume: true,
		LeaseUntil: now.Add(time.Second), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := registry.CancelRequested(context.Background(), lease)
	if err != nil || !requested {
		t.Fatalf("requested=%v err=%v", requested, err)
	}
}

func TestRunManagerAttachAcrossManagersUsesDurableReplayAndPolling(t *testing.T) {
	durable := eventlogmemory.New()
	bus1 := eventlog.NewBus()
	bus2 := eventlog.NewBus()
	store1 := eventlog.NewFanoutStore(durable, bus1)
	store2 := eventlog.NewFanoutStore(durable, bus2)
	registry := NewMemoryRunRegistry()
	agent1 := newManagerTestAgent(store1)
	manager1 := NewRunManager(agent1, store1, bus1, RunManagerOptions{
		Registry: registry, OwnerID: "owner_1", TerminalRetention: -1,
	})
	manager2 := NewRunManager(newManagerTestAgent(store2), store2, bus2, RunManagerOptions{
		Registry: registry, OwnerID: "owner_2", TerminalRetention: -1,
		Follow: eventlog.FollowOptions{PollInterval: time.Millisecond},
	})
	defer closeManager(t, manager1)
	defer closeManager(t, manager2)

	if _, err := manager1.Start(context.Background(), zenforge.Task{RunID: "cross_attach", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	agent1.send("cross_attach", zenforge.NewEvent(zenforge.EventRunStarted, "cross_attach", nil))
	waitLatest(t, store2, "cross_attach", 1)

	events, errs, err := manager2.Attach(context.Background(), "cross_attach", 0)
	if err != nil {
		t.Fatal(err)
	}
	agent1.send("cross_attach", zenforge.NewEvent(zenforge.EventModelDelta, "cross_attach", map[string]any{"text": "hello"}))
	agent1.finish("cross_attach", zenforge.EventRunDone)

	var got []zenforge.EventType
	for event := range events {
		got = append(got, event.Type)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	want := []zenforge.EventType{zenforge.EventRunStarted, zenforge.EventModelDelta, zenforge.EventRunDone}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("cross-manager attach events = %v, want %v", got, want)
	}
}

func TestRunManagerListLocalSnapshots(t *testing.T) {
	manager, agent, _ := newTestRunManager(t, RunManagerOptions{TerminalRetention: -1})
	defer closeManager(t, manager)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "older", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := manager.Start(context.Background(), zenforge.Task{RunID: "newer", Input: "hi"}); err != nil {
		t.Fatal(err)
	}
	agent.send("older", zenforge.NewEvent(zenforge.EventApprovalRequested, "older", nil))
	waitStatus(t, manager, "older", RunWaitingApproval)
	listed, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].RunID != "older" || listed[0].Status != RunWaitingApproval {
		t.Fatalf("local list = %+v", listed)
	}
}

func TestMemoryRunRegistryLeaseExpiryAllowsClaim(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	registry := newMemoryRunRegistryWithClock(func() time.Time { return now })
	lease, err := registry.Claim(context.Background(), RunClaim{
		RunID: "run_expire", OwnerID: "owner_1", Status: RunStarting,
		LeaseUntil: now.Add(time.Second), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Claim(context.Background(), RunClaim{
		RunID: "run_expire", OwnerID: "owner_2", Status: RunStarting,
		LeaseUntil: now.Add(time.Second), StartedAt: now, UpdatedAt: now,
	}); !errors.Is(err, ErrRunClaimed) {
		t.Fatalf("claim before expiry = %v", err)
	}
	now = now.Add(2 * time.Second)
	lease2, err := registry.Claim(context.Background(), RunClaim{
		RunID: "run_expire", OwnerID: "owner_2", Status: RunStarting,
		LeaseUntil: now.Add(time.Second), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Update(context.Background(), lease, RunInfo{
		Status: RunRunning, LeaseUntil: timePtr(now.Add(time.Second)), UpdatedAt: now,
	}); !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("stale update = %v, want ErrRunLeaseLost", err)
	}
	if err := registry.Update(context.Background(), lease2, RunInfo{
		Status: RunRunning, LeaseUntil: timePtr(now.Add(time.Second)), UpdatedAt: now,
	}); err != nil {
		t.Fatalf("new lease update = %v", err)
	}
}

func TestSQLiteRunRegistryClaimsAcrossConnections(t *testing.T) {
	path := t.TempDir() + "/runs.db"
	registry1, err := OpenSQLiteRunRegistry(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry1.Close()
	registry2, err := OpenSQLiteRunRegistry(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry2.Close()
	now := time.Now().UTC()
	lease, err := registry1.Claim(context.Background(), RunClaim{
		RunID: "sqlite_claim", OwnerID: "owner_1", Status: RunStarting,
		LeaseUntil: now.Add(time.Minute), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry2.Claim(context.Background(), RunClaim{
		RunID: "sqlite_claim", OwnerID: "owner_2", Status: RunStarting,
		LeaseUntil: now.Add(time.Minute), StartedAt: now, UpdatedAt: now,
	}); !errors.Is(err, ErrRunClaimed) {
		t.Fatalf("second sqlite claim = %v", err)
	}
	if err := registry1.Release(context.Background(), lease, RunInfo{
		RunID: "sqlite_claim", OwnerID: "owner_1", Status: RunCompleted,
		StartedAt: now, UpdatedAt: now, FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	info, err := registry2.Get(context.Background(), "sqlite_claim")
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != RunCompleted || info.LeaseUntil != nil {
		t.Fatalf("sqlite info = %+v", info)
	}
	listed, err := registry2.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].RunID != "sqlite_claim" || listed[0].Status != RunCompleted {
		t.Fatalf("sqlite list = %+v", listed)
	}
}

func TestSQLiteRunRegistrySharesCancellationAcrossConnections(t *testing.T) {
	path := t.TempDir() + "/cancel.db"
	owner, err := OpenSQLiteRunRegistry(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	requester, err := OpenSQLiteRunRegistry(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer requester.Close()
	now := time.Now().UTC()
	lease, err := owner.Claim(context.Background(), RunClaim{
		RunID: "sqlite_cancel", OwnerID: "owner", Status: RunRunning,
		LeaseUntil: now.Add(time.Minute), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := requester.RequestCancel(context.Background(), "sqlite_cancel"); err != nil {
		t.Fatal(err)
	}
	requested, err := owner.CancelRequested(context.Background(), lease)
	if err != nil || !requested {
		t.Fatalf("requested=%v err=%v", requested, err)
	}
	if err := owner.Release(context.Background(), lease, RunInfo{
		Status: RunCancelled, StartedAt: now, UpdatedAt: now, FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := requester.RequestCancel(context.Background(), "sqlite_cancel"); err != nil {
		t.Fatalf("repeated terminal cancellation = %v", err)
	}
}

func TestSQLiteRunRegistryMigratesExistingSchemaForCancellation(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE detached_runs (
		run_id TEXT PRIMARY KEY, status TEXT NOT NULL, error TEXT NOT NULL,
		owner_id TEXT NOT NULL, lease_token TEXT NOT NULL, lease_until TEXT NOT NULL,
		started_at TEXT NOT NULL, updated_at TEXT NOT NULL, finished_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err := OpenSQLiteRunRegistry(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	now := time.Now().UTC()
	lease, err := registry.Claim(context.Background(), RunClaim{
		RunID: "migrated_cancel", OwnerID: "owner", Status: RunRunning,
		LeaseUntil: now.Add(time.Minute), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RequestCancel(context.Background(), "migrated_cancel"); err != nil {
		t.Fatal(err)
	}
	requested, err := registry.CancelRequested(context.Background(), lease)
	if err != nil || !requested {
		t.Fatalf("requested=%v err=%v", requested, err)
	}
}

func TestSQLiteRunRegistryResumeClaimPreservesCancellation(t *testing.T) {
	registry, err := OpenSQLiteRunRegistry(context.Background(), t.TempDir()+"/resume-cancel.db")
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	now := time.Now().UTC()
	_, err = registry.Claim(context.Background(), RunClaim{
		RunID: "sqlite_resume_cancel", OwnerID: "old", Status: RunRunning,
		LeaseUntil: now.Add(-time.Second), StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RequestCancel(context.Background(), "sqlite_resume_cancel"); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Claim(context.Background(), RunClaim{
		RunID: "sqlite_resume_cancel", OwnerID: "new", Status: RunStarting, Resume: true,
		LeaseUntil: now.Add(time.Minute), StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := registry.CancelRequested(context.Background(), lease)
	if err != nil || !requested {
		t.Fatalf("requested=%v err=%v", requested, err)
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
	agent := newManagerTestAgent(store)
	return NewRunManager(agent, store, bus, opts), agent, store
}

func newManagerTestAgent(store eventlog.Store) *managerTestAgent {
	return &managerTestAgent{
		streams: make(map[string]chan zenforge.Event),
		ctxs:    make(map[string]context.Context),
		store:   store,
	}
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
