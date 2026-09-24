package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Agent assembly.
//
// Layer choice: this runner is built on Eino's ADK layer
// (github.com/cloudwego/eino/adk), not on the lower-level
// github.com/cloudwego/eino/flow/agent/react helper.
//
// Reason: react.NewAgent compiles its internal graph with a fixed
// GraphCompileOption list (adk-less react.go passes only WithMaxRunSteps,
// WithNodeTriggerMode and WithGraphName), so it exposes no way to install a
// CheckPointStore and therefore cannot take a durable pause. The ADK's
// ChatModelAgent is the same ReAct loop with a persistence hook:
// adk.RunnerConfig.CheckPointStore plus the adk.WithCheckPointID run option.
// See README.md for the exact call sites.

const agentName = "zenforge-bench-eino"

// chatModelFactory builds the ToolCallingChatModel that points at the
// OpenAI-compatible endpoint. It is a package variable so tests can install a
// scripted model and exercise the whole graph without a network endpoint.
var chatModelFactory = func(ctx context.Context, cfg Config) (model.ToolCallingChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
		Timeout: 2 * time.Minute,
	})
}

// maxApprovalRounds bounds the in-process approval loop so a script that keeps
// re-interrupting fails loudly instead of spinning.
const maxApprovalRounds = 8

const baseInstruction = "You are an autonomous agent working inside a sandboxed workspace. " +
	"Use the provided tools to carry out the user's request. " +
	"Paths given to read_file and write_file are resolved inside the workspace, and a path outside it is refused. " +
	"run_shell executes a real command in the workspace, but only after it has been approved."

// Execute runs one benchmark process and returns the result to report.
func Execute(cfg Config) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = NewResult(cfg.Task, cfg.Phase, StatusFailed, fmt.Sprintf("panic: %v", p))
		}
	}()

	ws, err := newWorkspace(cfg.Workspace)
	if err != nil {
		return NewResult(cfg.Task, cfg.Phase, StatusFailed, err.Error())
	}

	ctx := context.Background()

	chatModel, err := chatModelFactory(ctx, cfg)
	if err != nil {
		return NewResult(cfg.Task, cfg.Phase, StatusFailed, fmt.Sprintf("building chat model: %v", err))
	}

	tools, err := newTools(ws, cfg.Approval)
	if err != nil {
		return NewResult(cfg.Task, cfg.Phase, StatusFailed, fmt.Sprintf("building tools: %v", err))
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        agentName,
		Description: "Eino ReAct agent for the cross-framework benchmark",
		Instruction: baseInstruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	if err != nil {
		return NewResult(cfg.Task, cfg.Phase, StatusFailed, fmt.Sprintf("building agent: %v", err))
	}

	store, err := newFileCheckPointStore(cfg.StateDir)
	if err != nil {
		return NewResult(cfg.Task, cfg.Phase, StatusFailed, err.Error())
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: false,
		CheckPointStore: store,
	})

	checkpointID := cfg.CheckpointID()

	var iter *adk.AsyncIterator[*adk.AgentEvent]
	if cfg.Phase == PhaseResume {
		iter, err = resumeFromCheckpoint(ctx, runner, cfg, checkpointID)
		if err != nil {
			return NewResult(cfg.Task, cfg.Phase, StatusFailed, err.Error())
		}
	} else {
		iter = runner.Query(ctx, userQuery(cfg), adk.WithCheckPointID(checkpointID))
	}

	return drive(ctx, runner, iter, cfg, checkpointID)
}

// resumeFromCheckpoint starts the second process from the checkpoint the first
// process wrote. It targets exactly the interrupt ids recorded by the pause, so
// the approval answer travels as Eino resume data.
func resumeFromCheckpoint(ctx context.Context, runner *adk.Runner, cfg Config, checkpointID string) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	st, err := readResumeState(cfg.StateDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("BENCH_PHASE=resume but the run phase wrote no durable state (%s)",
				filepath.Join(cfg.StateDir, resumeStateFile))
		}
		return nil, err
	}
	if len(st.InterruptIDs) == 0 {
		// Implicit resume: Eino's own resume-all strategy.
		return runner.Resume(ctx, checkpointID)
	}
	targets := make(map[string]any, len(st.InterruptIDs))
	for _, id := range st.InterruptIDs {
		targets[id] = cfg.Approval
	}
	return runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{Targets: targets})
}

// drive consumes the runner's event stream, answers approval interrupts, and
// turns the outcome into a Result.
//
// Eino writes the checkpoint *before* delivering the interrupt event
// (adk/runner.go: runnerSaveCheckPointImpl, then gen.Send), so when the pause
// decision is taken the durable state is already on disk.
func drive(ctx context.Context, runner *adk.Runner, iter *adk.AsyncIterator[*adk.AgentEvent], cfg Config, checkpointID string) Result {
	var (
		pending     []*adk.InterruptCtx
		rounds      int
		toolCalls   []string
		lastMessage string
		paused      bool
	)

	for {
		event, ok := iter.Next()
		if !ok {
			if len(pending) == 0 {
				break
			}
			targets := make(map[string]any, len(pending))
			for _, ic := range pending {
				targets[ic.ID] = cfg.Approval
			}
			next, err := runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{Targets: targets})
			if err != nil {
				return NewResult(cfg.Task, cfg.Phase, StatusFailed,
					fmt.Sprintf("resuming from approval interrupt failed: %v", err))
			}
			iter = next
			pending = nil
			rounds++
			if rounds > maxApprovalRounds {
				return NewResult(cfg.Task, cfg.Phase, StatusFailed,
					fmt.Sprintf("gave up after %d approval interrupts without finishing", rounds))
			}
			continue
		}

		if event.Err != nil {
			return NewResult(cfg.Task, cfg.Phase, StatusFailed, event.Err.Error())
		}

		if out := event.Output; out != nil && out.MessageOutput != nil && out.MessageOutput.Message != nil {
			msg := out.MessageOutput.Message
			if msg.Content != "" {
				lastMessage = msg.Content
			}
			if msg.Role == schema.Assistant {
				for _, tc := range msg.ToolCalls {
					toolCalls = append(toolCalls, tc.Function.Name)
				}
			}
		}

		if event.Action == nil || event.Action.Interrupted == nil {
			continue
		}

		contexts := toolInterrupts(event.Action.Interrupted.InterruptContexts)

		if cfg.RequirePause && cfg.Phase == PhaseRun {
			ids := make([]string, 0, len(contexts))
			commands := make([]string, 0, len(contexts))
			for _, ic := range contexts {
				ids = append(ids, ic.ID)
				if s, ok := ic.Info.(string); ok {
					commands = append(commands, oneLine(s))
				}
			}
			if err := writeResumeState(cfg.StateDir, resumeState{
				Task:         cfg.Task,
				CheckpointID: checkpointID,
				InterruptIDs: ids,
				Commands:     commands,
				Framework:    frameworkName,
			}); err != nil {
				return NewResult(cfg.Task, cfg.Phase, StatusFailed,
					fmt.Sprintf("writing durable resume state: %v", err))
			}
			paused = true
			return NewResult(cfg.Task, cfg.Phase, StatusPaused,
				fmt.Sprintf("paused on %d approval interrupt(s) for task %s; checkpoint %q persisted under BENCH_STATE_DIR",
					len(ids), cfg.Task, checkpointID))
		}

		if len(contexts) == 0 {
			return NewResult(cfg.Task, cfg.Phase, StatusFailed,
				"run was interrupted but no tool interrupt context was reported")
		}
		pending = contexts
	}

	if paused {
		// Unreachable: the pause path returns directly.
		return NewResult(cfg.Task, cfg.Phase, StatusPaused, "paused")
	}

	if cfg.RequirePause && cfg.Phase == PhaseRun {
		return NewResult(cfg.Task, cfg.Phase, StatusFailed,
			"BENCH_REQUIRE_PAUSE=1 but the run finished without taking a durable pause")
	}

	return NewResult(cfg.Task, cfg.Phase, StatusCompleted, completedDetail(lastMessage, toolCalls))
}

func completedDetail(lastMessage string, toolCalls []string) string {
	detail := "agent loop finished"
	if len(toolCalls) > 0 {
		detail += fmt.Sprintf("; tools called: %v", toolCalls)
	}
	if lastMessage != "" {
		detail += "; final answer: " + oneLine(lastMessage)
	}
	return detail
}

// toolInterrupts picks the interrupt contexts that point at a tool.
//
// ToInterruptContexts returns root-cause contexts (the leaves) with Parent
// chains, so both the listed contexts and their parents are examined.
func toolInterrupts(contexts []*adk.InterruptCtx) []*adk.InterruptCtx {
	var out []*adk.InterruptCtx
	seen := make(map[string]bool)
	for _, root := range contexts {
		for ic := root; ic != nil; ic = ic.Parent {
			if len(ic.Address) == 0 || ic.ID == "" || seen[ic.ID] {
				continue
			}
			if ic.Address[len(ic.Address)-1].Type != adk.AddressSegmentTool {
				continue
			}
			seen[ic.ID] = true
			out = append(out, ic)
		}
	}
	return out
}

// userQuery returns the user message for this run.
//
// BENCH_QUERY carries the frozen task instruction and is used verbatim: prompt
// bytes are a reported cost metric, so a framework that invents its own user
// wording would make that column measure the runner rather than the framework.
// The runner's own framing belongs entirely to the system prompt
// (baseInstruction above), which is Eino's legitimate, measured cost.
//
// If BENCH_QUERY is absent the runner falls back to a fixed per-task sentence so
// it still runs against a harness that predates the variable; the fallback is
// announced on stderr so it can never be mistaken for the frozen instruction.
func userQuery(cfg Config) string {
	if cfg.Query != "" {
		return cfg.Query
	}
	fmt.Fprintf(os.Stderr, "eino-runner: BENCH_QUERY is not set; falling back to this runner's built-in %s prompt, "+
		"so the prompt-bytes column for this run is NOT comparable\n", cfg.Task)
	return queryFor(cfg.Task)
}

// queryFor supplies the fallback user turn when BENCH_QUERY is absent.
func queryFor(task string) string {
	switch task {
	case TaskEditFile:
		return "Read input.txt and write its contents to out.txt."
	case TaskApproveCommand:
		return "Run the shell command this task requires and report its output."
	case TaskDurableTask:
		return "Record each step of this task as you go, then run the shell command this task requires."
	default:
		return "Complete the task."
	}
}
