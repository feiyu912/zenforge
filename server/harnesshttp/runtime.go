package harnesshttp

import (
	"context"
	"fmt"
	"reflect"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/server/sse"
)

// RuntimeOptions controls the HTTP and detached-run parts of a Runtime.
type RuntimeOptions struct {
	Access         AccessController
	SSE            sse.Options
	Manager        RunManagerOptions
	ApprovalBuffer int
	LiveBuffer     int
}

// Runtime is the canonical assembly for serving a ZenForge agent with
// detached-run support. The caller retains ownership of the durable store.
type Runtime struct {
	Agent     *zenforge.Agent
	Manager   *RunManager
	Handler   *Handler
	Events    *eventlog.FanoutStore
	Bus       *eventlog.Bus
	Approvals *approval.PendingBroker
}

// NewRuntime wires one event pipeline and approval broker through every
// runtime component. Application-owned agent settings in config are preserved.
func NewRuntime(config zenforge.Config, durable eventlog.Store, opts RuntimeOptions) (*Runtime, error) {
	if nilEventStore(durable) {
		return nil, fmt.Errorf("durable event store is required")
	}
	if opts.ApprovalBuffer < 0 {
		return nil, fmt.Errorf("approval buffer must be non-negative")
	}
	if opts.LiveBuffer < 0 {
		return nil, fmt.Errorf("live buffer must be non-negative")
	}

	bus := eventlog.NewBus()
	events := eventlog.NewFanoutStore(durable, bus)
	approvals := approval.NewPendingBroker(opts.ApprovalBuffer)

	config.Events = events
	config.Approval = approvals
	agent := zenforge.New(config)
	manager := NewRunManager(agent, events, bus, opts.Manager)
	handler := New(agent, opts.SSE)
	handler.Manager = manager
	handler.Events = events
	handler.Bus = bus
	handler.Approvals = approvals
	handler.Access = opts.Access
	handler.LiveBuffer = opts.LiveBuffer

	return &Runtime{
		Agent:     agent,
		Manager:   manager,
		Handler:   handler,
		Events:    events,
		Bus:       bus,
		Approvals: approvals,
	}, nil
}

func nilEventStore(store eventlog.Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Close stops detached work. It does not close the caller-owned durable store.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil || r.Manager == nil {
		return nil
	}
	return r.Manager.Close(ctx)
}
