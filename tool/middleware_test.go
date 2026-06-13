package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMiddlewareChainOrder(t *testing.T) {
	var order []string
	first := func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			order = append(order, "first-before")
			result, err := next.Invoke(ctx, call)
			order = append(order, "first-after")
			return result, err
		})
	}
	second := func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			order = append(order, "second-before")
			result, err := next.Invoke(ctx, call)
			order = append(order, "second-after")
			return result, err
		})
	}
	invoker := Chain(first, second)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		order = append(order, "invoke")
		return Result{}, nil
	}))
	if _, err := invoker.Invoke(context.Background(), Call{}); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	want := []string{"first-before", "second-before", "invoke", "second-after", "first-after"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestRetrySkipsContextCancellation(t *testing.T) {
	calls := 0
	invoker := Retry(3)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		calls++
		return Result{Error: context.Canceled.Error(), ExitCode: 1}, context.Canceled
	}))
	_, err := invoker.Invoke(context.Background(), Call{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetryOnlyRetriesMarkedTransientErrors(t *testing.T) {
	transientCalls := 0
	transient := Retry(3)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		transientCalls++
		if transientCalls < 3 {
			return Result{Error: "temporary", ExitCode: 1}, MarkRetryable(errors.New("temporary"))
		}
		return Result{Output: "ok"}, nil
	}))
	result, err := transient.Invoke(context.Background(), Call{})
	if err != nil || result.Output != "ok" || transientCalls != 3 {
		t.Fatalf("transient retry result=%#v err=%v calls=%d", result, err, transientCalls)
	}

	permanentCalls := 0
	permanent := Retry(3)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		permanentCalls++
		return Result{Error: "permanent", ExitCode: 1}, errors.New("permanent")
	}))
	if _, err := permanent.Invoke(context.Background(), Call{}); err == nil {
		t.Fatal("permanent error was lost")
	}
	if permanentCalls != 1 {
		t.Fatalf("permanent calls = %d, want 1", permanentCalls)
	}
}

func TestMaxCallsIsScopedPerRun(t *testing.T) {
	calls := 0
	invoker := MaxCalls(1)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		calls++
		return Result{Output: call.RunID}, nil
	}))
	if _, err := invoker.Invoke(context.Background(), Call{RunID: "run_1"}); err != nil {
		t.Fatalf("first run_1 call: %v", err)
	}
	if _, err := invoker.Invoke(context.Background(), Call{RunID: "run_1"}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("second run_1 call error = %v", err)
	}
	if _, err := invoker.Invoke(context.Background(), Call{RunID: "run_2"}); err != nil {
		t.Fatalf("first run_2 call: %v", err)
	}
	if calls != 2 {
		t.Fatalf("underlying calls = %d, want 2", calls)
	}
}

func TestMaxOutputBytesTruncatesAndReturnsError(t *testing.T) {
	invoker := MaxOutputBytes(3)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		return Result{Output: "abcdef"}, nil
	}))
	result, err := invoker.Invoke(context.Background(), Call{})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("expected ErrOutputTooLarge, got %v", err)
	}
	if result.Output != "abc" || result.Metadata["truncated"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestMaxOutputBytesPreservesUTF8(t *testing.T) {
	invoker := MaxOutputBytes(4)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		return Result{Output: "你好吗"}, nil
	}))
	result, err := invoker.Invoke(context.Background(), Call{})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("expected ErrOutputTooLarge, got %v", err)
	}
	if result.Output != "你" || !utf8.ValidString(result.Output) {
		t.Fatalf("invalid UTF-8 truncation: %q", result.Output)
	}
}

func TestRedactArgumentsHidesNestedAuditValuesButPreservesToolInput(t *testing.T) {
	capture := &argumentCaptureTool{}
	registry, err := NewRegistry(capture)
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}
	var events []Event
	base := NewDefaultInvoker(InvokerConfig{
		Registry: registry,
		Sink: func(ctx context.Context, event Event) {
			events = append(events, event)
		},
	})
	invoker := RedactArguments("password", "TOKEN")(base)
	arguments := json.RawMessage(`{"username":"kai","password":"secret","nested":{"token":"abc"},"items":[{"Password":"other"}]}`)
	if _, err := invoker.Invoke(context.Background(), Call{ID: "call_1", RunID: "run_1", Name: capture.Name(), Arguments: arguments}); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if string(capture.input) != string(arguments) {
		t.Fatalf("tool input was modified: %s", capture.input)
	}
	if len(events) == 0 {
		t.Fatal("missing audit events")
	}
	got := events[0].Arguments
	want := map[string]any{
		"username": "kai",
		"password": "[REDACTED]",
		"nested":   map[string]any{"token": "[REDACTED]"},
		"items":    []any{map[string]any{"Password": "[REDACTED]"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redacted arguments = %#v, want %#v", got, want)
	}
}

func TestRecoverPanicReturnsToolError(t *testing.T) {
	invoker := RecoverPanic()(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		panic("boom")
	}))
	result, err := invoker.Invoke(context.Background(), Call{})
	if err == nil || result.Error == "" || result.ExitCode == 0 {
		t.Fatalf("expected panic error result, got result=%#v err=%v", result, err)
	}
}

func TestTimeoutReturnsTimeoutError(t *testing.T) {
	invoker := Timeout(time.Nanosecond)(InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}))
	result, err := invoker.Invoke(context.Background(), Call{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if result.Error == "" || result.ExitCode == 0 {
		t.Fatalf("expected timeout result, got %#v", result)
	}
}

type argumentCaptureTool struct {
	input json.RawMessage
}

func (*argumentCaptureTool) Name() string { return "capture_arguments" }

func (*argumentCaptureTool) Description() string { return "Capture arguments" }

func (*argumentCaptureTool) Schema() map[string]any { return nil }

func (t *argumentCaptureTool) Call(ctx context.Context, input json.RawMessage, call Context) (Result, error) {
	t.input = append(json.RawMessage(nil), input...)
	return Result{Output: fmt.Sprint("ok")}, nil
}
