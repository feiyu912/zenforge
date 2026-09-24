package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// funcModel is a scripted ToolCallingChatModel that decides each turn from the
// conversation it is given. It needs no endpoint, which keeps the graph tests
// hermetic while still exercising the real Eino ReAct loop, the real tool
// dispatch and the real checkpoint store.
type funcModel struct {
	mu    sync.Mutex
	fn    func(input []*schema.Message) (*schema.Message, error)
	calls int
}

func (m *funcModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.fn(input)
}

func (m *funcModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("funcModel: streaming is not implemented")
}

func (m *funcModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return m, nil }

func (m *funcModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func withChatModel(t *testing.T, m model.ToolCallingChatModel) {
	t.Helper()
	old := chatModelFactory
	chatModelFactory = func(context.Context, Config) (model.ToolCallingChatModel, error) { return m, nil }
	t.Cleanup(func() { chatModelFactory = old })
}

func lastMessage(input []*schema.Message) *schema.Message {
	if len(input) == 0 {
		return nil
	}
	return input[len(input)-1]
}

func sawToolResult(input []*schema.Message) bool {
	m := lastMessage(input)
	return m != nil && m.Role == schema.Tool
}

func toolCallMessage(name, args string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   "call_" + name,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}})
}

func testConfig(t *testing.T, task, phase, approval string, requirePause bool) (Config, string, string) {
	t.Helper()
	ws := t.TempDir()
	state := t.TempDir()
	return Config{
		BaseURL:      "http://127.0.0.1:1/v1",
		APIKey:       "test-key",
		Model:        "bench-model",
		Task:         task,
		Workspace:    ws,
		StateDir:     state,
		Phase:        phase,
		Approval:     approval,
		RequirePause: requirePause,
		ResultPath:   filepath.Join(t.TempDir(), "result.json"),
	}, ws, state
}

// TestGraphCompletesWithoutApproval runs the whole agent loop through Eino for
// a task that needs no interrupt.
func TestGraphCompletesWithoutApproval(t *testing.T) {
	model := &funcModel{fn: func(input []*schema.Message) (*schema.Message, error) {
		if sawToolResult(input) {
			return schema.AssistantMessage("wrote the file", nil), nil
		}
		return toolCallMessage("write_file", `{"path":"out.txt","content":"hello from the graph"}`), nil
	}}
	withChatModel(t, model)

	cfg, ws, _ := testConfig(t, TaskEditFile, PhaseRun, ApprovalApprove, false)
	res := Execute(cfg)
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Detail, StatusCompleted)
	}
	b, err := os.ReadFile(filepath.Join(ws, "out.txt"))
	if err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if string(b) != "hello from the graph" {
		t.Errorf("out.txt = %q", string(b))
	}
	if model.callCount() < 2 {
		t.Errorf("model called %d times, want at least 2 (tool call + final answer)", model.callCount())
	}
}

// TestGraphRejectsCommandAndDoesNotRunIt is the approve-command reject case.
func TestGraphRejectsCommandAndDoesNotRunIt(t *testing.T) {
	model := &funcModel{fn: func(input []*schema.Message) (*schema.Message, error) {
		if sawToolResult(input) {
			return schema.AssistantMessage("finished", nil), nil
		}
		return toolCallMessage("run_shell", `{"command":"echo x >> count.txt"}`), nil
	}}
	withChatModel(t, model)

	cfg, ws, _ := testConfig(t, TaskApproveCommand, PhaseRun, ApprovalReject, false)
	res := Execute(cfg)
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Detail, StatusCompleted)
	}
	if _, err := os.Stat(filepath.Join(ws, "count.txt")); err == nil {
		t.Fatal("run_shell executed a command that was rejected")
	}
}

// TestGraphApprovesCommandAndRunsItOnce is the approve-command approve case,
// answered in-process through Eino's resume data.
func TestGraphApprovesCommandAndRunsItOnce(t *testing.T) {
	model := &funcModel{fn: func(input []*schema.Message) (*schema.Message, error) {
		if sawToolResult(input) {
			return schema.AssistantMessage("finished", nil), nil
		}
		return toolCallMessage("run_shell", `{"command":"echo x >> count.txt"}`), nil
	}}
	withChatModel(t, model)

	cfg, ws, _ := testConfig(t, TaskApproveCommand, PhaseRun, ApprovalApprove, false)
	res := Execute(cfg)
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Detail, StatusCompleted)
	}
	b, err := os.ReadFile(filepath.Join(ws, "count.txt"))
	if err != nil {
		t.Fatalf("the approved command did not run: %v", err)
	}
	if got := strings.Count(string(b), "x"); got != 1 {
		t.Errorf("command ran %d times, want exactly once (count.txt = %q)", got, string(b))
	}
}

// TestDurablePauseThenResumeInFreshProcess is the durable-task contract:
// process one stops at an approval interrupt with durable state on disk, and a
// second process with BENCH_PHASE=resume finishes the task from Eino's own
// checkpoint.
func TestDurablePauseThenResumeInFreshProcess(t *testing.T) {
	newModel := func() *funcModel {
		return &funcModel{fn: func(input []*schema.Message) (*schema.Message, error) {
			if sawToolResult(input) {
				return schema.AssistantMessage("durable task done", nil), nil
			}
			return toolCallMessage("run_shell", `{"command":"echo x >> count.txt"}`), nil
		}}
	}

	cfg, ws, state := testConfig(t, TaskDurableTask, PhaseRun, ApprovalApprove, true)

	// ---- process 1 ----
	withChatModel(t, newModel())
	first := Execute(cfg)
	if first.Status != StatusPaused {
		t.Fatalf("phase run status = %q (%s), want %q", first.Status, first.Detail, StatusPaused)
	}
	if first.ExitCode() != 75 {
		t.Fatalf("paused exit code = %d, want 75", first.ExitCode())
	}
	if _, err := os.Stat(filepath.Join(ws, "count.txt")); err == nil {
		t.Fatal("phase run executed the unapproved command before pausing")
	}
	st, err := readResumeState(state)
	if err != nil {
		t.Fatalf("no durable resume state was written: %v", err)
	}
	if st.CheckpointID != cfg.CheckpointID() {
		t.Errorf("sidecar checkpoint id = %q, want %q", st.CheckpointID, cfg.CheckpointID())
	}
	if len(st.InterruptIDs) == 0 {
		t.Fatal("sidecar recorded no interrupt id; the second process could not answer the approval")
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatalf("ReadDir(state): %v", err)
	}
	var checkpointFiles int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "checkpoint-") {
			checkpointFiles++
		}
	}
	if checkpointFiles == 0 {
		t.Fatal("Eino persisted no checkpoint under BENCH_STATE_DIR")
	}

	// ---- process 2: a brand new agent, runner and model ----
	resumeCfg := cfg
	resumeCfg.Phase = PhaseResume
	secondModel := newModel()
	withChatModel(t, secondModel)
	second := Execute(resumeCfg)
	if second.Status != StatusCompleted {
		t.Fatalf("phase resume status = %q (%s), want %q", second.Status, second.Detail, StatusCompleted)
	}
	b, err := os.ReadFile(filepath.Join(ws, "count.txt"))
	if err != nil {
		t.Fatalf("the resumed command did not run: %v", err)
	}
	if got := strings.Count(string(b), "x"); got != 1 {
		t.Errorf("command ran %d times across both processes, want exactly once (count.txt = %q)", got, string(b))
	}
}

func TestResumeWithoutDurableStateFails(t *testing.T) {
	model := &funcModel{fn: func([]*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("should not be reached", nil), nil
	}}
	withChatModel(t, model)

	cfg, _, _ := testConfig(t, TaskDurableTask, PhaseResume, ApprovalApprove, false)
	res := Execute(cfg)
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", res.Status, StatusFailed)
	}
	if !strings.Contains(res.Detail, "no durable state") {
		t.Errorf("detail = %q, want it to name the missing durable state", res.Detail)
	}
}

func TestRequirePauseWithoutInterruptFails(t *testing.T) {
	model := &funcModel{fn: func([]*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("finished immediately", nil), nil
	}}
	withChatModel(t, model)

	cfg, _, _ := testConfig(t, TaskDurableTask, PhaseRun, ApprovalApprove, true)
	res := Execute(cfg)
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want %q (no durable pause was possible)", res.Status, StatusFailed)
	}
	if !strings.Contains(res.Detail, "BENCH_REQUIRE_PAUSE") {
		t.Errorf("detail = %q, want it to name BENCH_REQUIRE_PAUSE", res.Detail)
	}
}

func TestToolInterruptsPicksToolContexts(t *testing.T) {
	leaf := &adk.InterruptCtx{
		ID:      "agent:a;tool:run_shell#1",
		Address: adk.Address{{Type: adk.AddressSegmentAgent, ID: "a"}, {Type: adk.AddressSegmentTool, ID: "run_shell", SubID: "1"}},
		Parent:  &adk.InterruptCtx{ID: "agent:a", Address: adk.Address{{Type: adk.AddressSegmentAgent, ID: "a"}}},
	}
	got := toolInterrupts([]*adk.InterruptCtx{leaf})
	if len(got) != 1 || got[0].ID != leaf.ID {
		t.Fatalf("toolInterrupts = %+v, want just %q", got, leaf.ID)
	}

	agentOnly := &adk.InterruptCtx{ID: "agent:a", Address: adk.Address{{Type: adk.AddressSegmentAgent, ID: "a"}}}
	if got := toolInterrupts([]*adk.InterruptCtx{agentOnly}); len(got) != 0 {
		t.Fatalf("toolInterrupts(agent-only) = %+v, want none", got)
	}
	if got := toolInterrupts(nil); got != nil {
		t.Fatalf("toolInterrupts(nil) = %+v, want nil", got)
	}
}

func TestUserQueryUsesBenchQueryVerbatim(t *testing.T) {
	const frozen = "Record step 1 and step 2 in steps.txt, create milestone.txt with a shell command, " +
		"then write the final artifact to out.txt."

	cfg := Config{Task: TaskDurableTask, Query: frozen, QueryFromEnv: true}
	if got := userQuery(cfg); got != frozen {
		t.Errorf("userQuery = %q, want the frozen instruction verbatim %q", got, frozen)
	}

	// Without BENCH_QUERY the runner still runs, via an announced fallback.
	fallback := userQuery(Config{Task: TaskDurableTask})
	if strings.TrimSpace(fallback) == "" {
		t.Error("userQuery fallback is empty, so the runner could not run at all")
	}
	if fallback == frozen {
		t.Error("the fallback must not be mistaken for the frozen instruction")
	}
}

func TestQueryForCoversEveryTask(t *testing.T) {
	for _, task := range []string{TaskEditFile, TaskApproveCommand, TaskDurableTask} {
		if q := queryFor(task); strings.TrimSpace(q) == "" {
			t.Errorf("queryFor(%q) is empty", task)
		}
	}
	if q := queryFor("unknown"); strings.TrimSpace(q) == "" {
		t.Error("queryFor(unknown) is empty")
	}
}
