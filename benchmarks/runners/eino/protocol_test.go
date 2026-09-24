package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStatusExitCodeMapping(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{StatusCompleted, 0},
		{StatusPaused, 75},
		{StatusUnsupported, 78},
		{StatusFailed, 1},
	}
	for _, c := range cases {
		got := NewResult(TaskEditFile, PhaseRun, c.status, "").ExitCode()
		if got != c.want {
			t.Errorf("status %q: exit code = %d, want %d", c.status, got, c.want)
		}
	}
	// An unknown status must not be mistaken for success.
	if got := NewResult(TaskEditFile, PhaseRun, "nonsense", "").ExitCode(); got != ExitFailed {
		t.Errorf("unknown status: exit code = %d, want %d", got, ExitFailed)
	}
}

func TestResultJSONShape(t *testing.T) {
	r := NewResult(TaskApproveCommand, PhaseResume, StatusFailed, "line one\nline two")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []string{"task", "phase", "status", "detail", "framework"}
	got := make([]string, 0, len(raw))
	for k := range raw {
		got = append(got, k)
	}
	if len(got) != len(want) {
		t.Fatalf("result has keys %v, want exactly %v", got, want)
	}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Fatalf("result is missing key %q (has %v)", k, got)
		}
	}

	var decoded struct {
		Task      string `json:"task"`
		Phase     string `json:"phase"`
		Status    string `json:"status"`
		Detail    string `json:"detail"`
		Framework string `json:"framework"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal into struct: %v", err)
	}
	if decoded.Task != TaskApproveCommand || decoded.Phase != PhaseResume || decoded.Status != StatusFailed {
		t.Errorf("round trip changed fields: %+v", decoded)
	}
	if strings.Contains(decoded.Detail, "\n") {
		t.Errorf("detail must be one line, got %q", decoded.Detail)
	}
	if !strings.HasPrefix(decoded.Framework, "eino ") {
		t.Errorf("framework = %q, want an \"eino <version>\" string", decoded.Framework)
	}
}

func TestWriteResultRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "result.json")
	want := NewResult(TaskDurableTask, PhaseRun, StatusPaused, "paused once")
	if err := WriteResult(path, want); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got Result
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Error("result file should end with a newline")
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\nb\tc   d\r\n"); got != "a b c d" {
		t.Errorf("oneLine = %q, want %q", got, "a b c d")
	}
	long := strings.Repeat("x", 500)
	if got := oneLine(long); len(got) > 403 || !strings.HasSuffix(got, "...") {
		t.Errorf("oneLine did not bound its output: len=%d tail=%q", len(got), got[len(got)-5:])
	}
}

func baseEnv() map[string]string {
	return map[string]string{
		"BENCH_BASE_URL":  "http://127.0.0.1:1/v1",
		"BENCH_API_KEY":   "test-key",
		"BENCH_MODEL":     "bench-model",
		"BENCH_TASK":      TaskEditFile,
		"BENCH_STATE_DIR": "",
		"BENCH_RESULT":    "",
	}
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	ws := t.TempDir()
	state := t.TempDir()
	res := filepath.Join(t.TempDir(), "result.json")

	t.Run("valid with defaults", func(t *testing.T) {
		m := baseEnv()
		m["BENCH_WORKSPACE"] = ws
		m["BENCH_STATE_DIR"] = state
		m["BENCH_RESULT"] = res
		cfg, err := LoadConfig(envFunc(m))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Phase != PhaseRun {
			t.Errorf("default phase = %q, want %q", cfg.Phase, PhaseRun)
		}
		if cfg.Approval != ApprovalApprove {
			t.Errorf("default approval = %q, want %q", cfg.Approval, ApprovalApprove)
		}
		if cfg.RequirePause {
			t.Error("RequirePause should default to false")
		}
		if cfg.CheckpointID() != "zenforge-bench-"+TaskEditFile {
			t.Errorf("CheckpointID = %q", cfg.CheckpointID())
		}
		if cfg.Query != "" || cfg.QueryFromEnv {
			t.Errorf("BENCH_QUERY default = (%q, %v), want empty and QueryFromEnv false", cfg.Query, cfg.QueryFromEnv)
		}
	})

	t.Run("bench query is read verbatim", func(t *testing.T) {
		const frozen = "Read input.txt, then write its edited contents to out.txt."
		m := baseEnv()
		m["BENCH_WORKSPACE"] = ws
		m["BENCH_STATE_DIR"] = state
		m["BENCH_RESULT"] = res
		m["BENCH_QUERY"] = "  " + frozen + "\n"
		cfg, err := LoadConfig(envFunc(m))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.Query != frozen {
			t.Errorf("Query = %q, want %q", cfg.Query, frozen)
		}
		if !cfg.QueryFromEnv {
			t.Error("QueryFromEnv should be true when BENCH_QUERY is set")
		}
	})

	t.Run("require pause", func(t *testing.T) {
		m := baseEnv()
		m["BENCH_WORKSPACE"] = ws
		m["BENCH_STATE_DIR"] = state
		m["BENCH_RESULT"] = res
		m["BENCH_REQUIRE_PAUSE"] = "1"
		cfg, err := LoadConfig(envFunc(m))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if !cfg.RequirePause {
			t.Error("BENCH_REQUIRE_PAUSE=1 should set RequirePause")
		}
	})

	missing := []string{"BENCH_BASE_URL", "BENCH_MODEL", "BENCH_WORKSPACE", "BENCH_STATE_DIR", "BENCH_RESULT"}
	for _, key := range missing {
		t.Run("missing "+key, func(t *testing.T) {
			m := baseEnv()
			m["BENCH_WORKSPACE"] = ws
			m["BENCH_STATE_DIR"] = state
			m["BENCH_RESULT"] = res
			delete(m, key)
			_, err := LoadConfig(envFunc(m))
			if err == nil {
				t.Fatalf("expected an error for missing %s", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error %q does not name %s", err, key)
			}
		})
	}

	bad := []struct{ key, value string }{
		{"BENCH_TASK", "no-such-task"},
		{"BENCH_PHASE", "middle"},
		{"BENCH_APPROVAL", "maybe"},
		{"BENCH_WORKSPACE", "relative/path"},
		{"BENCH_STATE_DIR", "relative/state"},
	}
	for _, c := range bad {
		t.Run("bad "+c.key, func(t *testing.T) {
			m := baseEnv()
			m["BENCH_WORKSPACE"] = ws
			m["BENCH_STATE_DIR"] = state
			m["BENCH_RESULT"] = res
			m[c.key] = c.value
			if _, err := LoadConfig(envFunc(m)); err == nil {
				t.Fatalf("expected an error for %s=%q", c.key, c.value)
			}
		})
	}
}
