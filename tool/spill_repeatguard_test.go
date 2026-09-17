package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSpillKeepsSmallResultsInline(t *testing.T) {
	invoker := Spill(SpillConfig{MaxInlineBytes: 1024, Dir: t.TempDir()})(InvokerFunc(func(context.Context, Call) (Result, error) {
		return Result{Output: "small output"}, nil
	}))
	result, err := invoker.Invoke(context.Background(), Call{RunID: "run_1", ID: "call_1"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.Output != "small output" || result.Metadata != nil {
		t.Fatalf("small result was modified: %#v", result)
	}
}

func TestSpillMovesOversizedOutputToPrivateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spill")
	var payload strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&payload, "line-%04d\n", i)
	}
	full := payload.String()
	invoker := Spill(SpillConfig{MaxInlineBytes: 1000, HeadBytes: 400, TailBytes: 200, Dir: dir})(InvokerFunc(func(context.Context, Call) (Result, error) {
		return Result{Output: full}, nil
	}))
	result, err := invoker.Invoke(context.Background(), Call{RunID: "run_1", ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"seq"}`)})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if len(result.Output) >= len(full) {
		t.Fatalf("oversized output stayed inline: %d bytes", len(result.Output))
	}
	if !strings.HasPrefix(result.Output, "line-0000\n") {
		t.Fatalf("head preview missing: %.40q", result.Output)
	}
	if !strings.HasSuffix(result.Output, "line-2999\n") {
		t.Fatalf("tail preview missing: %.40q", result.Output[len(result.Output)-40:])
	}
	if !strings.Contains(result.Output, fmt.Sprintf("[output spilled: full %d bytes written to", len(full))) {
		t.Fatalf("spill footer missing: %q", result.Output)
	}
	path, _ := result.Metadata["spillPath"].(string)
	if path == "" || !strings.HasPrefix(path, dir) {
		t.Fatalf("spillPath metadata missing or misplaced: %#v", result.Metadata)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	if string(stored) != full {
		t.Fatalf("spill file content mismatch: %d bytes", len(stored))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat spill file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("spill file mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat spill dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("spill dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	if result.Metadata["spilled"] != true || result.Metadata["originalBytes"] != len(full) {
		t.Fatalf("spill metadata incomplete: %#v", result.Metadata)
	}

	// A retried identical call overwrites the same file (idempotent resume).
	result2, err := invoker.Invoke(context.Background(), Call{RunID: "run_1", ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"seq"}`)})
	if err != nil {
		t.Fatalf("second Invoke returned error: %v", err)
	}
	if result2.Metadata["spillPath"] != path {
		t.Fatalf("retry wrote a second spill file: %v vs %v", result2.Metadata["spillPath"], path)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read spill dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("spill dir has %d files, want 1", len(entries))
	}
}

func TestSpillFailsSoftWhenStoreUnavailable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	invoker := Spill(SpillConfig{MaxInlineBytes: 100, HeadBytes: 40, TailBytes: 20, Dir: filepath.Join(blocker, "spill")})(InvokerFunc(func(context.Context, Call) (Result, error) {
		return Result{Output: strings.Repeat("x", 5000)}, nil
	}))
	result, err := invoker.Invoke(context.Background(), Call{RunID: "run_1", ID: "call_1"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if len(result.Output) > 512 {
		t.Fatalf("fallback did not bound the output: %d bytes", len(result.Output))
	}
	if !strings.Contains(result.Output, "spill store unavailable") {
		t.Fatalf("fallback marker missing: %q", result.Output)
	}
	if result.Metadata["spilled"] != false {
		t.Fatalf("fallback metadata wrong: %#v", result.Metadata)
	}
}

func TestSpillPreviewIsRuneSafe(t *testing.T) {
	invoker := Spill(SpillConfig{MaxInlineBytes: 300, HeadBytes: 100, TailBytes: 50, Dir: t.TempDir()})(InvokerFunc(func(context.Context, Call) (Result, error) {
		return Result{Output: strings.Repeat("日本語テキスト", 500)}, nil
	}))
	result, err := invoker.Invoke(context.Background(), Call{RunID: "run_1", ID: "call_1"})
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if !utf8.ValidString(result.Output) {
		t.Fatalf("preview split a rune")
	}
}

func countingInvoker(output string, calls *int) Invoker {
	return InvokerFunc(func(context.Context, Call) (Result, error) {
		*calls++
		return Result{Output: output}, nil
	})
}

func repeatCall(runID, name, args string) Call {
	return Call{RunID: runID, ID: "call_" + name, Name: name, Arguments: json.RawMessage(args)}
}

func TestRepeatGuardRemindsAfterThresholdAndEscalates(t *testing.T) {
	calls := 0
	invoker := RepeatGuard()(countingInvoker("ok", &calls))
	call := repeatCall("run_1", "read", `{"path":"a.txt"}`)
	for i := 1; i <= 8; i++ {
		result, err := invoker.Invoke(context.Background(), call)
		if err != nil {
			t.Fatalf("invoke %d returned error: %v", i, err)
		}
		switch i {
		case 1, 2:
			if result.Output != "ok" || result.Metadata != nil {
				t.Fatalf("call %d got an early reminder: %#v", i, result)
			}
		case 3:
			if !strings.Contains(result.Output, "[repeat-guard: 3 consecutive identical calls") {
				t.Fatalf("call 3 missing reminder: %q", result.Output)
			}
			if result.Metadata["repeatCount"] != 3 {
				t.Fatalf("call 3 metadata = %#v", result.Metadata)
			}
		case 5:
			if !strings.Contains(result.Output, "unlikely to help") {
				t.Fatalf("call 5 missing escalation: %q", result.Output)
			}
		case 8:
			if !strings.Contains(result.Output, "stop repeating this exact call") {
				t.Fatalf("call 8 missing final escalation: %q", result.Output)
			}
		}
		if strings.HasPrefix(result.Output, "[repeat-guard") && i >= 3 && !strings.Contains(result.Output, "ok") {
			t.Fatalf("call %d lost the real result: %q", i, result.Output)
		}
	}
	if calls != 8 {
		t.Fatalf("guard swallowed calls: %d", calls)
	}
}

func TestRepeatGuardResetsOnDifferentCall(t *testing.T) {
	calls := 0
	invoker := RepeatGuard()(countingInvoker("ok", &calls))
	callA := repeatCall("run_1", "read", `{"path":"a.txt"}`)
	callB := repeatCall("run_1", "read", `{"path":"b.txt"}`)
	for i := 0; i < 2; i++ {
		if _, err := invoker.Invoke(context.Background(), callA); err != nil {
			t.Fatalf("invoke returned error: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		result, err := invoker.Invoke(context.Background(), callB)
		if err != nil {
			t.Fatalf("invoke returned error: %v", err)
		}
		if strings.Contains(result.Output, "repeat-guard") {
			t.Fatalf("streak was not reset by a different call: %q", result.Output)
		}
	}
	result, err := invoker.Invoke(context.Background(), callB)
	if err != nil {
		t.Fatalf("invoke returned error: %v", err)
	}
	if !strings.Contains(result.Output, "[repeat-guard: 3 consecutive") {
		t.Fatalf("new streak not counted: %q", result.Output)
	}
}

func TestRepeatGuardScopesByRun(t *testing.T) {
	calls := 0
	invoker := RepeatGuard()(countingInvoker("ok", &calls))
	callA := repeatCall("run_a", "read", `{"path":"a.txt"}`)
	for i := 0; i < 3; i++ {
		if _, err := invoker.Invoke(context.Background(), callA); err != nil {
			t.Fatalf("invoke returned error: %v", err)
		}
	}
	result, err := invoker.Invoke(context.Background(), repeatCall("run_b", "read", `{"path":"a.txt"}`))
	if err != nil {
		t.Fatalf("invoke returned error: %v", err)
	}
	if strings.Contains(result.Output, "repeat-guard") {
		t.Fatalf("run scoping broken: %q", result.Output)
	}
}
