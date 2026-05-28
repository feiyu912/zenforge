package tool

import (
	"context"
	"errors"
	"testing"
	"time"
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
