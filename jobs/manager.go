package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults for a manager.
const (
	DefaultTimeout      = 10 * time.Minute
	DefaultMaxJobs      = 16
	DefaultReadMaxBytes = 64 << 10
)

// Config configures a Manager.
type Config struct {
	// Shell is the program that runs a command line, "/bin/sh" by default.
	Shell string
	// DefaultCWD is used when a spec names none.
	DefaultCWD string
	// DefaultTimeout bounds a job whose spec sets none.
	DefaultTimeout time.Duration
	// MaxJobs bounds concurrent running jobs. Starting past the limit is
	// an error rather than a silent queue, so the caller learns to stop
	// something.
	MaxJobs int
	// Env is the base environment; empty inherits the manager process.
	Env []string
}

// Manager runs and tracks jobs.
type Manager struct {
	shell          string
	defaultCWD     string
	defaultTimeout time.Duration
	maxJobs        int
	env            []string

	mu     sync.Mutex
	jobs   map[string]*record
	order  []string
	nextID atomic.Int64
	closed bool
}

// record is the manager's mutable view of one job.
type record struct {
	job    Job
	stdout *Buffer
	stderr *Buffer
	cancel context.CancelFunc
	timer  *time.Timer
	done   chan struct{}
	stdin  io.WriteCloser
}

// New returns a manager with defaults applied.
func New(config Config) *Manager {
	shell := strings.TrimSpace(config.Shell)
	if shell == "" {
		shell = "/bin/sh"
	}
	timeout := config.DefaultTimeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	maxJobs := config.MaxJobs
	if maxJobs <= 0 {
		maxJobs = DefaultMaxJobs
	}
	return &Manager{
		shell:          shell,
		defaultCWD:     config.DefaultCWD,
		defaultTimeout: timeout,
		maxJobs:        maxJobs,
		env:            append([]string(nil), config.Env...),
		jobs:           map[string]*record{},
	}
}

// Start launches a job and returns immediately. The returned job is a
// snapshot: poll with Get or Output for progress.
func (m *Manager) Start(ctx context.Context, spec Spec) (Job, error) {
	if err := spec.Validate(); err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("job manager is closed")
	}
	if running := m.runningLocked(); running >= m.maxJobs {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("job limit reached (%d running); stop a job before starting another", running)
	}
	id := fmt.Sprintf("job_%d", m.nextID.Add(1))
	capacity := spec.MaxOutputBytes
	if capacity <= 0 {
		capacity = DefaultBufferBytes
	}
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = m.defaultTimeout
	}
	// The job's context is detached from the caller's: a background job
	// outlives the tool call that started it, and the manager's own timeout
	// and Kill are what stop it. The caller's cancellation is honoured only
	// for the startup itself.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	rec := &record{
		job: Job{
			ID:        id,
			Spec:      spec,
			Status:    StatusRunning,
			StartedAt: time.Now(),
		},
		stdout: NewBuffer(capacity),
		stderr: NewBuffer(capacity),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.jobs[id] = rec
	m.order = append(m.order, id)
	m.mu.Unlock()

	command := exec.CommandContext(jobCtx, m.shell, "-c", spec.Command)
	command.Dir = m.workingDir(spec)
	command.Env = m.environment(spec)
	stdout, err := command.StdoutPipe()
	if err != nil {
		m.fail(id, fmt.Errorf("capture stdout: %w", err))
		return m.snapshot(id)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		m.fail(id, fmt.Errorf("capture stderr: %w", err))
		return m.snapshot(id)
	}
	// stdin is always a pipe: a job started without input may still be fed
	// later, and a closed pipe is how a reader-driven program is told that
	// no more input is coming.
	stdin, err := command.StdinPipe()
	if err != nil {
		m.fail(id, fmt.Errorf("capture stdin: %w", err))
		return m.snapshot(id)
	}
	m.mu.Lock()
	rec.stdin = stdin
	m.mu.Unlock()
	if spec.Stdin != "" {
		if _, err := io.WriteString(stdin, spec.Stdin); err != nil {
			m.fail(id, fmt.Errorf("write stdin: %w", err))
			return m.snapshot(id)
		}
	}
	if err := command.Start(); err != nil {
		m.fail(id, fmt.Errorf("start command: %w", err))
		return m.snapshot(id)
	}
	m.mu.Lock()
	rec.job.PID = command.Process.Pid
	if timeout > 0 {
		rec.timer = time.AfterFunc(timeout, func() {
			m.kill(id, fmt.Sprintf("timeout after %s", timeout))
		})
	}
	m.mu.Unlock()

	go m.collect(rec, stdout, stderr)
	go m.wait(rec, command)
	return m.snapshot(id)
}

// Run starts a job and waits for it, returning the job with its buffered
// output. It is the foreground form of Start.
func (m *Manager) Run(ctx context.Context, spec Spec) (Job, Result, error) {
	job, err := m.Start(ctx, spec)
	if err != nil {
		return job, Result{}, err
	}
	m.mu.Lock()
	rec, ok := m.jobs[job.ID]
	m.mu.Unlock()
	if !ok {
		return job, Result{}, &ErrNotFound{ID: job.ID}
	}
	select {
	case <-rec.done:
	case <-ctx.Done():
		_ = m.Kill(job.ID, "caller cancelled")
		<-rec.done
	}
	result, err := m.Output(job.ID, 0, 0, 0)
	if err != nil {
		return job, result, err
	}
	final, err := m.Get(job.ID)
	return final, result, err
}

// Get returns a snapshot of one job.
func (m *Manager) Get(id string) (Job, error) { return m.snapshot(id) }

// List returns snapshots in start order.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.viewLocked(m.jobs[id]))
	}
	return out
}

// Result is one read of a job's output.
type Result struct {
	Job    Job
	Stdout Chunk
	Stderr Chunk
}

// Output reads both streams starting at the given offsets. A max of zero
// uses DefaultReadMaxBytes; a negative max reads everything retained.
func (m *Manager) Output(id string, sinceStdout, sinceStderr int64, max int) (Result, error) {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return Result{}, &ErrNotFound{ID: id}
	}
	// The job view is taken while the lock is held: the reaper writes the
	// job's status and exit code under the same lock, so reading it after
	// unlocking would race with a job that is finishing right now.
	view := m.viewLocked(rec)
	m.mu.Unlock()
	if max == 0 {
		max = DefaultReadMaxBytes
	}
	if max < 0 {
		max = int(^uint(0) >> 1)
	}
	return Result{
		Job:    view,
		Stdout: rec.stdout.Read(sinceStdout, max),
		Stderr: rec.stderr.Read(sinceStderr, max),
	}, nil
}

// Write sends input to a running job's stdin.
func (m *Manager) Write(id string, data []byte) error {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return &ErrNotFound{ID: id}
	}
	stdin := rec.stdin
	view := m.viewLocked(rec)
	m.mu.Unlock()
	if view.Status.Terminal() {
		return fmt.Errorf("job %s has already finished", id)
	}
	if stdin == nil {
		return fmt.Errorf("job %s has no stdin", id)
	}
	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("write to job %s: %w", id, err)
	}
	return nil
}

// CloseStdin closes the job's stdin, which is how a reader-driven program
// is told that no more input is coming.
func (m *Manager) CloseStdin(id string) error {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return &ErrNotFound{ID: id}
	}
	stdin := rec.stdin
	m.mu.Unlock()
	if stdin == nil {
		return fmt.Errorf("job %s has no stdin", id)
	}
	return stdin.Close()
}

// Kill stops a job. Killing a finished job succeeds: the caller's intent
// (the job must not be running) is already satisfied.
func (m *Manager) Kill(id, reason string) error { return m.kill(id, reason) }

func (m *Manager) kill(id, reason string) error {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return &ErrNotFound{ID: id}
	}
	cancel, timer := rec.cancel, rec.timer
	terminal := m.viewLocked(rec).Status.Terminal()
	if reason == "" {
		reason = "killed"
	}
	if !terminal {
		rec.job.Status = StatusKilled
		rec.job.Error = reason
	}
	if timer != nil {
		timer.Stop()
	}
	m.mu.Unlock()
	if !terminal {
		cancel()
	}
	return nil
}

// Close stops every job and refuses further starts.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	records := make([]*record, 0, len(m.jobs))
	for _, rec := range m.jobs {
		records = append(records, rec)
	}
	// A record that is already terminal is skipped; the status is read
	// through the manager's own snapshot, which takes the lock, because the
	// reaper may be finishing a job concurrently.
	m.mu.Unlock()
	for _, rec := range records {
		view, err := m.snapshot(rec.job.ID)
		if err == nil && view.Status.Terminal() {
			continue
		}
		_ = m.kill(rec.job.ID, "manager closed")
	}
}

// Running is the number of live jobs.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runningLocked()
}

func (m *Manager) runningLocked() int {
	count := 0
	for _, rec := range m.jobs {
		if !m.viewLocked(rec).Status.Terminal() {
			count++
		}
	}
	return count
}

// workingDir validates and resolves a spec's working directory.
func (m *Manager) workingDir(spec Spec) string {
	if spec.CWD != "" {
		return spec.CWD
	}
	return m.defaultCWD
}

// environment resolves a spec's environment.
func (m *Manager) environment(spec Spec) []string {
	if len(spec.Env) > 0 {
		return spec.Env
	}
	if len(m.env) > 0 {
		return m.env
	}
	return os.Environ()
}

// collect drains both pipes into their buffers until they close.
func (m *Manager) collect(rec *record, stdout, stderr io.Reader) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(rec.stdout, stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(rec.stderr, stderr)
	}()
	wg.Wait()
}

// wait reaps the process and records the outcome.
func (m *Manager) wait(rec *record, command *exec.Cmd) {
	err := command.Wait()
	m.mu.Lock()
	if rec.timer != nil {
		rec.timer.Stop()
	}
	ended := time.Now()
	rec.job.EndedAt = &ended
	if code := command.ProcessState.ExitCode(); err == nil || command.ProcessState != nil {
		rec.job.ExitCode = &code
	}
	switch {
	case rec.job.Status == StatusKilled:
		// Kill already recorded the status and reason.
	case command.ProcessState != nil:
		// The process ran and exited, whatever its exit code: a non-zero
		// code is a result the caller reads, not a failure of the manager.
		rec.job.Status = StatusExited
		if err != nil {
			rec.job.Error = err.Error()
		}
	case err != nil:
		rec.job.Status = StatusFailed
		rec.job.Error = err.Error()
	default:
		rec.job.Status = StatusExited
	}
	cancel := rec.cancel
	m.mu.Unlock()
	cancel()
	close(rec.done)
}

// fail records a startup failure.
func (m *Manager) fail(id string, cause error) {
	m.mu.Lock()
	rec, ok := m.jobs[id]
	if ok {
		ended := time.Now()
		rec.job.Status = StatusFailed
		rec.job.Error = cause.Error()
		rec.job.EndedAt = &ended
		if rec.timer != nil {
			rec.timer.Stop()
		}
	}
	m.mu.Unlock()
}

// snapshot returns a copy of one job.
func (m *Manager) snapshot(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.jobs[id]
	if !ok {
		return Job{}, &ErrNotFound{ID: id}
	}
	return m.viewLocked(rec), nil
}

// viewLocked builds the immutable view of a record.
func (m *Manager) viewLocked(rec *record) Job {
	job := rec.job
	job.StdoutTotal = rec.stdout.Total()
	job.StderrTotal = rec.stderr.Total()
	// A stream that lost bytes to the ring buffer reports it, so a reader
	// never believes it has seen everything.
	job.StdoutDropped = rec.stdout.Total() > int64(rec.stdout.Len())
	job.StderrDropped = rec.stderr.Total() > int64(rec.stderr.Len())
	end := time.Now()
	if job.EndedAt != nil {
		end = *job.EndedAt
	}
	job.Duration = end.Sub(job.StartedAt)
	return job
}
