package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type EventType string

const (
	EventCall   EventType = "tool.call"
	EventResult EventType = "tool.result"
	EventError  EventType = "tool.error"
)

type Event struct {
	Type       EventType
	RunID      string
	ToolCallID string
	ToolName   string
	Arguments  any
	Output     string
	Error      string
	ExitCode   int
	Duration   time.Duration
	Metadata   map[string]any
}

type EventSink func(ctx context.Context, event Event)

type InvokerConfig struct {
	Registry Registry
	Sink     EventSink
}

type DefaultInvoker struct {
	registry Registry
	sink     EventSink
}

func NewInvoker(registry Registry, middleware ...Middleware) Invoker {
	invoker := Invoker(NewDefaultInvoker(InvokerConfig{Registry: registry}))
	return Chain(middleware...)(invoker)
}

func NewDefaultInvoker(config InvokerConfig) *DefaultInvoker {
	return &DefaultInvoker{registry: config.Registry, sink: config.Sink}
}

func (i *DefaultInvoker) Invoke(ctx context.Context, call Call) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Error: err.Error(), ExitCode: 1}, err
	}
	if i.registry == nil {
		return missingToolResult(call.Name), fmt.Errorf("%w: registry is nil", ErrToolNotFound)
	}
	selected, ok := i.registry.Lookup(call.Name)
	if !ok {
		result := missingToolResult(call.Name)
		i.emit(ctx, call, EventError, result, 0)
		return result, fmt.Errorf("%w: %s", ErrToolNotFound, call.Name)
	}

	start := time.Now()
	i.emit(ctx, call, EventCall, Result{}, 0)
	result, err := selected.Call(ctx, call.Arguments, Context{
		RunID:      call.RunID,
		ToolCallID: call.ID,
		Metadata:   cloneMap(call.Metadata),
		Meta:       cloneMap(call.Metadata),
	})
	result = normalizeResult(result, err)
	eventType := EventResult
	if result.Error != "" || result.ExitCode != 0 {
		eventType = EventError
	}
	i.emit(ctx, call, eventType, result, time.Since(start))
	return result, err
}

func (i *DefaultInvoker) emit(ctx context.Context, call Call, eventType EventType, result Result, duration time.Duration) {
	if i.sink == nil {
		return
	}
	i.sink(ctx, Event{
		Type:       eventType,
		RunID:      call.RunID,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Arguments:  jsonValue(call.Arguments),
		Output:     result.Output,
		Error:      result.Error,
		ExitCode:   result.ExitCode,
		Duration:   duration,
		Metadata:   cloneMap(call.Metadata),
	})
}

func normalizeResult(result Result, err error) Result {
	if result.Metadata == nil && result.Meta != nil {
		result.Metadata = cloneMap(result.Meta)
	}
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	if result.Error != "" && result.ExitCode == 0 {
		result.ExitCode = 1
	}
	return result
}

func missingToolResult(name string) Result {
	return Result{Error: fmt.Sprintf("tool %q not found", name), ExitCode: 1}
}

func jsonValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func IsRetryable(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, ErrInvalidArguments) &&
		!errors.Is(err, ErrToolNotFound)
}
