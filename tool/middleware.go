package tool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func Chain(middleware ...Middleware) Middleware {
	return func(next Invoker) Invoker {
		for i := len(middleware) - 1; i >= 0; i-- {
			if middleware[i] == nil {
				continue
			}
			next = middleware[i](next)
		}
		return next
	}
}

func Timeout(timeout time.Duration) Middleware {
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			if timeout <= 0 {
				return next.Invoke(ctx, call)
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			result, err := next.Invoke(ctx, call)
			if ctx.Err() == context.DeadlineExceeded {
				result = normalizeResult(Result{Error: ErrTimeout.Error(), ExitCode: 1}, ErrTimeout)
				return result, ErrTimeout
			}
			return result, err
		})
	}
}

// TimeoutPolicy enforces each tool's declared cooperative timeout
// budget (TimeoutDeclarer), falling back to timeout for tools that
// declare none. It mirrors the DSH tool-call timeout policy: the budget
// is armed from the tool declaration rather than the model arguments,
// and an expiry replaces the outcome with a structured TOOL_TIMEOUT
// result carrying the budget in milliseconds. Resolution failures and
// zero budgets keep the fallback; a non-positive fallback means no
// deadline. A tool that ignores its context cannot be interrupted — the
// wrapper reports the expiry once the call settles, exactly as the
// reference wrapper awaits the tool promise instead of racing it.
func TimeoutPolicy(resolve func(name string) (Tool, bool), fallback time.Duration) Middleware {
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			budget := fallback
			if resolve != nil {
				if resolved, ok := resolve(call.Name); ok {
					if declared := TimeoutBudgetOf(resolved); declared > 0 {
						budget = declared
					}
				}
			}
			if budget <= 0 {
				return next.Invoke(ctx, call)
			}
			timeoutCtx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			result, err := next.Invoke(timeoutCtx, call)
			if timeoutCtx.Err() == context.DeadlineExceeded {
				return timeoutResult(budget), ErrTimeout
			}
			return result, err
		})
	}
}

// timeoutResult is the structured outcome for an expired tool deadline.
func timeoutResult(budget time.Duration) Result {
	result := normalizeResult(Result{
		Error:    fmt.Sprintf("%s after %s", ErrTimeout.Error(), budget),
		ExitCode: 1,
		Metadata: map[string]any{
			"code":      TimeoutCode,
			"timeoutMs": budget.Milliseconds(),
		},
	}, ErrTimeout)
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["code"] = TimeoutCode
	result.Metadata["timeoutMs"] = budget.Milliseconds()
	return result
}

func Retry(maxAttempts int) Middleware {
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			if maxAttempts <= 1 {
				return next.Invoke(ctx, call)
			}
			var result Result
			var err error
			for attempt := 1; attempt <= maxAttempts; attempt++ {
				result, err = next.Invoke(ctx, call)
				if err == nil || !IsRetryable(err) || ctx.Err() != nil {
					return result, err
				}
			}
			return result, err
		})
	}
}

func MaxCalls(max int) Middleware {
	var mu sync.Mutex
	counts := map[string]int{}
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			if max <= 0 {
				return next.Invoke(ctx, call)
			}
			runID := call.RunID
			if runID == "" {
				runID = "<unscoped>"
			}
			mu.Lock()
			counts[runID]++
			current := counts[runID]
			mu.Unlock()
			if current > max {
				return Result{Error: ErrBudgetExceeded.Error(), ExitCode: 1}, ErrBudgetExceeded
			}
			return next.Invoke(ctx, call)
		})
	}
}

func MaxOutputBytes(max int) Middleware {
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			result, err := next.Invoke(ctx, call)
			if max <= 0 || len(result.Output) <= max {
				return result, err
			}
			originalBytes := len(result.Output)
			result.Output = truncateUTF8(result.Output, max)
			if result.Metadata == nil {
				result.Metadata = map[string]any{}
			}
			if result.Error == "" {
				result.Error = ErrOutputTooLarge.Error()
			}
			if result.ExitCode == 0 {
				result.ExitCode = 1
			}
			result.Metadata["truncated"] = true
			result.Metadata["originalBytes"] = originalBytes
			return result, ErrOutputTooLarge
		})
	}
}

func RecoverPanic() Middleware {
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (result Result, err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("tool panic: %v", recovered)
					result = Result{Error: err.Error(), ExitCode: 1}
				}
			}()
			return next.Invoke(ctx, call)
		})
	}
}

func RedactArguments(keys ...string) Middleware {
	redacted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		redacted[strings.ToLower(key)] = struct{}{}
	}
	return func(next Invoker) Invoker {
		return InvokerFunc(func(ctx context.Context, call Call) (Result, error) {
			if len(redacted) == 0 {
				return next.Invoke(ctx, call)
			}
			call.RedactedArgumentKeys = append(append([]string(nil), call.RedactedArgumentKeys...), keys...)
			return next.Invoke(ctx, call)
		})
	}
}

func truncateUTF8(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	out := value[:max]
	for !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out
}
