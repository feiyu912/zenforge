package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/checkpoint"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/planner"
)

func TestMainVersion(t *testing.T) {
	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"version"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != Version {
		t.Fatalf("unexpected version output: %q", stdout.String())
	}
}

func TestExitCodeClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "runtime", err: errors.New("boom"), want: exitRuntimeError},
		{name: "invalid usage", err: fmt.Errorf("bad flag: %w", errInvalidUsage), want: exitInvalidUsage},
		{name: "cancelled event", err: fmt.Errorf("stopped: %w", errRunCancelled), want: exitRunCancelled},
		{name: "cancelled context", err: context.Canceled, want: exitRunCancelled},
		{name: "approval rejected", err: fmt.Errorf("tool denied: %w", errApprovalRejected), want: exitApprovalRejected},
		{name: "unsupported resume", err: fmt.Errorf("future state: %w", errUnsupportedResume), want: exitUnsupportedResume},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestRenderStreamClassifiesOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		events []zenforge.Event
		want   error
	}{
		{
			name: "approval rejection survives run completion",
			events: []zenforge.Event{
				zenforge.NewEvent(zenforge.EventApprovalResolved, "run_1", map[string]any{"action": "reject"}),
				zenforge.NewEvent(zenforge.EventRunDone, "run_1", map[string]any{"output": "continued"}),
			},
			want: errApprovalRejected,
		},
		{
			name: "cancellation takes precedence",
			events: []zenforge.Event{
				zenforge.NewEvent(zenforge.EventApprovalResolved, "run_1", map[string]any{"action": "reject"}),
				zenforge.NewEvent(zenforge.EventRunCancelled, "run_1", map[string]any{"error": "context canceled"}),
			},
			want: errRunCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan zenforge.Event, len(tt.events))
			for _, event := range tt.events {
				events <- event
			}
			close(events)
			if err := renderStream(io.Discard, events); !errors.Is(err, tt.want) {
				t.Fatalf("renderStream error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRunRequiresInputBeforeAPIKey(t *testing.T) {
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"run"}, IO{Stderr: &stderr})
	if code != exitInvalidUsage {
		t.Fatalf("code = %d, want %d", code, exitInvalidUsage)
	}
	if !strings.Contains(stderr.String(), "run input is required") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestCLIReportsUsefulArgumentErrors(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "unknown command",
			args:       []string{"wat"},
			wantCode:   2,
			wantStderr: "usage: zenforge",
		},
		{
			name:       "resume missing run id",
			args:       []string{"resume"},
			wantCode:   2,
			wantStderr: "resume requires run id",
		},
		{
			name:       "code missing repository",
			args:       []string{"code"},
			wantCode:   2,
			wantStderr: "code repository path is required",
		},
		{
			name:       "code missing input",
			args:       []string{"code", "."},
			wantCode:   2,
			wantStderr: "code input is required",
		},
		{
			name:       "events missing run id",
			args:       []string{"events"},
			wantCode:   2,
			wantStderr: "events requires run id",
		},
		{
			name:       "runs rejects positional args",
			args:       []string{"runs", "run_1"},
			wantCode:   2,
			wantStderr: "runs does not accept positional arguments",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Main(context.Background(), tt.args, IO{Stderr: &stderr})
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d; stderr=%q", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestMainClassifiesInvalidUsageAndRuntimeErrors(t *testing.T) {
	tests := []struct {
		name       string
		args       func(t *testing.T) []string
		wantCode   int
		wantStderr string
	}{
		{
			name: "unknown flag",
			args: func(*testing.T) []string {
				return []string{"run", "--unknown", "task"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "flag provided but not defined",
		},
		{
			name: "invalid config json",
			args: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "invalid.json")
				if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
					t.Fatalf("WriteFile returned error: %v", err)
				}
				return []string{"run", "--config", path, "task"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "parse config",
		},
		{
			name: "invalid config field",
			args: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "invalid.json")
				data, err := json.Marshal(configFile{Approval: approvalConfig{Mode: "sometimes"}})
				if err != nil {
					t.Fatalf("Marshal returned error: %v", err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatalf("WriteFile returned error: %v", err)
				}
				return []string{"run", "--config", path, "task"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "approval.mode",
		},
		{
			name: "invalid provider flag",
			args: func(t *testing.T) []string {
				t.Setenv("OPENAI_API_KEY", "test")
				return []string{"run", "--provider", "nonsense", "task"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "unknown model provider",
		},
		{
			name: "invalid approval flag",
			args: func(*testing.T) []string {
				return []string{"run", "--approve", "nonsense", "task"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "unknown approval mode",
		},
		{
			name: "invalid events checkpoint type",
			args: func(*testing.T) []string {
				return []string{"events", "--checkpoint-type", "nonsense", "run_1"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "unknown checkpoint type",
		},
		{
			name: "invalid runs checkpoint type",
			args: func(*testing.T) []string {
				return []string{"runs", "--checkpoint-type", "nonsense"}
			},
			wantCode:   exitInvalidUsage,
			wantStderr: "unknown checkpoint type",
		},
		{
			name: "checkpoint not found remains runtime",
			args: func(t *testing.T) []string {
				return []string{"resume", "--checkpoint-dir", t.TempDir(), "run_missing"}
			},
			wantCode:   exitRuntimeError,
			wantStderr: "checkpoint not found",
		},
		{
			name: "model setup remains runtime",
			args: func(t *testing.T) []string {
				t.Setenv("OPENAI_API_KEY", "")
				return []string{"run", "--checkpoint-dir", t.TempDir(), "task"}
			},
			wantCode:   exitRuntimeError,
			wantStderr: "OPENAI_API_KEY is not set",
		},
		{
			name: "database open failure remains runtime",
			args: func(t *testing.T) []string {
				return []string{"runs", "--checkpoint-type", "sqlite", "--checkpoint-dir", t.TempDir()}
			},
			wantCode:   exitRuntimeError,
			wantStderr: "error:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Main(context.Background(), tt.args(t), IO{Stdout: io.Discard, Stderr: &stderr})
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d; stderr=%q", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestCodeUsesPositionalRepositoryAsWorkspace(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	repository := t.TempDir()
	otherWorkspace := t.TempDir()
	const marker = "from-positional-repository"
	if err := os.WriteFile(filepath.Join(repository, "project.txt"), []byte(marker), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var requests int
	var secondRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll returned error: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"workspace_read\",\"arguments\":\"{\\\"path\\\":\\\"project.txt\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
			return
		}
		secondRequest = string(body)
		_, _ = fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"repository read\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{
		"code",
		"--base-url", server.URL,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--no-shell",
		"--workspace", otherWorkspace,
		repository,
		"inspect project.txt",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if exitCode != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !strings.Contains(secondRequest, marker) {
		t.Fatalf("second model request did not contain positional repository content: %s", secondRequest)
	}
	if !strings.Contains(stdout.String(), "tool workspace_read") || !strings.Contains(stdout.String(), "repository read") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestCodeUsesPositionalRepositoryAsShellWorkingDirectory(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	repository := t.TempDir()
	otherWorkspace := t.TempDir()

	var requests int
	var secondRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll returned error: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_shell\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\",\\\"description\\\":\\\"verify repository working directory\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
			return
		}
		secondRequest = string(body)
		_, _ = fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"shell complete\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{
		"code",
		"--base-url", server.URL,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--approve", "always",
		"--shell-allow", "pwd",
		"--workspace", otherWorkspace,
		repository,
		"print working directory",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if exitCode != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !strings.Contains(secondRequest, repository) {
		t.Fatalf("second model request did not contain repository working directory %q: %s", repository, secondRequest)
	}
	if strings.Contains(secondRequest, otherWorkspace) {
		t.Fatalf("second model request unexpectedly contained overridden workspace %q: %s", otherWorkspace, secondRequest)
	}
	if !strings.Contains(stdout.String(), "tool shell") || !strings.Contains(stdout.String(), "shell complete") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestRunKeepsWorkspaceAndShellWorkingDirectoryIndependent(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	workspaceDir := t.TempDir()
	shellDir := t.TempDir()
	const marker = "workspace-root-marker"
	if err := os.WriteFile(filepath.Join(workspaceDir, "project.txt"), []byte(marker), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "zenforge.json")
	configData, err := json.Marshal(configFile{
		Workspace: workspaceConfig{Root: workspaceDir},
		Shell:     shellConfig{WorkingDir: shellDir, Allow: []string{"pwd"}},
		Approval:  approvalConfig{Mode: "always"},
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, configData, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var requests int
	var finalRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("ReadAll returned error: %v", readErr)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"workspace_read\",\"arguments\":\"{\\\"path\\\":\\\"project.txt\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		case 2:
			if !strings.Contains(string(body), marker) {
				t.Errorf("workspace result missing from model request: %s", body)
			}
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_shell\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\",\\\"description\\\":\\\"print cwd\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		default:
			finalRequest = string(body)
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"done\"}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		}
	}))
	defer server.Close()

	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"run", "--config", configPath, "--base-url", server.URL,
		"--checkpoint-dir", t.TempDir(), "--planning", "disabled", "inspect",
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if requests != 3 || !strings.Contains(finalRequest, shellDir) {
		t.Fatalf("requests = %d, shell working directory %q missing from final request: %s", requests, shellDir, finalRequest)
	}
}

func TestCodeRejectsNonDirectoryRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("not a repository"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{"code", path, "inspect"}, IO{Stderr: &stderr})
	if exitCode != exitInvalidUsage {
		t.Fatalf("code = %d, want %d", exitCode, exitInvalidUsage)
	}
	if !strings.Contains(stderr.String(), "is not a directory") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestCodeRejectsMissingRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	var stderr bytes.Buffer
	exitCode := Main(context.Background(), []string{"code", path, "inspect"}, IO{Stderr: &stderr})
	if exitCode != exitInvalidUsage {
		t.Fatalf("code = %d, want %d", exitCode, exitInvalidUsage)
	}
	if !strings.Contains(stderr.String(), "resolve repository path") || !strings.Contains(stderr.String(), "no such file") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunStreamsOpenAICompatibleEndpoint(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	var gotPath string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"cli\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\" ok\"},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"run",
		"--base-url", server.URL,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--no-shell",
		"hello",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if gotPath != "/chat/completions" || gotAuth != "Bearer test" {
		t.Fatalf("unexpected request path/auth: %q %q", gotPath, gotAuth)
	}
	output := stdout.String()
	if !strings.Contains(output, "run ") || !strings.Contains(output, "cli ok") || !strings.Contains(output, "done") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestMainReturnsRunCancelledExitCode(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	code := Main(ctx, []string{
		"run", "--checkpoint-dir", t.TempDir(), "--planning", "disabled", "--no-shell", "cancel",
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if code != exitRunCancelled {
		t.Fatalf("code = %d, want %d; stderr=%q", code, exitRunCancelled, stderr.String())
	}
}

func TestMainReturnsApprovalRejectedExitCode(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_shell\",\"type\":\"function\",\"function\":{\"name\":\"shell\",\"arguments\":\"{\\\"command\\\":\\\"git status\\\",\\\"description\\\":\\\"inspect status\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
			return
		}
		_, _ = fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"continued\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"run", "--base-url", server.URL, "--checkpoint-dir", t.TempDir(),
		"--planning", "disabled", "--shell-allow", "pwd", "reject",
	}, IO{Stdin: strings.NewReader("2\n"), Stdout: &stdout, Stderr: &stderr})
	if code != exitApprovalRejected {
		t.Fatalf("code = %d, want %d; stderr=%q", code, exitApprovalRejected, stderr.String())
	}
	if requests != 2 || !strings.Contains(stdout.String(), "continued") || !strings.Contains(stdout.String(), "done") {
		t.Fatalf("rejected run did not continue to run.done: requests=%d stdout=%q", requests, stdout.String())
	}
	if !strings.Contains(stderr.String(), "2. Reject") {
		t.Fatalf("approval rejection was not prompted: stderr=%q", stderr.String())
	}
}

func TestRunWorkspaceWriteRequiresReadSnapshot(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	workspaceDir := t.TempDir()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_write\",\"type\":\"function\",\"function\":{\"name\":\"workspace_write\",\"arguments\":\"{\\\"path\\\":\\\"new.txt\\\",\\\"content\\\":\\\"hello\\\",\\\"description\\\":\\\"test write\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		default:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"write blocked\"}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"run",
		"--base-url", server.URL,
		"--checkpoint-dir", t.TempDir(),
		"--planning", "disabled",
		"--no-shell",
		"--workspace", workspaceDir,
		"write without reading first",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace_write should not create file before read snapshot; stat err=%v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "tool workspace_write") || !strings.Contains(output, "write blocked") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunWorkspaceReadHonorsConfigLimit(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "large.txt"), []byte("large"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{
		Workspace: workspaceConfig{Root: workspaceDir, MaxReadBytes: 1, MaxWriteBytes: 1},
		Shell:     shellConfig{Enabled: boolPtr(false)},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile config returned error: %v", err)
	}

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"workspace_read\",\"arguments\":\"{\\\"path\\\":\\\"large.txt\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		default:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"read blocked\"}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"run",
		"--config", configPath,
		"--base-url", server.URL,
		"--checkpoint-dir", filepath.Join(dir, "runs"),
		"--planning", "disabled",
		"read too-large file",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	output := stdout.String()
	if !strings.Contains(output, "tool workspace_read") || !strings.Contains(output, "read blocked") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunWorkspaceReadHonorsConfigRoots(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(filepath.Join(workspaceDir, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll docs returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceDir, "tmp"), 0o755); err != nil {
		t.Fatalf("MkdirAll tmp returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "tmp", "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile secret returned error: %v", err)
	}
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{
		Workspace: workspaceConfig{Root: workspaceDir, ReadRoots: []string{"docs"}},
		Shell:     shellConfig{Enabled: boolPtr(false)},
		Approval:  approvalConfig{Mode: "never"},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile config returned error: %v", err)
	}

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 1:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_read\",\"type\":\"function\",\"function\":{\"name\":\"workspace_read\",\"arguments\":\"{\\\"path\\\":\\\"tmp/secret.txt\\\"}\"}}]}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		default:
			_, _ = fmt.Fprint(w,
				"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"policy blocked\"}}]}\n\n"+
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
					"data: [DONE]\n\n",
			)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"run",
		"--config", configPath,
		"--base-url", server.URL,
		"--checkpoint-dir", filepath.Join(dir, "runs"),
		"--planning", "disabled",
		"read outside configured root",
	}, IO{Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	output := stdout.String()
	if !strings.Contains(output, "tool workspace_read") || !strings.Contains(output, "policy blocked") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestEventsPrintsTimeline(t *testing.T) {
	dir := t.TempDir()
	store := eventlogjsonl.New(dir)
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "run_1", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventToolCall, "run_1", map[string]any{
		"toolName":  "workspace_grep",
		"arguments": map[string]any{"pattern": "TODO"},
	})); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"events", "--checkpoint-dir", dir, "run_1"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "run run_1 started") || !strings.Contains(output, "tool workspace_grep") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestEventsLoadsCheckpointDirFromConfig(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "runs")
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{Checkpoint: checkpointConfig{Path: runDir}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	store := eventlogjsonl.New(runDir)
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "run_config", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"events", "--config", configPath, "run_config"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run run_config started") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestEventsCanReadSQLiteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := eventlogsqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	if err := store.Append(context.Background(), zenforge.NewEvent(zenforge.EventRunStarted, "run_sqlite", nil)); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"events", "--checkpoint-type", "sqlite", "--checkpoint-dir", path, "run_sqlite"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run run_sqlite started") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestRunsPrintsCheckpointSummaries(t *testing.T) {
	dir := t.TempDir()
	store := checkpointjsonl.New(dir)
	cp := testCLICheckpoint("run_cli", 2)
	cp.State.Phase = harness.RunPhaseCompleted
	cp.State.Control.Status = harness.RunStatusCompleted
	cp.State.Step = 3
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--checkpoint-dir", dir}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "RUN ID") || !strings.Contains(output, "run_cli") || !strings.Contains(output, "completed") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunsCanReadSQLiteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := checkpointsqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()
	cp := testCLICheckpoint("run_sqlite", 2)
	cp.State.Phase = harness.RunPhaseCompleted
	cp.State.Control.Status = harness.RunStatusCompleted
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--checkpoint-type", "sqlite", "--checkpoint-dir", path}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run_sqlite") || !strings.Contains(stdout.String(), "completed") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestResumeLoadsJSONLCheckpoint(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	dir := t.TempDir()
	store := checkpointjsonl.New(dir)
	cp := testCLICheckpoint("run_resume", 3)
	cp.State.Phase = harness.RunPhaseCompleted
	cp.State.Control.Status = harness.RunStatusCompleted
	cp.State.Messages = append(cp.State.Messages, harness.MessageState{Role: "assistant", Content: "already done"})
	if err := store.Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(context.Background(), []string{"resume", "--checkpoint-dir", dir, "run_resume"}, IO{Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "run run_resume resumed") || !strings.Contains(output, "already done") || !strings.Contains(output, "run run_resume done") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestResumeReturnsUnsupportedStateExitCode(t *testing.T) {
	dir := t.TempDir()
	cp := testCLICheckpoint("run_future", 1)
	cp.State.Version = "zenforge.run_state.v2"
	if err := checkpointjsonl.New(dir).Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"resume", "--checkpoint-dir", dir, "run_future",
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if code != exitUnsupportedResume {
		t.Fatalf("code = %d, want %d; stderr=%q", code, exitUnsupportedResume, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported run state version") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestResumeReturnsUnsupportedCheckpointExitCode(t *testing.T) {
	dir := t.TempDir()
	runID := "run_future_checkpoint"
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	cp := testCLICheckpoint(runID, 1)
	cp.Version = "zenforge.checkpoint.v2"
	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "latest.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"resume", "--checkpoint-dir", dir, runID,
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if code != exitUnsupportedResume {
		t.Fatalf("code = %d, want %d; stderr=%q", code, exitUnsupportedResume, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported checkpoint version") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestResumeReturnsUnsupportedPhaseExitCode(t *testing.T) {
	dir := t.TempDir()
	cp := testCLICheckpoint("run_future_phase", 1)
	cp.State.Phase = harness.RunPhase("future")
	if err := checkpointjsonl.New(dir).Save(context.Background(), cp); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stderr bytes.Buffer
	code := Main(context.Background(), []string{
		"resume", "--checkpoint-dir", dir, cp.RunID,
	}, IO{Stdout: io.Discard, Stderr: &stderr})
	if code != exitUnsupportedResume {
		t.Fatalf("code = %d, want %d; stderr=%q", code, exitUnsupportedResume, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported run phase") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunsJSONOutput(t *testing.T) {
	dir := t.TempDir()
	store := checkpointjsonl.New(dir)
	if err := store.Save(context.Background(), testCLICheckpoint("run_json", 1)); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--checkpoint-dir", dir, "--json"}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	var summaries []checkpointjsonl.Summary
	if err := json.Unmarshal(stdout.Bytes(), &summaries); err != nil {
		t.Fatalf("Unmarshal returned error: %v; output=%q", err, stdout.String())
	}
	if len(summaries) != 1 || summaries[0].RunID != "run_json" {
		t.Fatalf("unexpected summaries: %#v", summaries)
	}
}

func TestRunsLoadsCheckpointDirFromConfig(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "runs")
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{Checkpoint: checkpointConfig{Path: runDir}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	store := checkpointjsonl.New(runDir)
	if err := store.Save(context.Background(), testCLICheckpoint("run_config", 1)); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"runs", "--config", configPath}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run_config") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestOptionsFromConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "zenforge.json")
	enabled := false
	config := configFile{
		Model:     modelConfig{Provider: "anthropic", Name: "claude-test", APIKeyEnv: "TEST_KEY", BaseURL: "https://api.example"},
		Agent:     agentConfig{Instructions: "Be exact.", MaxSteps: 3, Planning: false},
		Workspace: workspaceConfig{Root: "repo", MaxReadBytes: 7, MaxWriteBytes: 8, ReadRoots: []string{"docs"}, WriteRoots: []string{"generated"}},
		Shell: shellConfig{
			Enabled:        &enabled,
			WorkingDir:     "shell-repo",
			Allow:          []string{"go test ./..."},
			Timeout:        "5s",
			MaxOutputBytes: 99,
		},
		Approval:   approvalConfig{Mode: "always"},
		Checkpoint: checkpointConfig{Type: "sqlite", Path: "runs.db"},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	opts, err := optionsFromArgs([]string{"--config", configPath})
	if err != nil {
		t.Fatalf("optionsFromArgs returned error: %v", err)
	}
	if opts.provider != "anthropic" || opts.model != "claude-test" || opts.apiKeyEnv != "TEST_KEY" || opts.baseURL != "https://api.example" {
		t.Fatalf("model opts not applied: %#v", opts)
	}
	if opts.instructions != "Be exact." || opts.maxSteps != 3 || opts.planning != "disabled" {
		t.Fatalf("agent opts not applied: %#v", opts)
	}
	if opts.workspace != "repo" || opts.shellWorkingDir != "shell-repo" || opts.workspaceMaxRead != 7 || opts.workspaceMaxWrite != 8 || !opts.noShell || opts.shellTimeout.String() != "5s" || opts.shellMaxOutputBytes != 99 {
		t.Fatalf("tool opts not applied: %#v", opts)
	}
	if strings.Join(opts.workspaceReadRoots, ",") != "docs" || strings.Join(opts.workspaceWriteRoots, ",") != "generated" {
		t.Fatalf("workspace roots not applied: %#v", opts)
	}
	if opts.approve != "always" {
		t.Fatalf("approval opts not applied: %#v", opts)
	}
	if opts.checkpointType != "sqlite" || opts.checkpointDir != "runs.db" {
		t.Fatalf("checkpoint opts not applied: %#v", opts)
	}
}

func TestOptionsFromConfigRejectsInvalidShellTimeout(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{Shell: shellConfig{Timeout: "not-a-duration"}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	_, err = optionsFromArgs([]string{"--config", configPath})
	if err == nil || !strings.Contains(err.Error(), "parse shell.timeout") {
		t.Fatalf("expected parse shell.timeout error, got %v", err)
	}
}

func TestOptionsFromConfigRejectsInvalidPlanningMode(t *testing.T) {
	for name, planning := range map[string]any{
		"unknown": "sometimes",
		"number":  1,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "zenforge.json")
			config := configFile{Agent: agentConfig{Planning: planning}}
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatalf("Marshal returned error: %v", err)
			}
			if err := os.WriteFile(configPath, data, 0o644); err != nil {
				t.Fatalf("WriteFile returned error: %v", err)
			}
			_, err = optionsFromArgs([]string{"--config", configPath})
			if err == nil || !strings.Contains(err.Error(), "agent.planning") {
				t.Fatalf("expected agent.planning error, got %v", err)
			}
		})
	}
}

func TestOptionsFromConfigRejectsInvalidOrConflictingAgentMode(t *testing.T) {
	for name, agent := range map[string]agentConfig{
		"invalid":  {Mode: "batch"},
		"conflict": {Mode: "react", Planning: "enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "zenforge.json")
			data, err := json.Marshal(configFile{Agent: agent})
			if err != nil {
				t.Fatalf("Marshal returned error: %v", err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("WriteFile returned error: %v", err)
			}
			if _, err := optionsFromArgs([]string{"--config", path}); err == nil || !strings.Contains(err.Error(), "agent.mode") {
				t.Fatalf("expected agent.mode error, got %v", err)
			}
		})
	}
}

func TestOptionsFromConfigRejectsInvalidApprovalMode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "zenforge.json")
	config := configFile{Approval: approvalConfig{Mode: "later"}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	_, err = optionsFromArgs([]string{"--config", configPath})
	if err == nil || !strings.Contains(err.Error(), "approval.mode") {
		t.Fatalf("expected approval.mode error, got %v", err)
	}
}

func TestOptionsFromConfigRejectsInvalidProviderAndCheckpoint(t *testing.T) {
	for name, config := range map[string]configFile{
		"provider":   {Model: modelConfig{Provider: "other"}},
		"checkpoint": {Checkpoint: checkpointConfig{Type: "yaml"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "zenforge.json")
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatalf("Marshal returned error: %v", err)
			}
			if err := os.WriteFile(configPath, data, 0o644); err != nil {
				t.Fatalf("WriteFile returned error: %v", err)
			}
			_, err = optionsFromArgs([]string{"--config", configPath})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected %s error, got %v", name, err)
			}
		})
	}
}

func TestOptionsFromConfigRejectsNegativeLimits(t *testing.T) {
	for name, config := range map[string]configFile{
		"agent.maxSteps":          {Agent: agentConfig{MaxSteps: -1}},
		"workspace.maxReadBytes":  {Workspace: workspaceConfig{MaxReadBytes: -1}},
		"workspace.maxWriteBytes": {Workspace: workspaceConfig{MaxWriteBytes: -1}},
		"shell.maxOutputBytes":    {Shell: shellConfig{MaxOutputBytes: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "zenforge.json")
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatalf("Marshal returned error: %v", err)
			}
			if err := os.WriteFile(configPath, data, 0o644); err != nil {
				t.Fatalf("WriteFile returned error: %v", err)
			}
			_, err = optionsFromArgs([]string{"--config", configPath})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected %s error, got %v", name, err)
			}
		})
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func testCLICheckpoint(runID string, seq int64) checkpoint.Checkpoint {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	return checkpoint.Checkpoint{
		Version: checkpoint.CheckpointVersion,
		RunID:   runID,
		Seq:     seq,
		State: harness.RunState{
			Version:   harness.RunStateVersion,
			RunID:     runID,
			Input:     "hello",
			Phase:     harness.RunPhaseCreated,
			CreatedAt: now,
			UpdatedAt: now,
			Control:   harness.RunControlState{Status: harness.RunStatusIdle},
		},
		SavedAt: now,
	}
}

func TestInitCreatesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "zenforge.json")
	var stdout bytes.Buffer
	code := Main(context.Background(), []string{"init", "--config", configPath}, IO{Stdout: &stdout})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var config configFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if config.Model.Name == "" || config.Checkpoint.Path == "" {
		t.Fatalf("default config incomplete: %#v", config)
	}
	if config.Approval.Mode != "prompt" {
		t.Fatalf("default approval mode = %q", config.Approval.Mode)
	}
	if config.Agent.Mode != "plan_execute" || config.Agent.Planning != nil {
		t.Fatalf("default execution mode = %#v", config.Agent)
	}
	if !strings.Contains(stdout.String(), "created") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestConfigReferenceIncludesDefaultConfig(t *testing.T) {
	data, err := json.MarshalIndent(defaultConfigFile(), "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent returned error: %v", err)
	}
	docs, err := os.ReadFile(filepath.Join("..", "docs", "config-reference.md"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !strings.Contains(string(docs), string(data)) {
		t.Fatalf("config reference does not include current default config")
	}
}

func TestApprovalBrokerModes(t *testing.T) {
	always, err := approvalBroker(options{approve: "always"}, IO{})
	if err != nil || always == nil {
		t.Fatalf("always broker = %#v err=%v", always, err)
	}
	never, err := approvalBroker(options{approve: "never"}, IO{})
	if err != nil || never == nil {
		t.Fatalf("never broker = %#v err=%v", never, err)
	}
	prompt, err := approvalBroker(options{approve: "prompt"}, IO{Stdin: strings.NewReader("1\n"), Stderr: &bytes.Buffer{}})
	if err != nil || prompt == nil {
		t.Fatalf("prompt broker = %#v err=%v", prompt, err)
	}
	if _, err := approvalBroker(options{approve: "later"}, IO{}); err == nil {
		t.Fatalf("expected unknown approval mode error")
	}
}

func TestRenderApprovalRequested(t *testing.T) {
	var stdout bytes.Buffer
	renderEvent(&stdout, zenforge.NewEvent(zenforge.EventApprovalRequested, "run_1", map[string]any{
		"operation": "shell.command",
		"risk":      "high",
		"request": map[string]any{
			"title":       "Approve shell command",
			"description": "Run tests",
		},
	}))
	output := stdout.String()
	if !strings.Contains(output, "approval required: shell.command (high)") || !strings.Contains(output, "Approve shell command") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRenderTodosHandlesTypedPayload(t *testing.T) {
	var stdout bytes.Buffer
	renderEvent(&stdout, zenforge.NewEvent(zenforge.EventTodoUpdated, "run_1", map[string]any{
		"todos": []planner.Todo{
			{Content: "Inspect project structure", Status: planner.TodoDone},
			{Content: "Review tool runtime", Status: planner.TodoInProgress},
		},
	}))
	output := stdout.String()
	if !strings.Contains(output, "[done] Inspect project structure") ||
		!strings.Contains(output, "[in_progress] Review tool runtime") ||
		strings.Contains(output, `"content"`) {
		t.Fatalf("unexpected todo output: %q", output)
	}
}

func TestPlanningModeParsing(t *testing.T) {
	tests := map[string]zenforge.PlanningMode{
		"enabled":      zenforge.PlanningEnabled,
		"true":         zenforge.PlanningEnabled,
		"plan_execute": zenforge.PlanningPlanExecute,
		"plan-execute": zenforge.PlanningPlanExecute,
		"disabled":     zenforge.PlanningDisabled,
		"bogus":        zenforge.PlanningDisabled,
	}
	for input, want := range tests {
		if got := planningMode(input); got != want {
			t.Fatalf("planningMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAgentModeParsing(t *testing.T) {
	tests := map[string]zenforge.AgentMode{
		"react":        zenforge.ModeReact,
		"ONESHOT":      zenforge.ModeOneshot,
		"one-shot":     zenforge.ModeOneshot,
		"plan_execute": zenforge.ModePlanExecute,
		"plan-execute": zenforge.ModePlanExecute,
	}
	for input, want := range tests {
		got, err := parseAgentMode(input)
		if err != nil || got != want {
			t.Fatalf("parseAgentMode(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := parseAgentMode("batch"); err == nil {
		t.Fatal("expected invalid mode error")
	}
}
