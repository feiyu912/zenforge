// Package jobs exposes long-running commands to the model: exec_command
// starts a command in the foreground or the background, job_output polls a
// running job's output by offset, write_stdin feeds it, job_list reports
// what exists, and job_kill stops it. The command lifecycle lives in the
// jobs package; these tools are the schema and the rendering.
package jobs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	jobspkg "github.com/feiyu912/zenforge/jobs"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

// Tool names exposed to the model.
const (
	ExecName      = "exec_command"
	WriteName     = "write_stdin"
	OutputName    = "job_output"
	ListName      = "job_list"
	KillName      = "job_kill"
	DefaultMaxOut = 64 << 10
	DefaultWaitMs = 0
	MaxWaitMs     = 30000
)

// Description mirrors the reference execution model: one interface for
// foreground commands, background jobs, input, and output.
const (
	ExecDescription   = "Run a shell command. With background=true the command keeps running after this call returns and you poll it with job_output, feed it with write_stdin, and stop it with job_kill; use that for dev servers, watch loops, and anything long-running. Foreground commands block until they finish or hit timeoutMs and return their output and exit code. Set pty=true for an interactive session: the command runs on a terminal (so prompts, prompts-driven tools, and full-screen programs work), its output and errors arrive as one stream, and write_stdin feeds it."
	WriteDescription  = "Send input to a running job's stdin, optionally closing it afterwards so a reader-driven program learns that no more input is coming."
	OutputDescription = "Read a running or finished job's output. Pass the stdoutOffset/stderrOffset from the previous read to get only new bytes; the response returns the next offsets and flags when the job's bounded buffer dropped older output, with how many bytes were skipped, so you never silently miss it. A bounded buffer keeps the beginning of the stream as well as its newest bytes, so a flooded job still shows how it started. waitMs lets the call block briefly for new output instead of polling in a tight loop."
	ListDescription   = "List the jobs this session has started, with their status, exit codes, and byte counts."
	KillDescription   = "Stop a job and its process. Killing an already finished job is not an error."
)

// Config configures the tools.
type Config struct {
	// Manager runs the jobs. Required.
	Manager *jobspkg.Manager
	// DefaultCWD is used when a call names none.
	DefaultCWD string
	// MaxOutputBytes caps one job's stream when a call names none.
	MaxOutputBytes int
	// MaxWait bounds job_output's waitMs.
	MaxWait time.Duration
}

// Tools returns the job tools in registration order.
func Tools(config Config) ([]tool.Tool, error) {
	if config.Manager == nil {
		return nil, fmt.Errorf("%w: job manager is nil", tool.ErrInvalidTool)
	}
	exec, err := newExec(config)
	if err != nil {
		return nil, err
	}
	write, err := newWrite(config)
	if err != nil {
		return nil, err
	}
	output, err := newOutput(config)
	if err != nil {
		return nil, err
	}
	list, err := newList(config)
	if err != nil {
		return nil, err
	}
	kill, err := newKill(config)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{exec, write, output, list, kill}, nil
}

type execInput struct {
	Command        string `json:"command" jsonschema:"required,description=Shell command line to run"`
	Description    string `json:"description,omitempty" jsonschema:"description=Short human-readable description of what the command does"`
	CWD            string `json:"cwd,omitempty" jsonschema:"description=Absolute working directory; defaults to the session working directory"`
	Background     bool   `json:"background,omitempty" jsonschema:"description=Keep the command running after this call returns; poll it with job_output"`
	Stdin          string `json:"stdin,omitempty" jsonschema:"description=Input written to the command's stdin before it is read"`
	TimeoutMs      int    `json:"timeoutMs,omitempty" jsonschema:"description=Timeout in milliseconds; a foreground command returns on timeout, a background command is killed"`
	MaxOutputBytes int    `json:"maxOutputBytes,omitempty" jsonschema:"description=Per-stream output cap in bytes"`
	PTY            bool   `json:"pty,omitempty" jsonschema:"description=Run the command on a terminal: interactive programs behave normally, output and errors merge into one stream, and write_stdin feeds it"`
	Rows           int    `json:"rows,omitempty" jsonschema:"description=Terminal rows for a pty command; defaults to 24"`
	Cols           int    `json:"cols,omitempty" jsonschema:"description=Terminal columns for a pty command; defaults to 80"`
}

type execOutput struct {
	JobID       string `json:"jobId"`
	Status      string `json:"status"`
	Output      string `json:"output"`
	Error       string `json:"error,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	StdoutTotal int64  `json:"stdoutTotal,omitempty"`
	StderrTotal int64  `json:"stderrTotal,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	ElidedBytes int64  `json:"elidedBytes,omitempty"`
	DurationMs  int64  `json:"durationMs"`
}

func newExec(config Config) (tool.Tool, error) {
	return tools.New(ExecName, ExecDescription, func(ctx context.Context, in execInput) (execOutput, error) {
		if strings.TrimSpace(in.Command) == "" {
			return execOutput{}, fmt.Errorf("%w: exec_command requires a command", tool.ErrInvalidArguments)
		}
		cwd := strings.TrimSpace(in.CWD)
		if cwd == "" {
			cwd = config.DefaultCWD
		}
		spec := jobspkg.Spec{
			Command:        in.Command,
			CWD:            cwd,
			Stdin:          in.Stdin,
			Timeout:        time.Duration(in.TimeoutMs) * time.Millisecond,
			MaxOutputBytes: in.MaxOutputBytes,
			PTY:            in.PTY,
			Rows:           in.Rows,
			Cols:           in.Cols,
		}
		if spec.MaxOutputBytes <= 0 {
			spec.MaxOutputBytes = config.MaxOutputBytes
		}
		if !in.Background {
			// A foreground command is bounded: without a timeout a stuck
			// command would hang the whole turn.
			if spec.Timeout <= 0 {
				spec.Timeout = jobspkg.DefaultTimeout
			}
			job, result, err := config.Manager.Run(ctx, spec)
			if err != nil {
				return execOutput{}, err
			}
			return renderExec(job, result), nil
		}
		job, err := config.Manager.Start(ctx, spec)
		if err != nil {
			return execOutput{}, err
		}
		out := renderExec(job, jobspkg.Result{})
		out.Output = fmt.Sprintf("Started %s: %s\nPoll it with job_output(jobId=%q).", job.ID, strings.TrimSpace(job.Spec.Command), job.ID)
		return out, nil
	})
}

// renderExec builds the tool output for a job snapshot.
func renderExec(job jobspkg.Job, result jobspkg.Result) execOutput {
	out := execOutput{
		JobID:       job.ID,
		Status:      string(job.Status),
		Error:       job.Error,
		ExitCode:    job.ExitCode,
		Stdout:      string(result.Stdout.Data),
		Stderr:      string(result.Stderr.Data),
		StdoutTotal: job.StdoutTotal,
		StderrTotal: job.StderrTotal,
		Truncated:   result.Stdout.Dropped || result.Stderr.Dropped,
		ElidedBytes: result.Stdout.Elided + result.Stderr.Elided,
		DurationMs:  job.Duration.Milliseconds(),
	}
	var builder strings.Builder
	if out.Stdout != "" {
		builder.WriteString(strings.TrimRight(out.Stdout, "\n"))
	}
	if out.Stderr != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("[stderr]\n")
		builder.WriteString(strings.TrimRight(out.Stderr, "\n"))
	}
	fmt.Fprintf(&builder, "\n[job %s %s", job.ID, job.Status)
	if job.ExitCode != nil {
		fmt.Fprintf(&builder, " exit=%d", *job.ExitCode)
	}
	if job.Error != "" {
		fmt.Fprintf(&builder, " error=%q", job.Error)
	}
	if out.Truncated {
		fmt.Fprintf(&builder, " output-dropped(%d)", out.ElidedBytes)
	}
	builder.WriteString("]")
	out.Output = strings.TrimLeft(builder.String(), "\n")
	return out
}

type writeInput struct {
	JobID string `json:"jobId" jsonschema:"required,description=Job to write to"`
	Input string `json:"input,omitempty" jsonschema:"description=Bytes to write; a trailing newline is not added"`
	Close bool   `json:"close,omitempty" jsonschema:"description=Close stdin after writing, telling the program no more input is coming"`
}

type writeOutput struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

func newWrite(config Config) (tool.Tool, error) {
	return tools.New(WriteName, WriteDescription, func(ctx context.Context, in writeInput) (writeOutput, error) {
		id := strings.TrimSpace(in.JobID)
		if id == "" {
			return writeOutput{}, fmt.Errorf("%w: write_stdin requires a jobId", tool.ErrInvalidArguments)
		}
		if in.Input != "" {
			if err := config.Manager.Write(id, []byte(in.Input)); err != nil {
				return writeOutput{}, jobError(err)
			}
		} else if !in.Close {
			return writeOutput{}, fmt.Errorf("%w: write_stdin requires input or close", tool.ErrInvalidArguments)
		}
		if in.Close {
			if err := config.Manager.CloseStdin(id); err != nil {
				return writeOutput{}, jobError(err)
			}
		}
		job, err := config.Manager.Get(id)
		if err != nil {
			return writeOutput{}, jobError(err)
		}
		action := "wrote %d bytes to %s"
		if in.Close {
			action = "wrote %d bytes to %s and closed stdin"
		}
		return writeOutput{
			JobID:  job.ID,
			Status: string(job.Status),
			Output: fmt.Sprintf(action, len(in.Input), job.ID),
		}, nil
	})
}

type outputInput struct {
	JobID        string `json:"jobId" jsonschema:"required,description=Job to read"`
	StdoutOffset int64  `json:"stdoutOffset,omitempty" jsonschema:"description=Absolute stdout offset from the previous read; omit for the start"`
	StderrOffset int64  `json:"stderrOffset,omitempty" jsonschema:"description=Absolute stderr offset from the previous read; omit for the start"`
	MaxBytes     int    `json:"maxBytes,omitempty" jsonschema:"description=Maximum bytes per stream; defaults to 64KiB"`
	WaitMs       int    `json:"waitMs,omitempty" jsonschema:"description=Block up to this long for new output or for the job to finish"`
}

type outputOutput struct {
	JobID    string `json:"jobId"`
	Status   string `json:"status"`
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	// Stdout and Stderr are always present, even when empty. This is the
	// polling result: a poll that found no new bytes must answer "zero bytes"
	// rather than omit the field, or a client accumulating output cannot tell
	// an empty poll from a response shape it does not understand. The
	// offsets already say where the next read starts, and the status says
	// whether the job is still running.
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	StdoutOffset  int64  `json:"stdoutOffset"`
	StderrOffset  int64  `json:"stderrOffset"`
	StdoutTotal   int64  `json:"stdoutTotal"`
	StderrTotal   int64  `json:"stderrTotal"`
	StdoutDropped bool   `json:"stdoutDropped,omitempty"`
	StderrDropped bool   `json:"stderrDropped,omitempty"`
	ElidedBytes   int64  `json:"elidedBytes,omitempty"`
	Running       bool   `json:"running"`
	DurationMs    int64  `json:"durationMs"`
	Hint          string `json:"hint,omitempty"`
}

func newOutput(config Config) (tool.Tool, error) {
	return tools.New(OutputName, OutputDescription, func(ctx context.Context, in outputInput) (outputOutput, error) {
		id := strings.TrimSpace(in.JobID)
		if id == "" {
			return outputOutput{}, fmt.Errorf("%w: job_output requires a jobId", tool.ErrInvalidArguments)
		}
		maxBytes := in.MaxBytes
		if maxBytes <= 0 {
			maxBytes = DefaultMaxOut
		}
		if _, err := config.Manager.Get(id); err != nil {
			return outputOutput{}, jobError(err)
		}
		deadline := time.Now().Add(config.waitDuration(in.WaitMs))
		for {
			result, err := config.Manager.Output(id, in.StdoutOffset, in.StderrOffset, maxBytes)
			if err != nil {
				return outputOutput{}, jobError(err)
			}
			if len(result.Stdout.Data) > 0 || len(result.Stderr.Data) > 0 || result.Job.Status.Terminal() || time.Now().After(deadline) {
				return renderOutput(result, in, maxBytes), nil
			}
			if err := ctx.Err(); err != nil {
				return outputOutput{}, err
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// waitDuration clamps the requested wait to the configured maximum.
func (c Config) waitDuration(requestedMs int) time.Duration {
	limit := c.MaxWait
	if limit <= 0 {
		limit = MaxWaitMs * time.Millisecond
	}
	wait := time.Duration(requestedMs) * time.Millisecond
	if wait <= 0 {
		return 0
	}
	if wait > limit {
		return limit
	}
	return wait
}

// renderOutput builds the tool output for one read.
func renderOutput(result jobspkg.Result, in outputInput, maxBytes int) outputOutput {
	job := result.Job
	out := outputOutput{
		JobID:         job.ID,
		Status:        string(job.Status),
		Error:         job.Error,
		ExitCode:      job.ExitCode,
		Stdout:        string(result.Stdout.Data),
		Stderr:        string(result.Stderr.Data),
		StdoutOffset:  result.Stdout.Next,
		StderrOffset:  result.Stderr.Next,
		StdoutTotal:   job.StdoutTotal,
		StderrTotal:   job.StderrTotal,
		StdoutDropped: result.Stdout.Dropped || job.StdoutDropped,
		StderrDropped: result.Stderr.Dropped || job.StderrDropped,
		ElidedBytes:   result.Stdout.Elided + result.Stderr.Elided,
		Running:       job.Running(),
		DurationMs:    job.Duration.Milliseconds(),
	}
	var builder strings.Builder
	if out.Stdout != "" {
		builder.WriteString(strings.TrimRight(out.Stdout, "\n"))
	}
	if out.Stderr != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("[stderr]\n")
		builder.WriteString(strings.TrimRight(out.Stderr, "\n"))
	}
	if builder.Len() == 0 {
		builder.WriteString("(no new output)")
	}
	fmt.Fprintf(&builder, "\n[job %s %s stdout=%d stderr=%d", job.ID, job.Status, out.StdoutOffset, out.StderrOffset)
	if job.ExitCode != nil {
		fmt.Fprintf(&builder, " exit=%d", *job.ExitCode)
	}
	if out.StdoutDropped || out.StderrDropped {
		fmt.Fprintf(&builder, " output-dropped(%d)", out.ElidedBytes)
	}
	builder.WriteString("]")
	out.Output = builder.String()
	// A running job with no new output tells the model how to proceed
	// rather than leaving it to guess.
	if out.Running && out.Stdout == "" && out.Stderr == "" && int64(maxBytes) <= job.StdoutTotal {
		out.Hint = "the job is still running; pass waitMs to block for new output"
	} else if out.Running {
		out.Hint = "the job is still running; poll again with the returned offsets"
	}
	return out
}

type listInput struct{}

type listOutput struct {
	Jobs   []jobView `json:"jobs"`
	Output string    `json:"output"`
}

type jobView struct {
	JobID       string `json:"jobId"`
	Command     string `json:"command"`
	Status      string `json:"status"`
	ExitCode    *int   `json:"exitCode,omitempty"`
	Error       string `json:"error,omitempty"`
	Running     bool   `json:"running"`
	StdoutTotal int64  `json:"stdoutTotal"`
	StderrTotal int64  `json:"stderrTotal"`
	DurationMs  int64  `json:"durationMs"`
}

func newList(config Config) (tool.Tool, error) {
	instance, err := tools.New(ListName, ListDescription, func(context.Context, listInput) (listOutput, error) {
		jobs := config.Manager.List()
		views := make([]jobView, 0, len(jobs))
		for _, job := range jobs {
			views = append(views, jobView{
				JobID:       job.ID,
				Command:     strings.TrimSpace(job.Spec.Command),
				Status:      string(job.Status),
				ExitCode:    job.ExitCode,
				Error:       job.Error,
				Running:     job.Running(),
				StdoutTotal: job.StdoutTotal,
				StderrTotal: job.StderrTotal,
				DurationMs:  job.Duration.Milliseconds(),
			})
		}
		if len(views) == 0 {
			return listOutput{Jobs: views, Output: "no jobs"}, nil
		}
		lines := make([]string, 0, len(views))
		for _, view := range views {
			line := fmt.Sprintf("%s %s [%s", view.JobID, view.Command, view.Status)
			if view.ExitCode != nil {
				line += fmt.Sprintf(" exit=%d", *view.ExitCode)
			}
			line += "]"
			lines = append(lines, line)
		}
		return listOutput{Jobs: views, Output: strings.Join(lines, "\n")}, nil
	})
	if err != nil {
		return nil, err
	}
	// Listing reads state and changes nothing, so plan mode allows it.
	return tools.ReadOnly(instance), nil
}

type killInput struct {
	JobID  string `json:"jobId" jsonschema:"required,description=Job to stop"`
	Reason string `json:"reason,omitempty" jsonschema:"description=Short reason recorded on the job"`
}

type killOutput struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
	Output string `json:"output"`
}

func newKill(config Config) (tool.Tool, error) {
	return tools.New(KillName, KillDescription, func(ctx context.Context, in killInput) (killOutput, error) {
		id := strings.TrimSpace(in.JobID)
		if id == "" {
			return killOutput{}, fmt.Errorf("%w: job_kill requires a jobId", tool.ErrInvalidArguments)
		}
		if err := config.Manager.Kill(id, in.Reason); err != nil {
			return killOutput{}, jobError(err)
		}
		job, err := config.Manager.Get(id)
		if err != nil {
			return killOutput{}, jobError(err)
		}
		return killOutput{
			JobID:  job.ID,
			Status: string(job.Status),
			Output: fmt.Sprintf("Stopped %s (%s).", job.ID, job.Status),
		}, nil
	})
}

// jobError maps a jobs-package error onto a tool error.
func jobError(err error) error {
	if jobspkg.IsNotFound(err) {
		return fmt.Errorf("%w: %v; use job_list to see the jobs this session started", tool.ErrInvalidArguments, err)
	}
	return err
}

// SortedJobs is a test helper: job ids in order.
func SortedJobs(jobs []jobspkg.Job) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	sort.Strings(ids)
	return ids
}

// IsNotFound exposes the jobs sentinel check to callers that only import
// this package.
func IsNotFound(err error) bool { return jobspkg.IsNotFound(err) }
