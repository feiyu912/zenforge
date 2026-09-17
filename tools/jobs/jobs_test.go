package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	jobspkg "github.com/feiyu912/zenforge/jobs"
	"github.com/feiyu912/zenforge/tool"
)

type harness struct {
	manager *jobspkg.Manager
	tools   map[string]tool.Tool
	ctx     tool.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	manager := jobspkg.New(jobspkg.Config{DefaultTimeout: 10 * time.Second, MaxJobs: 4})
	t.Cleanup(manager.Close)
	built, err := Tools(Config{Manager: manager, MaxOutputBytes: 64 << 10, MaxWait: 2 * time.Second})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	byName := map[string]tool.Tool{}
	for _, instance := range built {
		byName[instance.Name()] = instance
	}
	return &harness{manager: manager, tools: byName}
}

func (h *harness) call(t *testing.T, name string, input any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	instance, ok := h.tools[name]
	if !ok {
		t.Fatalf("tool %s is not registered", name)
	}
	result, err := instance.Call(context.Background(), raw, h.ctx)
	if err != nil {
		t.Fatalf("%s returned error: %v", name, err)
	}
	return result.Structured
}

func (h *harness) callErr(t *testing.T, name string, input any) error {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	_, err = h.tools[name].Call(context.Background(), raw, h.ctx)
	return err
}

func TestToolsAreRegisteredWithTheReferenceNames(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{ExecName, WriteName, OutputName, ListName, KillName} {
		if _, ok := h.tools[name]; !ok {
			t.Fatalf("tool %s is missing", name)
		}
	}
	if _, err := Tools(Config{}); err == nil {
		t.Fatal("a missing manager was accepted")
	}
}

func TestExecCommandForegroundReturnsOutputAndExitCode(t *testing.T) {
	h := newHarness(t)
	out := h.call(t, ExecName, execInput{Command: "echo hello; echo oops >&2; exit 4"})
	if out["jobId"] == "" || out["status"] != "exited" {
		t.Fatalf("output = %#v", out)
	}
	if !strings.Contains(out["stdout"].(string), "hello") || !strings.Contains(out["stderr"].(string), "oops") {
		t.Fatalf("output = %#v", out)
	}
	if code, ok := out["exitCode"].(float64); !ok || int(code) != 4 {
		t.Fatalf("exit code = %#v", out["exitCode"])
	}
	if !strings.Contains(out["output"].(string), "exit=4") {
		t.Fatalf("rendered output = %q", out["output"])
	}
}

func TestExecCommandBackgroundThenPoll(t *testing.T) {
	h := newHarness(t)
	started := h.call(t, ExecName, execInput{Command: "echo first; sleep 0.2; echo second", Background: true})
	id, _ := started["jobId"].(string)
	if id == "" || started["status"] != "running" {
		t.Fatalf("output = %#v", started)
	}
	if !strings.Contains(started["output"].(string), id) {
		t.Fatalf("the model was not told how to poll: %q", started["output"])
	}
	// Poll with the returned offsets until the job finishes; the bytes seen
	// across polls must be exactly the command's output.
	seen := ""
	stdoutOffset := int64(0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := h.call(t, OutputName, outputInput{
			JobID:        id,
			StdoutOffset: stdoutOffset,
			WaitMs:       500,
		})
		seen += out["stdout"].(string)
		stdoutOffset = int64(out["stdoutOffset"].(float64))
		if out["running"] == false {
			break
		}
	}
	if seen != "first\nsecond\n" {
		t.Fatalf("seen = %q", seen)
	}
}

func TestWriteStdinFeedsAJob(t *testing.T) {
	h := newHarness(t)
	started := h.call(t, ExecName, execInput{Command: "read line; echo got:$line", Background: true})
	id, _ := started["jobId"].(string)
	written := h.call(t, WriteName, writeInput{JobID: id, Input: "hello\n", Close: true})
	if written["status"] == "" || !strings.Contains(written["output"].(string), "closed stdin") {
		t.Fatalf("output = %#v", written)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := h.call(t, OutputName, outputInput{JobID: id, WaitMs: 500})
		if !out["running"].(bool) {
			if !strings.Contains(out["stdout"].(string), "got:hello") {
				t.Fatalf("stdout = %q", out["stdout"])
			}
			return
		}
	}
	t.Fatal("the job did not finish")
}

func TestJobOutputReportsDroppedOutput(t *testing.T) {
	h := newHarness(t)
	started := h.call(t, ExecName, execInput{
		Command:        "for i in $(seq 1 200); do echo line-$i; done",
		Background:     true,
		MaxOutputBytes: 64,
	})
	id, _ := started["jobId"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := h.call(t, OutputName, outputInput{JobID: id, WaitMs: 500})
		if !out["running"].(bool) {
			if out["stdoutDropped"] != true {
				t.Fatalf("dropped output was not reported: %#v", out)
			}
			return
		}
	}
	t.Fatal("the job did not finish")
}

func TestJobListAndKill(t *testing.T) {
	h := newHarness(t)
	empty := h.call(t, ListName, listInput{})
	if empty["output"] != "no jobs" {
		t.Fatalf("output = %#v", empty)
	}
	started := h.call(t, ExecName, execInput{Command: "sleep 30", Background: true})
	id, _ := started["jobId"].(string)
	list := h.call(t, ListName, listInput{})
	jobs, _ := list["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("list = %#v", list)
	}
	killed := h.call(t, KillName, killInput{JobID: id, Reason: "test over"})
	if !strings.Contains(killed["output"].(string), "Stopped") {
		t.Fatalf("output = %#v", killed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := h.call(t, OutputName, outputInput{JobID: id})
		if out["status"] == "killed" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job was not killed")
}

func TestInvalidArgumentsAndUnknownJobs(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name  string
		tool  string
		input any
	}{
		{"empty command", ExecName, execInput{}},
		{"write without input", WriteName, writeInput{JobID: "job_1"}},
		{"write without id", WriteName, writeInput{Input: "x"}},
		{"output without id", OutputName, outputInput{}},
		{"kill without id", KillName, killInput{}},
		{"unknown output job", OutputName, outputInput{JobID: "job_missing"}},
		{"unknown kill job", KillName, killInput{JobID: "job_missing"}},
		{"unknown write job", WriteName, writeInput{JobID: "job_missing", Input: "x"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := h.callErr(t, testCase.tool, testCase.input)
			if err == nil {
				t.Fatal("the call succeeded")
			}
			if !errors.Is(err, tool.ErrInvalidArguments) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	// A missing job suggests how to recover.
	err := h.callErr(t, OutputName, outputInput{JobID: "job_missing"})
	if !strings.Contains(err.Error(), "job_list") {
		t.Fatalf("error = %v", err)
	}
}

func TestOutputHintPointersForARunningJob(t *testing.T) {
	h := newHarness(t)
	started := h.call(t, ExecName, execInput{Command: "sleep 2", Background: true})
	id, _ := started["jobId"].(string)
	out := h.call(t, OutputName, outputInput{JobID: id})
	if hint, _ := out["hint"].(string); hint == "" {
		t.Fatalf("a running job should explain how to wait: %#v", out)
	}
	h.call(t, KillName, killInput{JobID: id, Reason: "done"})
}
