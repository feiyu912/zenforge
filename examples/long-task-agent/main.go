// Command long-task-agent is a runnable example of a long task that stops
// durably in the middle of its work and finishes in a later process.
//
// The agent works through the task one tool-advised step at a time. The last
// step needs a human decision, so the tool that performs it returns an
// approval-required result instead of doing the work. ZenForge then
// checkpoints the waiting decision and stops the run: no terminal event is
// emitted, the process exits, and the run stays resumable. A second process
// over the same checkpoint store calls Agent.Resume, answers the pending
// decision, and the run finishes -- with the conversation, the tool results
// from before the pause, and the task state recovered from the checkpoint
// rather than rebuilt from scratch.
//
// Why the pause is an approval, and not the step limit: MaxSteps is a bound,
// not a boundary. When a run exhausts it, harness/runner.go appends a
// "tool-use limit" instruction, makes one more model call, and ends the run
// `run.done` in phase `completed`; Resume on that checkpoint only replays the
// terminal event. A waiting approval is the one public-API stop that leaves a
// genuinely resumable run, so that is the interruption this example uses.
// MaxSteps stays generous (default 6) so a scripted or real run reaches the
// approval instead of the finalization path.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalcli "github.com/feiyu912/zenforge/approval/cli"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
)

const (
	// programName prefixes stderr errors and names the program in the hint.
	programName = "long-task-agent"
	// exitPaused reports that the run is durably paused, not failed. It is
	// EX_TEMPFAIL from sysexits.h: running the printed resume command later is
	// expected to succeed, so a supervisor must retry rather than give up.
	exitPaused = 75

	recordToolName   = "record_step"
	finalizeToolName = "finalize_task"
	stepLogName      = "long-task.log"
	reportName       = "long-task-report.md"
)

// options is one invocation's configuration.
type options struct {
	task      string
	workspace string
	runDir    string
	runID     string
	resume    string
	maxSteps  int
}

func main() {
	var opts options
	flag.StringVar(&opts.task, "task", "Audit the workspace step by step, record every step you complete, then finalize the report.",
		"task the agent works through before it needs a decision")
	flag.StringVar(&opts.workspace, "workspace", ".", "workspace the task log and report are written to")
	flag.StringVar(&opts.runDir, "run-dir", envOr("ZENFORGE_RUN_DIR", ".zenforge/long-task"),
		"directory holding the durable checkpoints and event log")
	flag.StringVar(&opts.runID, "run-id", fmt.Sprintf("long_%d", time.Now().UnixNano()),
		"run id to start; ignored when -resume names one")
	flag.StringVar(&opts.resume, "resume", "", "resume this run id from its checkpoint instead of starting a run")
	flag.IntVar(&opts.maxSteps, "max-steps", 6, "maximum agent steps before the model is asked for a final answer")
	flag.Parse()

	// The id a run is known by must be settled before the run starts: a
	// process that is about to pause has to be able to tell its operator which
	// id to resume, and a caller that only learns the id afterwards cannot.
	if strings.TrimSpace(opts.resume) != "" {
		opts.runID = strings.TrimSpace(opts.resume)
	}
	if err := prepareWorkspace(opts.workspace); err != nil {
		fatal(err)
	}
	modelClient, err := provider.FromEnv()
	if err != nil {
		fatal(err)
	}
	if err := run(context.Background(), modelClient, opts); err != nil {
		fatal(err)
	}
}

func run(ctx context.Context, modelClient zenforge.Model, opts options) error {
	if opts.resume != "" {
		return resumeRun(ctx, modelClient, opts)
	}
	return startRun(ctx, modelClient, opts)
}

// startRun works the task until it finishes or pauses. A pause is not an
// error: the run is durable, so the example names the id and the command that
// will finish it, then exits with exitPaused.
func startRun(ctx context.Context, modelClient zenforge.Model, opts options) error {
	render := func(event zenforge.Event) { printEvent(os.Stdout, event, opts) }
	// No Approval broker is configured. That is the point: a tool that needs a
	// decision gets none, so the run checkpoints the waiting request and
	// returns instead of guessing on the operator's behalf.
	agent := newAgent(modelClient, opts, nil)
	result, err := agent.Run(ctx, zenforge.Task{
		RunID:   opts.runID,
		Input:   opts.task,
		OnEvent: render,
	})
	if errors.Is(err, approval.ErrRequired) {
		runID := opts.runID
		if result != nil && result.RunID != "" {
			runID = result.RunID
		}
		fmt.Fprintf(os.Stdout, "run: incomplete %s\n", runID)
		fmt.Fprintf(os.Stdout, "run: resume with: %s -run-dir %s -workspace %s -resume %s\n",
			programName, opts.runDir, opts.workspace, runID)
		os.Exit(exitPaused)
	}
	return err
}

// resumeRun loads the checkpoint the first process left behind and streams the
// rest of the run. The approval broker reads the operator's decision from
// stdin, exactly as the first process would have if it had been configured
// with one.
func resumeRun(ctx context.Context, modelClient zenforge.Model, opts options) error {
	broker := decisionReporter{inner: approvalcli.New(bufio.NewReader(os.Stdin), os.Stderr), out: os.Stdout}
	agent := newAgent(modelClient, opts, broker)
	events, err := agent.Resume(ctx, opts.runID)
	if err != nil {
		return err
	}
	return drain(os.Stdout, events, opts)
}

// newAgent builds the agent both processes use. Resume has to re-register the
// same tools: the checkpoint holds a pending finalize_task call, and the
// tool that answers it is looked up by name on the resuming agent.
func newAgent(modelClient zenforge.Model, opts options, broker approval.Broker) *zenforge.Agent {
	logPath := filepath.Join(opts.workspace, stepLogName)
	reportPath := filepath.Join(opts.workspace, reportName)

	recordStep := tools.Must(recordToolName,
		"Record one completed step of the long task in the workspace task log.",
		func(ctx context.Context, in recordStepInput) (recordStepOutput, error) {
			if in.Step <= 0 {
				return recordStepOutput{}, errors.New("step must be a positive number")
			}
			note := strings.TrimSpace(in.Note)
			if note == "" {
				return recordStepOutput{}, errors.New("note is required")
			}
			count, err := appendStep(logPath, in.Step, note)
			if err != nil {
				return recordStepOutput{}, err
			}
			return recordStepOutput{Step: in.Step, Note: note, Steps: count, LogPath: logPath}, nil
		})

	// finalizeTask closes the task out, so it is the step a human signs off
	// on: the wrapper turns it into an approval-requiring call without the
	// tool body knowing anything about approvals.
	finalizeTask := tools.Must(finalizeToolName,
		"Write the final long-task report. This call requires the operator's approval.",
		func(ctx context.Context, in finalizeInput) (finalizeOutput, error) {
			report := strings.TrimSpace(in.Report)
			if report == "" {
				return finalizeOutput{}, errors.New("report is required")
			}
			if err := writeReport(reportPath, logPath, report); err != nil {
				return finalizeOutput{}, err
			}
			return finalizeOutput{ReportPath: reportPath, Report: report}, nil
		})

	return zenforge.New(zenforge.Config{
		Model: modelClient,
		Instructions: "Work the task one step at a time. Call record_step for every step you complete, " +
			"then call finalize_task once with the final report. finalize_task needs the operator's " +
			"approval and may pause the run until it arrives.",
		Tools: []zenforge.Tool{
			recordStep,
			gatedTool{
				Tool: finalizeTask,
				request: approval.Request{
					Operation:   "long-task.finalize",
					Title:       "Finalize the long task",
					Description: "Write " + reportPath + " and close out the task log.",
					Risk:        approval.RiskMedium,
					Options:     approval.DefaultOptions(),
				},
			},
		},
		Events:      eventlogjsonl.New(opts.runDir),
		Checkpoints: checkpointjsonl.New(opts.runDir),
		Approval:    broker,
		MaxSteps:    opts.maxSteps,
	})
}

// drain prints a streamed run and reports how it ended.
func drain(out io.Writer, events <-chan zenforge.Event, opts options) error {
	var terminal error
	for event := range events {
		printEvent(out, event, opts)
		switch event.Type {
		case zenforge.EventRunError:
			terminal = errors.New(payloadText(event.Payload["error"]))
		case zenforge.EventRunCancelled:
			terminal = fmt.Errorf("run cancelled: %s", payloadText(event.Payload["error"]))
		}
	}
	return terminal
}

// printEvent writes one transcript line per observable step. The prefixes are
// the example's contract: run/step/tool/checkpoint/approval/answer.
func printEvent(out io.Writer, event zenforge.Event, opts options) {
	switch event.Type {
	case zenforge.EventRunStarted:
		fmt.Fprintf(out, "run: started %s\n", event.RunID())
	case zenforge.EventRunResumed:
		fmt.Fprintf(out, "run: resumed %s\n", event.RunID())
	case zenforge.EventStepStarted:
		fmt.Fprintf(out, "step: %v\n", event.Value("step"))
	case zenforge.EventToolCall:
		fmt.Fprintf(out, "tool: %v\n", event.Value("toolName"))
	case zenforge.EventApprovalRequested:
		fmt.Fprintf(out, "approval: requested %v\n", event.Value("toolName"))
	case zenforge.EventCheckpointCreated:
		// One line per durable boundary: the sequence the store accepted, the
		// phase that boundary records, and the file a resume reads. A run
		// checkpoints far more often than it takes a step (every model attempt
		// draft, every tool boundary), which is why a resume never depends on
		// state that only ever lived in memory.
		fmt.Fprintf(out, "checkpoint: seq=%v phase=%v file=%s\n",
			event.Value("checkpointSeq"), event.Value("phase"), checkpointPath(opts))
	case zenforge.EventRunDone:
		fmt.Fprintf(out, "run: done %s\n", event.RunID())
		fmt.Fprintf(out, "answer: %s\n", payloadText(event.Value("output")))
	}
}

// gatedTool makes any tool require an operator decision before it runs.
//
// A tool asks for a decision by returning approval.RequiredResult together
// with approval.ErrRequired. The run then checkpoints the waiting request and
// stops without a terminal event, which is what leaves a resumable run.
// Once the decision approves the call, the harness retries it with the
// decision stamped on tool.Context.Metadata, and the wrapped tool runs.
type gatedTool struct {
	tool.Tool
	request approval.Request
}

func (g gatedTool) Call(ctx context.Context, input json.RawMessage, call tool.Context) (tool.Result, error) {
	if approval.IsApprovedAction(call.Metadata[approval.MetadataDecisionAction]) {
		return g.Tool.Call(ctx, input, call)
	}
	return approval.RequiredResult(g.request), approval.ErrRequired
}

// decisionReporter prints the decision the operator actually gave. The broker
// itself reads the numbered choice from stdin; printing here means the
// transcript reports the real decision rather than assuming one.
type decisionReporter struct {
	inner approval.Broker
	out   io.Writer
}

func (b decisionReporter) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	decision, err := b.inner.Request(ctx, req)
	if err != nil {
		return decision, err
	}
	fmt.Fprintf(b.out, "approval: %s %s\n", req.ToolName, decision.Action)
	return decision, nil
}

type recordStepInput struct {
	Step int    `json:"step" jsonschema:"required,description=One-based number of the step that just completed"`
	Note string `json:"note" jsonschema:"required,description=What this step accomplished, in one line"`
}

type recordStepOutput struct {
	Step    int    `json:"step"`
	Note    string `json:"note"`
	Steps   int    `json:"steps"`
	LogPath string `json:"logPath"`
}

type finalizeInput struct {
	Report string `json:"report" jsonschema:"required,description=The final report text for the long task"`
}

type finalizeOutput struct {
	ReportPath string `json:"reportPath"`
	Report     string `json:"report"`
}

// taskLogMu serializes the read-modify-write of the task log.
var taskLogMu sync.Mutex

// appendStep adds one line to the workspace task log and reports how many
// steps the log now holds.
func appendStep(logPath string, step int, note string) (int, error) {
	taskLogMu.Lock()
	defer taskLogMu.Unlock()
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	if _, err := fmt.Fprintf(file, "- step %d: %s\n", step, note); err != nil {
		_ = file.Close()
		return 0, err
	}
	if err := file.Close(); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

// writeReport writes the final report. The steps come from the workspace log
// the earlier steps appended to, so the artifact carries the work that was
// already recorded when the run paused.
func writeReport(reportPath, logPath, report string) error {
	steps, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var builder strings.Builder
	builder.WriteString("# Long task report\n\n")
	builder.WriteString(report)
	builder.WriteString("\n\n## Recorded steps\n\n")
	if recorded := strings.TrimSpace(string(steps)); recorded != "" {
		builder.WriteString(recorded)
		builder.WriteString("\n")
	} else {
		builder.WriteString("(no steps recorded)\n")
	}
	return os.WriteFile(reportPath, []byte(builder.String()), 0o644)
}

// checkpointPath is the file the JSONL store keeps the latest checkpoint in,
// which is what a resume command has to point at.
func checkpointPath(opts options) string {
	return filepath.Join(opts.runDir, opts.runID, "latest.json")
}

func prepareWorkspace(workspace string) error {
	if strings.TrimSpace(workspace) == "" {
		return errors.New("workspace is required")
	}
	return os.MkdirAll(workspace, 0o755)
}

func payloadText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, programName+":", err)
	os.Exit(1)
}
