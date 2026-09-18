package jobs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, manager *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(id)
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if job.Status.Terminal() {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return Job{}
}

func TestBufferKeepsTheHeadAndTheNewestBytes(t *testing.T) {
	// A quarter of the capacity, at most 8KiB, is kept as the head; the rest
	// is the newest output, so a flooded stream still shows how it began.
	buffer := NewBuffer(8)
	if buffer.headCap != 2 {
		t.Fatalf("head capacity = %d, want 2", buffer.headCap)
	}
	_, _ = buffer.Write([]byte("abcd"))
	chunk := buffer.Read(0, 0)
	if string(chunk.Data) != "ab" || chunk.Dropped {
		t.Fatalf("head chunk = %#v", chunk)
	}
	if chunk.Next != 2 {
		t.Fatalf("head next = %d", chunk.Next)
	}
	// The middle is dropped once the tail fills, and the reader is told its
	// view has a hole of a known size rather than silently losing it.
	_, _ = buffer.Write([]byte("efgh"))
	_, _ = buffer.Write([]byte("ijkl"))
	chunk = buffer.Read(2, 0)
	if string(chunk.Data) != "ghijkl" || !chunk.Dropped || chunk.Elided != 4 {
		t.Fatalf("tail chunk = %#v", chunk)
	}
	if chunk.Total != 12 {
		t.Fatalf("total = %d", chunk.Total)
	}
	// A reader that stayed current only sees the new bytes.
	fresh := buffer.Read(10, 0)
	if string(fresh.Data) != "kl" || fresh.Dropped {
		t.Fatalf("fresh chunk = %#v", fresh)
	}
	// Reading at the end returns nothing and does not move.
	chunk = buffer.Read(chunk.Next, 0)
	if len(chunk.Data) != 0 || chunk.Next != 12 {
		t.Fatalf("chunk = %#v", chunk)
	}
	// A max limits the read, and Next advances by what was returned.
	chunk = buffer.Read(8, 2)
	if string(chunk.Data) != "ij" || chunk.Next != 10 {
		t.Fatalf("chunk = %#v", chunk)
	}
	if buffer.Total() != 12 || buffer.Len() != 8 {
		t.Fatalf("total=%d len=%d", buffer.Total(), buffer.Len())
	}
}

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	job, result, err := manager.Run(context.Background(), Spec{Command: "echo out; echo err >&2; exit 3"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if job.Status != StatusExited || job.ExitCode == nil || *job.ExitCode != 3 {
		t.Fatalf("job = %#v", job)
	}
	if strings.TrimSpace(string(result.Stdout.Data)) != "out" {
		t.Fatalf("stdout = %q", result.Stdout.Data)
	}
	if strings.TrimSpace(string(result.Stderr.Data)) != "err" {
		t.Fatalf("stderr = %q", result.Stderr.Data)
	}
	if job.EndedAt == nil || job.Duration <= 0 {
		t.Fatalf("job = %#v", job)
	}
	if !strings.Contains(job.Summary(), "exited (3)") {
		t.Fatalf("summary = %s", job.Summary())
	}
}

func TestRunWaitsForOutputWrittenAfterTheMainProcessExits(t *testing.T) {
	// A command can hand its stdout to a background subshell and exit before
	// that subshell has written. The bytes are still coming, and a job
	// announced as terminal at the main process's exit would hand the caller
	// an empty stdout for output that arrives a moment later.
	manager := New(Config{DefaultTimeout: 10 * time.Second, DrainGrace: 10 * time.Second})
	defer manager.Close()
	start := time.Now()
	_, result, err := manager.Run(context.Background(), Spec{Command: "(sleep 0.2; echo late) & exit 0"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := strings.TrimSpace(string(result.Stdout.Data)); got != "late" {
		t.Fatalf("stdout = %q, want the output the subshell wrote after the process exited", got)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("the job took %s", elapsed)
	}
}

func TestCollectSignalsDrainedOnlyAfterBothStreamsEnd(t *testing.T) {
	// The mechanism the reaper depends on: a drain is complete only when both
	// copies have ended, not when the first pipe reaches EOF.
	manager := New(Config{})
	stdoutRead, stdoutWrite := io.Pipe()
	stderrRead, stderrWrite := io.Pipe()
	drained := make(chan struct{})
	go manager.collect(&record{stdout: NewBuffer(1024), stderr: NewBuffer(1024)}, stdoutRead, stderrRead, drained)
	if _, err := io.WriteString(stdoutWrite, "out"); err != nil {
		t.Fatalf("WriteString returned error: %v", err)
	}
	_ = stdoutWrite.Close()
	select {
	case <-drained:
		t.Fatal("the drain was reported complete while stderr was still open")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := io.WriteString(stderrWrite, "err"); err != nil {
		t.Fatalf("WriteString returned error: %v", err)
	}
	_ = stderrWrite.Close()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain was never reported complete")
	}
}

func TestJobWhoseOutputIsHeldByAGrandchildStillBecomesTerminal(t *testing.T) {
	// A background grandchild inherits the pipe and can hold it open past the
	// main process exit, so EOF may never arrive. The job must still reach a
	// terminal state with what was captured instead of hanging.
	manager := New(Config{DefaultTimeout: 10 * time.Second, DrainGrace: 500 * time.Millisecond})
	defer manager.Close()
	start := time.Now()
	_, result, err := manager.Run(context.Background(), Spec{Command: "sleep 30 & echo started"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(string(result.Stdout.Data), "started") {
		t.Fatalf("stdout = %q", result.Stdout.Data)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the job hung for %s", elapsed)
	}
}

func TestBackgroundJobIsPolledByOffset(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	job, err := manager.Start(context.Background(), Spec{Command: "echo first; sleep 0.2; echo second"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if !job.Running() {
		t.Fatalf("job = %#v", job)
	}
	// The first read may already see both lines; what matters is that
	// offsets advance and never re-deliver bytes.
	seen := ""
	offset := int64(0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := manager.Output(job.ID, offset, 0, 0)
		if err != nil {
			t.Fatalf("Output returned error: %v", err)
		}
		seen += string(result.Stdout.Data)
		offset = result.Stdout.Next
		if result.Job.Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seen != "first\nsecond\n" {
		t.Fatalf("seen = %q", seen)
	}
	final, err := manager.Get(job.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if final.Status != StatusExited || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("job = %#v", final)
	}
}

func TestKillStopsARunningJob(t *testing.T) {
	manager := New(Config{DefaultTimeout: 30 * time.Second})
	defer manager.Close()
	job, err := manager.Start(context.Background(), Spec{Command: "echo starting; sleep 30"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := manager.Kill(job.ID, "no longer needed"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != StatusKilled || final.Error != "no longer needed" {
		t.Fatalf("job = %#v", final)
	}
	// Killing again is a no-op, and the status stays killed.
	if err := manager.Kill(job.ID, "again"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}
	again, err := manager.Get(job.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if again.Status != StatusKilled || again.Error != "no longer needed" {
		t.Fatalf("job = %#v", again)
	}
}

func TestTimeoutKillsAJob(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	job, err := manager.Start(context.Background(), Spec{Command: "sleep 30", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != StatusKilled {
		t.Fatalf("job = %#v", final)
	}
	if !strings.Contains(final.Error, "timeout") {
		t.Fatalf("error = %q", final.Error)
	}
}

func TestJobLimitRefusesRatherThanQueues(t *testing.T) {
	manager := New(Config{MaxJobs: 1, DefaultTimeout: 30 * time.Second})
	defer manager.Close()
	first, err := manager.Start(context.Background(), Spec{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if _, err := manager.Start(context.Background(), Spec{Command: "sleep 30"}); err == nil {
		t.Fatal("the job limit was not enforced")
	} else if !strings.Contains(err.Error(), "job limit") {
		t.Fatalf("error = %v", err)
	}
	if manager.Running() != 1 {
		t.Fatalf("running = %d", manager.Running())
	}
	if err := manager.Kill(first.ID, "free the slot"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}
	waitFor(t, manager, first.ID)
	if _, err := manager.Start(context.Background(), Spec{Command: "echo ok"}); err != nil {
		t.Fatalf("Start returned error after a slot freed: %v", err)
	}
}

func TestStdinAndCloseStdin(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	job, err := manager.Start(context.Background(), Spec{Command: "read line; echo got:$line"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if err := manager.Write(job.ID, []byte("hello\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if err := manager.CloseStdin(job.ID); err != nil {
		t.Fatalf("CloseStdin returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	result, err := manager.Output(job.ID, 0, 0, 0)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !strings.Contains(string(result.Stdout.Data), "got:hello") {
		t.Fatalf("stdout = %q (job %#v)", result.Stdout.Data, final)
	}
	// Writing to a finished job reports that it is done.
	if err := manager.Write(job.ID, []byte("late\n")); err == nil {
		t.Fatal("writing to a finished job succeeded")
	}
}

func TestListAndNotFound(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	first, err := manager.Start(context.Background(), Spec{Command: "echo one"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	second, err := manager.Start(context.Background(), Spec{Command: "echo two"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	waitFor(t, manager, first.ID)
	waitFor(t, manager, second.ID)
	list := manager.List()
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list = %#v", list)
	}
	if _, err := manager.Get("job_does_not_exist"); !IsNotFound(err) {
		t.Fatalf("Get error = %v", err)
	}
	if err := manager.Kill("job_does_not_exist", ""); !IsNotFound(err) {
		t.Fatalf("Kill error = %v", err)
	}
	if _, err := manager.Output("job_does_not_exist", 0, 0, 0); !IsNotFound(err) {
		t.Fatalf("Output error = %v", err)
	}
	if err := manager.Write("job_does_not_exist", nil); !IsNotFound(err) {
		t.Fatalf("Write error = %v", err)
	}
}

func TestSpecValidationAndStartupFailure(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	if _, err := manager.Start(context.Background(), Spec{}); err == nil {
		t.Fatal("an empty command was accepted")
	}
	if _, err := manager.Start(context.Background(), Spec{Command: "true", CWD: "relative"}); err == nil {
		t.Fatal("a relative cwd was accepted")
	}
	// A command that cannot be started is recorded as failed, not panicked.
	job, err := manager.Start(context.Background(), Spec{Command: "true", CWD: "/definitely/not/here"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != StatusFailed || final.Error == "" {
		t.Fatalf("job = %#v", final)
	}
	manager.Close()
	if _, err := manager.Start(context.Background(), Spec{Command: "true"}); err == nil {
		t.Fatal("Start succeeded on a closed manager")
	}
}

func TestOutputReportsDroppedBytes(t *testing.T) {
	manager := New(Config{DefaultTimeout: 5 * time.Second})
	defer manager.Close()
	job, err := manager.Start(context.Background(), Spec{
		Command:        "for i in 1 2 3 4 5 6 7 8 9 10; do echo line-$i; done",
		MaxOutputBytes: 64,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if !final.StdoutDropped {
		t.Fatalf("dropped bytes were not reported: %#v", final)
	}
	// The head is still readable after the flood, which is the point of
	// keeping both ends.
	head, err := manager.Output(job.ID, 0, 0, 0)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !strings.HasPrefix(string(head.Stdout.Data), "line-1\nline-2") || head.Stdout.Dropped {
		t.Fatalf("head of the output was not kept: %#v", head.Stdout)
	}
	// Continuing past the head crosses the discarded middle, and the reader
	// is told how many bytes it missed.
	tail, err := manager.Output(job.ID, head.Stdout.Next, 0, 0)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !tail.Stdout.Dropped || tail.Stdout.Elided <= 0 {
		t.Fatalf("the hole was not reported and measured: %#v", tail.Stdout)
	}
	if len(tail.Stdout.Data) > 64 {
		t.Fatalf("retained %d bytes in one read with a 64-byte buffer", len(tail.Stdout.Data))
	}
}

func TestRunHonoursCallerCancellation(t *testing.T) {
	manager := New(Config{DefaultTimeout: 30 * time.Second})
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	job, _, err := manager.Run(ctx, Spec{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if job.Status != StatusKilled {
		t.Fatalf("job = %#v", job)
	}
	if _, err := manager.Get(job.ID); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Get returned error: %v", err)
	}
}

// waitForOutput polls a job's stdout until it contains want, which is how a
// session test observes output that arrives between writes.
func waitForOutput(t *testing.T, manager *Manager, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	seen := ""
	for time.Now().Before(deadline) {
		result, err := manager.Output(id, 0, 0, -1)
		if err != nil {
			t.Fatalf("Output returned error: %v", err)
		}
		seen = string(result.Stdout.Data)
		if strings.Contains(seen, want) {
			return seen
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never produced %q; saw %q", id, want, seen)
	return ""
}

func TestPTYJobSeesATerminalAndMergesStreams(t *testing.T) {
	manager := New(Config{DefaultTimeout: 10 * time.Second})
	defer manager.Close()
	command := "if [ -t 0 ]; then echo stdin-is-a-tty; else echo stdin-is-a-pipe; fi; echo diagnostics >&2"
	piped, _, err := manager.Run(context.Background(), Spec{Command: command})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	pipedResult, err := manager.Output(piped.ID, 0, 0, -1)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !strings.Contains(string(pipedResult.Stdout.Data), "stdin-is-a-pipe") {
		t.Fatalf("piped run saw a terminal: %q", pipedResult.Stdout.Data)
	}
	if !strings.Contains(string(pipedResult.Stderr.Data), "diagnostics") {
		t.Fatalf("piped run lost stderr: %q", pipedResult.Stderr.Data)
	}

	job, err := manager.Start(context.Background(), Spec{Command: command, PTY: true})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	seen := waitForOutput(t, manager, job.ID, "diagnostics")
	if !strings.Contains(seen, "stdin-is-a-tty") {
		t.Fatalf("terminal job did not see a terminal: %q", seen)
	}
	final := waitFor(t, manager, job.ID)
	result, err := manager.Output(job.ID, 0, 0, -1)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if final.StderrTotal != 0 || len(result.Stderr.Data) != 0 {
		t.Fatalf("a terminal has one stream, but stderr carried %d bytes", final.StderrTotal)
	}
	if !strings.Contains(string(result.Stdout.Data), "diagnostics") {
		t.Fatalf("terminal output lost the merged stderr: %q", result.Stdout.Data)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("terminal job exit = %#v", final.ExitCode)
	}
}

func TestPTYSessionAcceptsInputAndReportsItsExit(t *testing.T) {
	manager := New(Config{DefaultTimeout: 10 * time.Second})
	defer manager.Close()
	// A terminal session is driven by writes, not by a spec's stdin: the
	// script reads a line and answers it.
	job, err := manager.Start(context.Background(), Spec{Command: "read line; echo \"got:$line\"", PTY: true, Rows: 40, Cols: 120})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if job.Spec.Rows != 40 || job.Spec.Cols != 120 {
		t.Fatalf("terminal size was not recorded: %#v", job.Spec)
	}
	if err := manager.Write(job.ID, []byte("hello\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	// The terminal echoes what was typed, which is what makes an interactive
	// session readable, and the program's answer follows it.
	seen := waitForOutput(t, manager, job.ID, "got:hello")
	if !strings.Contains(seen, "hello") {
		t.Fatalf("typed input was not echoed: %q", seen)
	}
	final := waitFor(t, manager, job.ID)
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("session exit = %#v (error %q)", final.ExitCode, final.Error)
	}
}

func TestPTYJobGetsATerminalEnvironment(t *testing.T) {
	manager := New(Config{DefaultTimeout: 10 * time.Second})
	defer manager.Close()
	job, _, err := manager.Run(context.Background(), Spec{Command: "printf '%s' \"$TERM\"", PTY: true, Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	result, err := manager.Output(job.ID, 0, 0, -1)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !strings.Contains(string(result.Stdout.Data), "xterm") {
		t.Fatalf("TERM was not set for a terminal job: %q", result.Stdout.Data)
	}
}

func TestPTYSpecIsValidated(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{name: "rows-without-pty", spec: Spec{Command: "true", Rows: 40}},
		{name: "cols-without-pty", spec: Spec{Command: "true", Cols: 120}},
		{name: "rows-too-large", spec: Spec{Command: "true", PTY: true, Rows: MaxTerminalAxis + 1}},
		{name: "cols-negative", spec: Spec{Command: "true", PTY: true, Cols: -1}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.spec.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
	spec := Spec{Command: "true", PTY: true}
	if err := spec.Validate(); err != nil {
		t.Fatalf("a plain terminal spec was refused: %v", err)
	}
	rows, cols := spec.TerminalSize()
	if rows != DefaultRows || cols != DefaultCols {
		t.Fatalf("default terminal size = %dx%d", rows, cols)
	}
}

func TestStatusIsPublishedOnlyAfterTheOutputIsDrained(t *testing.T) {
	// The main process exits at once; its output follows from a background
	// writer. A terminal status must not appear in between, because a caller
	// that polls the job treats it as permission to read the result.
	manager := New(Config{DefaultTimeout: 10 * time.Second, DrainGrace: 5 * time.Second})
	defer manager.Close()
	job, err := manager.Start(context.Background(), Spec{Command: "(sleep 0.3; echo late) & exit 0"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	early, err := manager.Get(job.ID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if early.Status.Terminal() {
		t.Fatalf("status became %s before the output was drained (total %d)", early.Status, early.StdoutTotal)
	}
	earlyOutput, err := manager.Output(job.ID, 0, 0, -1)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !earlyOutput.Job.Running() {
		t.Fatalf("a job whose output is still arriving reported %s", earlyOutput.Job.Status)
	}
	final := waitFor(t, manager, job.ID)
	result, err := manager.Output(job.ID, 0, 0, -1)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if final.StdoutTotal == 0 || !strings.Contains(string(result.Stdout.Data), "late") {
		t.Fatalf("drained output was not readable at the terminal status: %#v", final)
	}
}
