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

func TestBufferKeepsOffsetsAcrossDrops(t *testing.T) {
	buffer := NewBuffer(8)
	_, _ = buffer.Write([]byte("abcd"))
	_, _ = buffer.Write([]byte("efgh"))
	chunk := buffer.Read(0, 0)
	if string(chunk.Data) != "abcdefgh" || chunk.Dropped {
		t.Fatalf("chunk = %#v", chunk)
	}
	// Writing past the capacity drops the oldest bytes, and the reader is
	// told its view has a hole rather than silently losing them.
	_, _ = buffer.Write([]byte("ijkl"))
	chunk = buffer.Read(0, 0)
	if string(chunk.Data) != "efghijkl" || !chunk.Dropped {
		t.Fatalf("chunk = %#v", chunk)
	}
	// A reader that stayed current only sees the new bytes.
	chunk = buffer.Read(8, 0)
	if string(chunk.Data) != "ijkl" || chunk.Dropped {
		t.Fatalf("chunk = %#v", chunk)
	}
	if chunk.Total != 12 {
		t.Fatalf("total = %d", chunk.Total)
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
		MaxOutputBytes: 16,
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if !final.StdoutDropped {
		t.Fatalf("dropped bytes were not reported: %#v", final)
	}
	result, err := manager.Output(job.ID, 0, 0, 0)
	if err != nil {
		t.Fatalf("Output returned error: %v", err)
	}
	if !result.Stdout.Dropped {
		t.Fatal("the read did not report the hole")
	}
	if len(result.Stdout.Data) > 16 {
		t.Fatalf("retained %d bytes with a 16-byte buffer", len(result.Stdout.Data))
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
