package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalcli "github.com/feiyu912/zenforge/approval/cli"
	"github.com/feiyu912/zenforge/checkpoint"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
	"github.com/feiyu912/zenforge/compaction"
	"github.com/feiyu912/zenforge/eventlog"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/instructions"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/modelretry"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools/askuser"
	"github.com/feiyu912/zenforge/tools/contextinfo"
	"github.com/feiyu912/zenforge/tools/present"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	"github.com/feiyu912/zenforge/tools/toolsearch"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

const Version = "0.1.0"

const (
	exitRuntimeError      = 1
	exitInvalidUsage      = 2
	exitRunCancelled      = 3
	exitApprovalRejected  = 4
	exitUnsupportedResume = 5
)

var (
	errInvalidUsage      = errors.New("invalid config or usage")
	errRunCancelled      = errors.New("run cancelled")
	errApprovalRejected  = errors.New("approval rejected")
	errUnsupportedResume = errors.New("unsupported resume state")
)

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func Main(ctx context.Context, args []string, ioStreams IO) int {
	if ioStreams.Stdout == nil {
		ioStreams.Stdout = os.Stdout
	}
	if ioStreams.Stdin == nil {
		ioStreams.Stdin = os.Stdin
	}
	if ioStreams.Stderr == nil {
		ioStreams.Stderr = os.Stderr
	}
	if len(args) == 0 {
		printUsage(ioStreams.Stderr)
		return exitInvalidUsage
	}
	var err error
	switch args[0] {
	case "run":
		err = run(ctx, args[1:], ioStreams)
	case "code":
		err = code(ctx, args[1:], ioStreams)
	case "resume":
		err = resume(ctx, args[1:], ioStreams)
	case "events":
		err = events(ctx, args[1:], ioStreams)
	case "runs":
		err = runs(ctx, args[1:], ioStreams)
	case "init":
		err = initConfig(args[1:], ioStreams)
	case "version":
		_, err = fmt.Fprintln(ioStreams.Stdout, Version)
	default:
		printUsage(ioStreams.Stderr)
		return exitInvalidUsage
	}
	if err != nil {
		_, _ = fmt.Fprintln(ioStreams.Stderr, "error:", err)
		return exitCode(err)
	}
	return 0
}

func run(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		return invalidUsage(errors.New("run input is required"))
	}
	return streamTask(ctx, opts, input, ioStreams)
}

func code(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("code", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	if fs.NArg() == 0 {
		return invalidUsage(errors.New("code repository path is required"))
	}
	if fs.NArg() == 1 {
		return invalidUsage(errors.New("code input is required"))
	}
	repository, err := resolveRepository(fs.Arg(0))
	if err != nil {
		return invalidUsage(err)
	}
	input := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
	if input == "" {
		return invalidUsage(errors.New("code input is required"))
	}
	opts.workspace = repository
	opts.shellWorkingDir = repository
	return streamTask(ctx, opts, input, ioStreams)
}

func resolveRepository(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("code repository path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %q: %w", path, err)
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %q: %w", path, err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("inspect repository path %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %q is not a directory", path)
	}
	return filepath.Clean(realPath), nil
}

func streamTask(ctx context.Context, opts options, input string, ioStreams IO) error {
	agent, err := buildAgent(ctx, opts, ioStreams)
	if err != nil {
		return err
	}
	events, err := agent.Stream(ctx, zenforge.Task{Input: input})
	if err != nil {
		return err
	}
	return renderStream(ioStreams.Stdout, events)
}

func resume(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	if fs.NArg() != 1 {
		return invalidUsage(errors.New("resume requires run id"))
	}
	if err := validateResumeCheckpoint(ctx, opts, fs.Arg(0)); err != nil {
		return err
	}
	agent, err := buildAgent(ctx, opts, ioStreams)
	if err != nil {
		return err
	}
	events, err := agent.Resume(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return renderStream(ioStreams.Stdout, events)
}

func events(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	configPath := fs.String("config", opts.configPath, "config file path")
	checkpointType := fs.String("checkpoint-type", opts.checkpointType, "event/checkpoint store type: jsonl|sqlite")
	checkpointDir := fs.String("checkpoint-dir", opts.checkpointDir, "event/checkpoint directory")
	jsonOut := fs.Bool("json", false, "print JSON events")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := validateCheckpointType(*checkpointType); err != nil {
		return invalidUsage(err)
	}
	_ = configPath
	if fs.NArg() != 1 {
		return invalidUsage(errors.New("events requires run id"))
	}
	store, closeStore, err := openEventStore(ctx, *checkpointType, *checkpointDir)
	if err != nil {
		return err
	}
	defer closeStore()
	events, err := store.Read(ctx, fs.Arg(0), 0, 0)
	if err != nil {
		return err
	}
	for _, event := range events {
		if *jsonOut {
			data, err := json.Marshal(event)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(ioStreams.Stdout, string(data))
			continue
		}
		renderEvent(ioStreams.Stdout, event)
	}
	return nil
}

func runs(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	configPath := fs.String("config", opts.configPath, "config file path")
	checkpointType := fs.String("checkpoint-type", opts.checkpointType, "event/checkpoint store type: jsonl|sqlite")
	checkpointDir := fs.String("checkpoint-dir", opts.checkpointDir, "event/checkpoint directory")
	jsonOut := fs.Bool("json", false, "print JSON summaries")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := validateCheckpointType(*checkpointType); err != nil {
		return invalidUsage(err)
	}
	_ = configPath
	if fs.NArg() != 0 {
		return invalidUsage(errors.New("runs does not accept positional arguments"))
	}
	summaries, closeStore, err := listRuns(ctx, *checkpointType, *checkpointDir)
	if err != nil {
		return err
	}
	defer closeStore()
	if *jsonOut {
		data, err := json.Marshal(summaries)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(ioStreams.Stdout, string(data))
		return nil
	}
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(ioStreams.Stdout, "no runs found")
		return nil
	}
	_, _ = fmt.Fprintln(ioStreams.Stdout, "RUN ID\tPHASE\tSTATUS\tSTEP\tSAVED")
	for _, summary := range summaries {
		_, _ = fmt.Fprintf(ioStreams.Stdout, "%s\t%s\t%s\t%d\t%s\n",
			summary.RunID,
			summary.Phase,
			summary.Status,
			summary.Step,
			summary.SavedAt.Format(time.RFC3339),
		)
	}
	return nil
}

func initConfig(args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	configPath := fs.String("config", "zenforge.json", "config file path")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if fs.NArg() != 0 {
		return invalidUsage(errors.New("init does not accept positional arguments"))
	}
	if err := os.MkdirAll(".zenforge/runs", 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(*configPath); err == nil {
		return fmt.Errorf("%s already exists", *configPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(defaultConfigFile(), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(*configPath), 0o755); err != nil && filepath.Dir(*configPath) != "." {
		return err
	}
	if err := os.WriteFile(*configPath, data, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(ioStreams.Stdout, "created %s\n", *configPath)
	_, _ = fmt.Fprintln(ioStreams.Stdout, "created .zenforge/runs")
	return nil
}

type options struct {
	configPath          string
	workspace           string
	workspaceMaxRead    int64
	workspaceMaxWrite   int64
	workspaceReadRoots  multiFlag
	workspaceWriteRoots multiFlag
	instructions        string
	sessionTitle        string
	personaPrefix       string
	personaSuffix       string
	promptVariables     map[string]string
	provider            string
	model               string
	apiKeyEnv           string
	baseURL             string
	contextWindow       int
	retryEnabled        bool
	retryMaxRetries     int
	retryInitialDelay   time.Duration
	retryMaxDelay       time.Duration
	retryJitter         float64
	streamIdleTimeout   time.Duration
	checkpointType      string
	checkpointDir       string
	maxSteps            int
	mode                string
	planning            string
	noShell             bool
	approve             string
	shellTimeout        time.Duration
	shellMaxOutputBytes int64
	shellAllow          multiFlag
	shellWorkingDir     string

	environmentContext      bool
	instructionsEnabled     bool
	instructionsGlobalPath  string
	instructionsMaxBytes    int
	instructionsFileNames   []string
	instructionsRootMarkers []string
}

func defaultOptions() options {
	return options{
		workspace:           ".",
		workspaceMaxRead:    1_000_000,
		workspaceMaxWrite:   1_000_000,
		workspaceReadRoots:  nil,
		workspaceWriteRoots: nil,
		instructions:        "You are a senior Go backend engineer. Be concise, careful, and use tools when helpful.",
		provider:            "openai",
		model:               "gpt-4.1",
		apiKeyEnv:           "OPENAI_API_KEY",
		retryEnabled:        true,
		retryMaxRetries:     modelretry.DefaultMaxRetries,
		retryInitialDelay:   modelretry.DefaultInitialDelay,
		retryMaxDelay:       modelretry.DefaultMaxDelay,
		retryJitter:         modelretry.DefaultJitter,
		streamIdleTimeout:   5 * time.Minute,
		checkpointType:      "jsonl",
		checkpointDir:       ".zenforge/runs",
		maxSteps:            20,
		planning:            "plan_execute",
		approve:             "prompt",
		shellTimeout:        30 * time.Second,
		shellMaxOutputBytes: 256_000,
		shellAllow:          multiFlag{"go test ./...", "go vet ./...", "grep", "find"},
		shellWorkingDir:     ".",

		environmentContext:  true,
		instructionsEnabled: true,
	}
}

func bindOptions(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.configPath, "config", opts.configPath, "config file path")
	fs.StringVar(&opts.workspace, "workspace", opts.workspace, "workspace root")
	fs.Var(&opts.workspaceReadRoots, "workspace-read-root", "workspace-relative readable root; repeatable")
	fs.Var(&opts.workspaceWriteRoots, "workspace-write-root", "workspace-relative writable root; repeatable")
	fs.StringVar(&opts.instructions, "instructions", opts.instructions, "agent instructions")
	fs.StringVar(&opts.sessionTitle, "title", opts.sessionTitle, "session title stored as log-only run metadata (defaults to the first words of the task)")
	fs.StringVar(&opts.provider, "provider", opts.provider, "model provider: openai|anthropic")
	fs.StringVar(&opts.model, "model", opts.model, "OpenAI-compatible model name")
	fs.StringVar(&opts.apiKeyEnv, "api-key-env", opts.apiKeyEnv, "environment variable containing API key")
	fs.StringVar(&opts.baseURL, "base-url", opts.baseURL, "OpenAI-compatible base URL")
	fs.IntVar(&opts.contextWindow, "context-window", opts.contextWindow, "model context window in tokens; enables pressure compaction when positive")
	fs.StringVar(&opts.checkpointType, "checkpoint-type", opts.checkpointType, "event/checkpoint store type: jsonl|sqlite")
	fs.StringVar(&opts.checkpointDir, "checkpoint-dir", opts.checkpointDir, "event/checkpoint directory")
	fs.IntVar(&opts.maxSteps, "max-steps", opts.maxSteps, "max harness steps")
	fs.StringVar(&opts.mode, "mode", opts.mode, "execution mode: react|oneshot|plan_execute")
	fs.StringVar(&opts.planning, "planning", opts.planning, "planning mode: disabled|enabled|plan_execute")
	fs.StringVar(&opts.approve, "approve", opts.approve, "approval mode: always|never|prompt")
	fs.BoolVar(&opts.noShell, "no-shell", opts.noShell, "disable shell tool")
	fs.Var(&opts.shellAllow, "shell-allow", "allowlisted shell command prefix; repeatable")
}

func optionsFromArgs(args []string) (options, error) {
	opts := defaultOptions()
	configPath := configPathFromArgs(args)
	if configPath == "" {
		return opts, nil
	}
	config, err := loadConfigFile(configPath)
	if err != nil {
		return opts, invalidUsage(err)
	}
	opts.configPath = configPath
	if err := applyConfig(&opts, config); err != nil {
		return opts, invalidUsage(err)
	}
	return opts, nil
}

func buildAgent(ctx context.Context, opts options, ioStreams IO) (*zenforge.Agent, error) {
	var executionMode zenforge.AgentMode
	if strings.TrimSpace(opts.mode) != "" {
		mode, err := parseAgentMode(opts.mode)
		if err != nil {
			return nil, err
		}
		executionMode = mode
	}
	ws, err := workspacelocal.New(workspacelocal.Config{
		Root:            opts.workspace,
		MaxReadBytes:    opts.workspaceMaxRead,
		MaxWriteBytes:   opts.workspaceMaxWrite,
		CreateParentDir: true,
	})
	if err != nil {
		return nil, err
	}
	// One private spill store under the workspace serves both the
	// tool-result spill middleware and the search tools' over-cap lists,
	// and stays inside the configured read root so the model can read
	// spilled files back.
	spillStore := tool.NewSpillStore(filepath.Join(opts.workspace, ".zenforge", "spill"))
	// The turn-diff store is shared between the workspace tools (which
	// capture mutations) and the agent (which drains and emits
	// turn.diff events at turn boundaries).
	turnDiffs := workspacetools.NewTurnDiffStore()
	workspaceTools, err := workspacetools.Tools(workspacetools.Config{
		Workspace:              ws,
		Snapshots:              workspacetools.NewSnapshotStore(),
		RequireReadBeforeWrite: true,
		Policy:                 workspaceFilePolicy(opts),
		SearchSpill:            spillStore,
		TurnDiffs:              turnDiffs,
	})
	if err != nil {
		return nil, err
	}
	tools := append([]tool.Tool(nil), workspaceTools...)
	if !opts.noShell {
		shell, err := shelltool.New(shelltool.Config{Policy: policy.ShellPolicy{
			WorkingDir:      opts.shellWorkingDir,
			AllowCommands:   []string(opts.shellAllow),
			RequireApproval: opts.approve != "never",
			MaxTimeout:      opts.shellTimeout,
			MaxOutputBytes:  opts.shellMaxOutputBytes,
		}})
		if err != nil {
			return nil, err
		}
		tools = append(tools, shell)
	}
	// ask_user rides the approval channel: the interactive CLI broker
	// renders questions and collects answers; other brokers may approve
	// without answers (the tool reports the dismissal) or deny.
	askTool, err := askuser.New(askuser.Config{})
	if err != nil {
		return nil, err
	}
	tools = append(tools, askTool)
	// get_context_remaining reports the live token budget the agent
	// injects into tool-call metadata; it answers null until a context
	// window is configured.
	contextTool, err := contextinfo.New()
	if err != nil {
		return nil, err
	}
	tools = append(tools, contextTool)
	// present declares final deliverables; the agent records validated
	// files as deliverables.presented events.
	presentTool, err := present.New(present.Config{Workspace: ws})
	if err != nil {
		return nil, err
	}
	tools = append(tools, presentTool)
	// Deferred tools (for example MCP catalogs fetched through
	// ToolsDeferred) only become callable after a tool_search, so the
	// search tool is registered exactly when something is deferred. The
	// source snapshot is built before the search tool is appended, so a
	// search can never return tool_search itself.
	if hasDeferredTools(tools) {
		source, err := tool.NewRegistry(tools...)
		if err != nil {
			return nil, err
		}
		searchTool, err := toolsearch.New(toolsearch.Config{Source: source})
		if err != nil {
			return nil, err
		}
		tools = append(tools, searchTool)
	}
	approvalBroker, err := approvalBroker(opts, ioStreams)
	if err != nil {
		return nil, err
	}
	events, closeEvents, err := openEventStore(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		return nil, err
	}
	checkpoints, _, err := openCheckpointStore(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		_ = closeEvents()
		return nil, err
	}
	modelAdapter, err := buildModel(opts)
	if err != nil {
		_ = closeEvents()
		return nil, err
	}
	// Context management follows the reference harnesses: retry with
	// exponential backoff and an idle watchdog for transport failures, and
	// compaction (prune + summarize) for context pressure and overflow.
	// The summarizer reuses the configured provider; contextWindow 0 keeps
	// overflow-triggered recovery without proactive pressure compaction.
	var retryConfig *modelretry.Config
	if opts.retryEnabled && opts.retryMaxRetries > 0 {
		retryConfig = &modelretry.Config{
			MaxRetries:   opts.retryMaxRetries,
			InitialDelay: opts.retryInitialDelay,
			MaxDelay:     opts.retryMaxDelay,
			Jitter:       opts.retryJitter,
		}
	}
	compactionConfig := &compaction.Config{
		Policy:     compaction.Policy{ContextWindow: opts.contextWindow},
		Summarizer: compaction.ModelSummarizer{Model: modelAdapter, Name: opts.model},
	}
	// Hierarchical project instructions (AGENTS.md-compatible) and the
	// environment-context snapshot follow the reference harnesses: both are
	// discovered once per run and frozen into durable run state so resume
	// replays the exact prompting.
	var instructionFiles *instructions.Config
	if opts.instructionsEnabled {
		instructionFiles = &instructions.Config{
			GlobalPath:  opts.instructionsGlobalPath,
			MaxBytes:    opts.instructionsMaxBytes,
			FileNames:   opts.instructionsFileNames,
			RootMarkers: opts.instructionsRootMarkers,
		}
		if instructionFiles.GlobalPath == "" {
			if home, err := os.UserHomeDir(); err == nil {
				instructionFiles.GlobalPath = filepath.Join(home, ".zenforge", "AGENTS.md")
			}
		}
	}
	// Tool-runtime guardrails follow the reference harnesses: recover
	// panics, surface repeated identical calls to the model, and spill
	// oversized results to the shared private store.
	toolsByName := make(map[string]tool.Tool, len(tools))
	for _, registered := range tools {
		toolsByName[registered.Name()] = registered
	}
	// The timeout policy arms each tool's declared cooperative budget
	// (see tool.TimeoutDeclarer); tools that declare none run unbounded,
	// matching the DSH default of no deadline.
	toolRuntime := []tool.Middleware{
		tool.RecoverPanic(),
		tool.RepeatGuard(),
		tool.Spill(tool.SpillConfig{Store: spillStore}),
		tool.TimeoutPolicy(func(name string) (tool.Tool, bool) {
			resolved, ok := toolsByName[name]
			return resolved, ok
		}, 0),
	}
	return zenforge.New(zenforge.Config{
		Model:              modelAdapter,
		Instructions:       opts.instructions,
		PersonaPrefix:      opts.personaPrefix,
		PersonaSuffix:      opts.personaSuffix,
		PromptVariables:    opts.promptVariables,
		Tools:              tools,
		ToolRuntime:        toolRuntime,
		Approval:           approvalBroker,
		Events:             events,
		Checkpoints:        checkpoints,
		Compaction:         compactionConfig,
		Retry:              retryConfig,
		StreamIdleTimeout:  opts.streamIdleTimeout,
		InstructionFiles:   instructionFiles,
		SessionTitle:       opts.sessionTitle,
		WorkingDir:         opts.workspace,
		EnvironmentContext: opts.environmentContext,
		TurnDiffs:          turnDiffs,
		MaxSteps:           opts.maxSteps,
		Mode:               executionMode,
		Planning:           planningMode(opts.planning),
	}), nil
}

func resolveExecutionFlags(fs *flag.FlagSet, opts *options) error {
	var modeSet, planningSet bool
	fs.Visit(func(current *flag.Flag) {
		switch current.Name {
		case "mode":
			modeSet = true
		case "planning":
			planningSet = true
		}
	})
	if modeSet && planningSet {
		return fmt.Errorf("--mode and --planning cannot be used together")
	}
	if planningSet {
		opts.mode = ""
	}
	if strings.TrimSpace(opts.mode) != "" {
		if _, err := parseAgentMode(opts.mode); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionEnums(opts options) error {
	switch strings.ToLower(opts.provider) {
	case "openai", "anthropic":
	default:
		return fmt.Errorf("unknown model provider: %s", opts.provider)
	}
	switch opts.approve {
	case "prompt", "always", "never":
	default:
		return fmt.Errorf("unknown approval mode: %s", opts.approve)
	}
	return validateCheckpointType(opts.checkpointType)
}

func validateCheckpointType(value string) error {
	switch strings.ToLower(value) {
	case "jsonl", "sqlite":
		return nil
	default:
		return fmt.Errorf("unknown checkpoint type: %s", value)
	}
}

func workspaceFilePolicy(opts options) policy.FilePolicy {
	readRoots := []string(opts.workspaceReadRoots)
	writeRoots := []string(opts.workspaceWriteRoots)
	return policy.FilePolicy{
		ReadRoots:       readRoots,
		WriteRoots:      writeRoots,
		RequireApproval: opts.approve != "never" && (len(readRoots) > 0 || len(writeRoots) > 0),
	}
}

func buildModel(opts options) (model.Model, error) {
	return provider.FromEnv(provider.Config{
		Protocol:  opts.provider,
		Model:     opts.model,
		BaseURL:   opts.baseURL,
		APIKeyEnv: opts.apiKeyEnv,
	})
}

type runSummary struct {
	RunID   string    `json:"runId"`
	Seq     int64     `json:"seq"`
	Phase   string    `json:"phase"`
	Status  string    `json:"status"`
	Step    int       `json:"step"`
	SavedAt time.Time `json:"savedAt"`
}

func openEventStore(ctx context.Context, storeType, path string) (eventlog.Store, func() error, error) {
	switch strings.ToLower(storeType) {
	case "", "jsonl":
		return eventlogjsonl.New(path), func() error { return nil }, nil
	case "sqlite":
		store, err := eventlogsqlite.Open(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return store, store.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown checkpoint type: %s", storeType)
	}
}

func openCheckpointStore(ctx context.Context, storeType, path string) (checkpoint.Store, func() error, error) {
	switch strings.ToLower(storeType) {
	case "", "jsonl":
		return checkpointjsonl.New(path), func() error { return nil }, nil
	case "sqlite":
		store, err := checkpointsqlite.Open(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return store, store.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown checkpoint type: %s", storeType)
	}
}

func listRuns(ctx context.Context, storeType, path string) ([]runSummary, func() error, error) {
	switch strings.ToLower(storeType) {
	case "", "jsonl":
		summaries, err := checkpointjsonl.New(path).List(ctx)
		return mapSummaries(summaries, func(in checkpointjsonl.Summary) runSummary {
			return runSummary(in)
		}), func() error { return nil }, err
	case "sqlite":
		store, err := checkpointsqlite.Open(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		summaries, err := store.List(ctx)
		return mapSummaries(summaries, func(in checkpointsqlite.Summary) runSummary {
			return runSummary(in)
		}), store.Close, err
	default:
		return nil, nil, fmt.Errorf("unknown checkpoint type: %s", storeType)
	}
}

func mapSummaries[T any](in []T, convert func(T) runSummary) []runSummary {
	out := make([]runSummary, 0, len(in))
	for _, item := range in {
		out = append(out, convert(item))
	}
	return out
}

func approvalBroker(opts options, ioStreams IO) (approval.Broker, error) {
	switch opts.approve {
	case "", "prompt":
		return approvalcli.New(ioStreams.Stdin, ioStreams.Stderr), nil
	case "always":
		return approval.AlwaysAllow(), nil
	case "never":
		return approval.AlwaysDeny("approval disabled"), nil
	default:
		return nil, fmt.Errorf("unknown approval mode: %s", opts.approve)
	}
}

func planningMode(value string) zenforge.PlanningMode {
	switch value {
	case "enabled", "true":
		return zenforge.PlanningEnabled
	case "plan_execute", "plan-execute", "":
		return zenforge.PlanningPlanExecute
	default:
		return zenforge.PlanningDisabled
	}
}

func renderStream(out io.Writer, events <-chan zenforge.Event) error {
	var finalErr error
	var approvalRejected bool
	var runCancelled bool
	for event := range events {
		renderEvent(out, event)
		switch event.Type {
		case zenforge.EventApprovalResolved, zenforge.EventApprovalExpired:
			if stringValue(event.Payload["action"]) == string(approval.DecisionReject) {
				approvalRejected = true
			}
		case zenforge.EventRunCancelled:
			runCancelled = true
		case zenforge.EventRunError:
			finalErr = fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
	}
	if runCancelled {
		return fmt.Errorf("%w", errRunCancelled)
	}
	if approvalRejected {
		return fmt.Errorf("%w", errApprovalRejected)
	}
	return finalErr
}

func exitCode(err error) int {
	switch {
	case errors.Is(err, errUnsupportedResume):
		return exitUnsupportedResume
	case errors.Is(err, errApprovalRejected):
		return exitApprovalRejected
	case errors.Is(err, errRunCancelled), errors.Is(err, context.Canceled):
		return exitRunCancelled
	case errors.Is(err, errInvalidUsage):
		return exitInvalidUsage
	default:
		return exitRuntimeError
	}
}

func invalidUsage(err error) error {
	return fmt.Errorf("%w: %w", errInvalidUsage, err)
}

func validateResumeCheckpoint(ctx context.Context, opts options, runID string) error {
	store, closeStore, err := openCheckpointStore(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		return err
	}
	defer closeStore()
	cp, err := store.Load(ctx, runID)
	if err != nil {
		// Checkpoint stores currently return validation errors without a typed
		// schema sentinel, so classify only their exact unsupported-version error.
		if strings.Contains(err.Error(), "unsupported checkpoint version") {
			return fmt.Errorf("%w: %v", errUnsupportedResume, err)
		}
		return err
	}
	if cp.State.Version != "" && cp.State.Version != harness.RunStateVersion {
		return fmt.Errorf("%w: unsupported run state version %q", errUnsupportedResume, cp.State.Version)
	}
	switch cp.State.Phase {
	case harness.RunPhaseCreated, harness.RunPhaseModel, harness.RunPhaseTool,
		harness.RunPhaseApproval, harness.RunPhaseSubtask, harness.RunPhaseFinalizing,
		harness.RunPhaseCompleted, harness.RunPhaseFailed, harness.RunPhaseCancelled:
	default:
		return fmt.Errorf("%w: unsupported run phase %q", errUnsupportedResume, cp.State.Phase)
	}
	return nil
}

func renderEvent(out io.Writer, event zenforge.Event) {
	switch event.Type {
	case zenforge.EventRunStarted:
		_, _ = fmt.Fprintf(out, "run %s started\n", event.RunID())
	case zenforge.EventRunResumed:
		_, _ = fmt.Fprintf(out, "run %s resumed\n", event.RunID())
	case zenforge.EventModelDelta:
		_, _ = fmt.Fprint(out, stringValue(event.Payload["textDelta"]))
	case zenforge.EventToolCall:
		_, _ = fmt.Fprintf(out, "\ntool %s %s\n", stringValue(event.Payload["toolName"]), jsonValue(event.Payload["arguments"]))
	case zenforge.EventTodoUpdated:
		renderTodos(out, event.Payload["todos"])
	case zenforge.EventApprovalRequested:
		_, _ = fmt.Fprintf(out, "\napproval required: %s (%s)\n", stringValue(event.Payload["operation"]), stringValue(event.Payload["risk"]))
		if request, ok := mapValue(event.Payload["request"]); ok {
			if title := stringValue(request["title"]); title != "" {
				_, _ = fmt.Fprintf(out, "%s\n", title)
			}
			if description := stringValue(request["description"]); description != "" {
				_, _ = fmt.Fprintf(out, "%s\n", description)
			}
		}
	case zenforge.EventRunDone:
		if output := stringValue(event.Payload["output"]); output != "" {
			_, _ = fmt.Fprintf(out, "\n%s\n", output)
		}
		_, _ = fmt.Fprintf(out, "run %s done\n", event.RunID())
	case zenforge.EventRunError:
		_, _ = fmt.Fprintf(out, "\nrun %s error: %s\n", event.RunID(), stringValue(event.Payload["error"]))
	default:
		_, _ = fmt.Fprintf(out, "%d %s\n", event.Seq, event.Type)
	}
}

func renderTodos(out io.Writer, value any) {
	items, ok := todoItems(value)
	if !ok {
		data, err := json.Marshal(value)
		if err == nil && string(data) != "null" {
			_, _ = fmt.Fprintf(out, "\ntodos %s\n", data)
		}
		return
	}
	_, _ = fmt.Fprintln(out, "\ntodos")
	for _, item := range items {
		fields := item
		_, _ = fmt.Fprintf(out, "  [%s] %s\n", stringValue(fields["status"]), stringValue(fields["content"]))
	}
}

func todoItems(value any) ([]map[string]any, bool) {
	if items, ok := value.([]any); ok {
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			fields, ok := mapValue(item)
			if !ok {
				return nil, false
			}
			out = append(out, fields)
		}
		return out, true
	}
	data, err := json.Marshal(value)
	if err != nil || string(data) == "null" {
		return nil, false
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, true
}

func jsonValue(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func mapValue(value any) (map[string]any, bool) {
	fields, ok := value.(map[string]any)
	if ok {
		return fields, true
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, false
	}
	return fields, true
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func printUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "usage: zenforge <run|code|resume|events|runs|init|version> [options]")
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

// hasDeferredTools reports whether any configured tool defers its
// definition until a tool_search activates it.
func hasDeferredTools(tools []tool.Tool) bool {
	for _, registered := range tools {
		if tool.IsDeferred(registered) {
			return true
		}
	}
	return false
}
