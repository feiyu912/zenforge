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
	"github.com/feiyu912/zenforge/commands"
	"github.com/feiyu912/zenforge/compaction"
	"github.com/feiyu912/zenforge/configlayer"
	"github.com/feiyu912/zenforge/eventlog"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/goals"
	"github.com/feiyu912/zenforge/harness"
	"github.com/feiyu912/zenforge/hooks"
	"github.com/feiyu912/zenforge/instructions"
	jobspkg "github.com/feiyu912/zenforge/jobs"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/modelretry"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox/linuxsandbox"
	"github.com/feiyu912/zenforge/schedule"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools/askuser"
	"github.com/feiyu912/zenforge/tools/contextinfo"
	goaltools "github.com/feiyu912/zenforge/tools/goal"
	jobtools "github.com/feiyu912/zenforge/tools/jobs"
	patchtools "github.com/feiyu912/zenforge/tools/patch"
	plantools "github.com/feiyu912/zenforge/tools/plan"
	"github.com/feiyu912/zenforge/tools/present"
	"github.com/feiyu912/zenforge/tools/toolsearch"
	webtools "github.com/feiyu912/zenforge/tools/web"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	"github.com/feiyu912/zenforge/web"
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
	case "exec":
		err = exec(ctx, args[1:], ioStreams)
	case "code":
		err = code(ctx, args[1:], ioStreams)
	case "resume":
		err = resume(ctx, args[1:], ioStreams)
	case "fork":
		err = fork(ctx, args[1:], ioStreams)
	case "revert":
		err = revert(ctx, args[1:], ioStreams)
	case "goal":
		err = goalCommand(ctx, args[1:], ioStreams)
	case "ralph":
		err = ralphCommand(ctx, args[1:], ioStreams)
	case linuxsandbox.HelperCommand:
		// Hidden helper: the landlock+seccomp backend re-invokes this
		// binary to apply the ruleset and filter, then exec the command. A
		// Landlock or seccomp restriction cannot be installed from outside
		// the process that execs, so this indirection is the only correct
		// shape.
		err = linuxSandboxHelper(ctx, args[1:], ioStreams)
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

// exec runs one headless task, printing either the human stream or one
// JSON event per line (codex exec --json), optionally constraining the
// final response to a JSON Schema and writing the last message to a
// file.
func exec(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	jsonOut := fs.Bool("json", false, "print events to stdout as JSONL")
	outputSchemaPath := fs.String("output-schema", "", "path to a JSON Schema file describing the final response")
	lastMessagePath := fs.String("output-last-message", "", "write the last agent message to this file")
	lastMessageShort := fs.String("o", "", "alias for --output-last-message")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	if *lastMessagePath == "" {
		*lastMessagePath = *lastMessageShort
	}
	if *outputSchemaPath != "" {
		schema, err := loadOutputSchema(*outputSchemaPath)
		if err != nil {
			return invalidUsage(err)
		}
		opts.outputSchema = schema
		opts.outputSchemaName = schemaNameFromPath(*outputSchemaPath)
	}
	input, err := execInput(fs.Args(), ioStreams.Stdin)
	if err != nil {
		return err
	}
	catalog, err := buildCatalog(opts)
	if err != nil {
		return err
	}
	if opts.listCommands {
		listing := catalog.List()
		if listing == "" {
			listing = "no commands are defined"
		}
		ioStreams.Stdout.Write([]byte(listing + "\n"))
		return nil
	}
	input, err = resolveCommand(catalog, input, opts)
	if err != nil {
		return invalidUsage(err)
	}
	if strings.TrimSpace(opts.scheduleSpec) != "" {
		spec, err := schedule.Parse(opts.scheduleSpec)
		if err != nil {
			return invalidUsage(err)
		}
		return runSchedule(ctx, opts, spec, input, ioStreams)
	}
	agent, err := buildAgent(ctx, opts, ioStreams)
	if err != nil {
		return err
	}
	events, err := agent.Stream(ctx, zenforge.Task{Input: input})
	if err != nil {
		return err
	}
	render := renderEvent
	if *jsonOut {
		render = renderEventJSON
	}
	finalOutput, streamErr := renderStreamCapturing(ioStreams.Stdout, events, render)
	if streamErr == nil && *lastMessagePath != "" {
		if err := os.WriteFile(*lastMessagePath, []byte(finalOutput), 0o644); err != nil {
			return fmt.Errorf("write last message: %w", err)
		}
	}
	return streamErr
}

// execInput reads the prompt from the arguments, or from stdin when the
// argument is "-" or absent and stdin is not a terminal.
func execInput(args []string, stdin io.Reader) (string, error) {
	input := strings.TrimSpace(strings.Join(args, " "))
	if input != "" && input != "-" {
		return input, nil
	}
	if stdin == nil {
		return "", invalidUsage(errors.New("exec input is required"))
	}
	data, err := io.ReadAll(io.LimitReader(stdin, maxExecInputBytes))
	if err != nil {
		return "", fmt.Errorf("read exec input: %w", err)
	}
	input = strings.TrimSpace(string(data))
	if input == "" {
		return "", invalidUsage(errors.New("exec input is required"))
	}
	return input, nil
}

const maxExecInputBytes = 1 << 20

// loadOutputSchema reads a JSON Schema file, requiring a JSON object.
func loadOutputSchema(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read output schema: %w", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("output schema %s: %w", path, err)
	}
	if len(schema) == 0 {
		return nil, fmt.Errorf("output schema %s: schema must be a non-empty JSON object", path)
	}
	return schema, nil
}

// schemaNameFromPath derives a provider-safe schema label: providers
// require a name, and the file stem is the most useful stable choice.
func schemaNameFromPath(path string) string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	var builder strings.Builder
	for _, r := range stem {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	name := strings.Trim(builder.String(), "_")
	if name == "" {
		return ""
	}
	return name
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
	revertTo := fs.Int64("revert-to", 0, "rewind to the newest checkpoint at or below this event sequence before resuming")
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
	if *revertTo < 0 {
		return invalidUsage(errors.New("--revert-to must be non-negative"))
	}
	if err := validateResumeCheckpoint(ctx, opts, fs.Arg(0)); err != nil {
		return err
	}
	agent, err := buildAgent(ctx, opts, ioStreams)
	if err != nil {
		return err
	}
	if *revertTo > 0 {
		marker, err := agent.Revert(ctx, fs.Arg(0), *revertTo)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(ioStreams.Stderr, "reverted run %s to seq %d (marker seq %d)\n", fs.Arg(0), *revertTo, marker.Seq)
	}
	events, err := agent.Resume(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return renderStream(ioStreams.Stdout, events)
}

// fork starts a new run from an existing run's checkpoint at or below
// --at (default: the latest checkpoint) and streams the continued run.
// The child's conversation, todos, and tool state come from the parent;
// the parent's log is untouched and the child records its lineage.
func fork(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("fork", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	at := fs.Int64("at", 0, "parent checkpoint sequence to branch from (0 selects the latest)")
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
		return invalidUsage(errors.New("fork requires parent run id"))
	}
	if *at < 0 {
		return invalidUsage(errors.New("--at must be non-negative"))
	}
	agent, err := buildAgent(ctx, opts, ioStreams)
	if err != nil {
		return err
	}
	runID, events, err := agent.Fork(ctx, fs.Arg(0), *at)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(ioStreams.Stderr, "forked %s into %s\n", fs.Arg(0), runID)
	return renderStream(ioStreams.Stdout, events)
}

// revert rewinds a run without running it: the next resume continues
// from the rewound state, and the log keeps the abandoned branch behind
// a run.reverted marker.
func revert(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("revert", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	bindOptions(fs, &opts)
	to := fs.Int64("to", 0, "event sequence to rewind to")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if fs.NArg() != 1 {
		return invalidUsage(errors.New("revert requires run id"))
	}
	if *to <= 0 {
		return invalidUsage(errors.New("revert requires --to with a positive sequence"))
	}
	checkpoints, closeCheckpoints, err := openCheckpointStore(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		return err
	}
	defer func() { _ = closeCheckpoints() }()
	events, closeEvents, err := openEventStore(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		return err
	}
	defer func() { _ = closeEvents() }()
	marker, err := zenforge.RevertRun(ctx, zenforge.TimeTravelStores{
		Checkpoints: checkpoints,
		Events:      events,
	}, fs.Arg(0), *to)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(ioStreams.Stdout, "reverted run %s to seq %d (marker seq %d)\n", fs.Arg(0), *to, marker.Seq)
	return nil
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
	outputSchema        map[string]any
	outputSchemaName    string
	outputSchemaStrict  *bool
	apiKey              string
	configSources       []configlayer.Source
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

	planMode      bool
	goalsEnabled  bool
	jobsEnabled   bool
	goalMaxRounds int

	hooksPath     string
	memoryDir     string
	memoryScope   string
	memoryDistill bool
	reviewMode    string
	commandsDir   string
	scheduleSpec  string
	listCommands  bool

	sandboxBackend      string
	sandboxRoots        multiFlag
	sandboxAllowNetwork bool
	sandboxRestricted   bool
	sandboxImage        string
	sandboxTimeout      time.Duration
	sandboxProtected    multiFlag

	webEnabled         bool
	webSearchEndpoint  string
	webSearchAPIKey    string
	webSearchAPIKeyEnv string
	webMaxResults      int
	webMaxQueries      int
	webMaxBodyChars    int
	webAllowPrivate    bool
	webRequireApproval bool

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
	fs.StringVar(&opts.apiKey, "api-key", opts.apiKey, "inline API key; prefer --api-key-env or model.apiKeyEnv")
	_ = fs.String("profile", "", "configuration profile from the `profiles` object")
	_ = fs.String("requirements", "", "managed requirements file: `allowed` sets reject values, `enforce` overwrites them")
	_ = fs.Bool("strict-config", false, "reject config fields this version does not recognize")
	_ = fs.Bool("ignore-user-config", false, "skip the system and user configuration layers")
	fs.BoolVar(&opts.planMode, "plan", opts.planMode, "start in plan mode: mutating tools are refused until exit_plan_mode is approved")
	fs.BoolVar(&opts.goalsEnabled, "goals", opts.goalsEnabled, "register the create_goal/get_goal/update_goal tools")
	fs.BoolVar(&opts.jobsEnabled, "jobs", opts.jobsEnabled, "register the exec_command/write_stdin/job_output/job_list/job_kill tools")
	fs.StringVar(&opts.hooksPath, "hooks", opts.hooksPath, "JSON file of lifecycle hooks (PreToolUse/PostToolUse run around every tool call)")
	fs.StringVar(&opts.memoryDir, "memory", opts.memoryDir, "directory of durable cross-run memories (injected as instructions)")
	fs.StringVar(&opts.memoryScope, "memory-scope", opts.memoryScope, "scope new memories get: user (default) or project")
	fs.BoolVar(&opts.memoryDistill, "memory-distill", opts.memoryDistill, "distil each finished run into new memories with one model call")
	fs.StringVar(&opts.reviewMode, "review", opts.reviewMode, "independent review of each finished run: off, report, or enforce")
	fs.StringVar(&opts.commandsDir, "commands", opts.commandsDir, "directory of command definitions (default <workspace>/"+commands.DefaultDir+")")
	fs.BoolVar(&opts.listCommands, "list-commands", opts.listCommands, "list the available commands and exit")
	fs.StringVar(&opts.scheduleSpec, "schedule", opts.scheduleSpec, "repeat the task on a schedule, e.g. 'every 1h' or '0 3 * * *'")
	fs.IntVar(&opts.goalMaxRounds, "goal-max-rounds", opts.goalMaxRounds, "default round budget for goals created in this session")
	fs.StringVar(&opts.sandboxBackend, "sandbox", opts.sandboxBackend, "confine the shell in a sandbox: none, seatbelt (macOS), bwrap (Linux), or docker")
	fs.Var(&opts.sandboxRoots, "sandbox-root", "writable root inside the sandbox (repeatable; defaults to the working directory)")
	fs.BoolVar(&opts.sandboxAllowNetwork, "sandbox-allow-network", opts.sandboxAllowNetwork, "grant the sandboxed shell network access")
	fs.BoolVar(&opts.sandboxRestricted, "sandbox-restricted", opts.sandboxRestricted, "start the bubblewrap sandbox from an empty root instead of a read-only host root")
	fs.StringVar(&opts.sandboxImage, "sandbox-image", opts.sandboxImage, "container image for the docker backend")
	fs.DurationVar(&opts.sandboxTimeout, "sandbox-timeout", opts.sandboxTimeout, "timeout for one sandboxed command (defaults to shell.timeout)")
	fs.Var(&opts.sandboxProtected, "sandbox-protected", "basename kept read-only inside writable roots (repeatable; defaults to .git and .zenforge)")
	fs.BoolVar(&opts.webEnabled, "web", opts.webEnabled, "enable the web_fetch and web_search tools")
	fs.StringVar(&opts.webSearchEndpoint, "web-search-endpoint", opts.webSearchEndpoint, "JSON search API endpoint for web_search (enables the web tools)")
	fs.StringVar(&opts.webSearchAPIKey, "web-search-api-key", opts.webSearchAPIKey, "inline search API key; prefer --web-search-api-key-env")
	fs.StringVar(&opts.webSearchAPIKeyEnv, "web-search-api-key-env", opts.webSearchAPIKeyEnv, "environment variable containing the search API key")
	fs.IntVar(&opts.webMaxResults, "web-max-results", opts.webMaxResults, "maximum sources returned by web_search (default 8)")
	fs.BoolVar(&opts.webAllowPrivate, "web-allow-private", opts.webAllowPrivate, "allow web_fetch to reach loopback and private addresses (dangerous; local development only)")
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
	config, sources, err := layeredSources(args)
	if err != nil {
		return opts, invalidUsage(err)
	}
	opts.configPath = configPathFromArgs(args)
	opts.configSources = sources
	if err := applyConfig(&opts, config); err != nil {
		return opts, invalidUsage(err)
	}
	return opts, nil
}

func buildAgent(ctx context.Context, opts options, ioStreams IO) (*zenforge.Agent, error) {
	// The hook configuration is validated first: it decides what may run, so
	// a typo in it must fail before anything else is constructed.
	hookEngine, err := buildHookEngine(opts)
	if err != nil {
		return nil, err
	}
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
	snapshots := workspacetools.NewSnapshotStore()
	workspaceTools, err := workspacetools.Tools(workspacetools.Config{
		Workspace:              ws,
		Snapshots:              snapshots,
		RequireReadBeforeWrite: true,
		Policy:                 workspaceFilePolicy(opts),
		SearchSpill:            spillStore,
		TurnDiffs:              turnDiffs,
	})
	if err != nil {
		return nil, err
	}
	patchTool, err := patchtools.New(patchtools.Config{
		Workspace:              ws,
		Snapshots:              snapshots,
		RequireReadBeforeWrite: true,
		FilePolicy:             workspaceFilePolicy(opts),
		TurnDiffs:              turnDiffs,
	})
	if err != nil {
		return nil, err
	}
	tools := append([]tool.Tool(nil), workspaceTools...)
	tools = append(tools, patchTool)
	if opts.webEnabled {
		webTools, err := buildWebTools(opts)
		if err != nil {
			return nil, err
		}
		tools = append(tools, webTools...)
	}
	if !opts.noShell {
		shell, err := buildShellTool(opts)
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
	if opts.goalsEnabled {
		goalTools, err := goaltools.Tools(goaltools.Config{
			Store:     goals.NewFileStore(filepath.Join(opts.checkpointDir, "goals")),
			MaxRounds: opts.goalMaxRounds,
		})
		if err != nil {
			return nil, err
		}
		tools = append(tools, goalTools...)
		for _, registered := range goalTools {
			toolsByName[registered.Name()] = registered
		}
	}
	if opts.jobsEnabled {
		// Long-running commands: the manager owns the processes, and it is
		// closed when the command's context ends so a cancelled or
		// interrupted CLI does not leave a dev server behind.
		manager := jobspkg.New(jobspkg.Config{
			DefaultCWD:     opts.shellWorkingDir,
			DefaultTimeout: opts.shellTimeout,
		})
		if ctx.Done() != nil {
			go func() {
				<-ctx.Done()
				manager.Close()
			}()
		}
		jobTools, err := jobtools.Tools(jobtools.Config{
			Manager:        manager,
			DefaultCWD:     opts.shellWorkingDir,
			MaxOutputBytes: int(opts.shellMaxOutputBytes),
		})
		if err != nil {
			manager.Close()
			return nil, err
		}
		tools = append(tools, jobTools...)
		for _, registered := range jobTools {
			toolsByName[registered.Name()] = registered
		}
	}
	if hookEngine != nil {
		// Hooks observe and may refuse tool calls, so they wrap the whole
		// runtime: a blocking PreToolUse hook returns before the tool runs,
		// and a PostToolUse hook annotates the result.
		toolRuntime = append(toolRuntime, hooks.Middleware(hookEngine, ""))
	}
	// Plan mode refuses mutating tools until exit_plan_mode is approved.
	// The resolver consults each tool's ReadOnlyDeclarer, and an
	// undeclared tool counts as mutating.
	if opts.planMode {
		planTool, err := plantools.New()
		if err != nil {
			return nil, err
		}
		tools = append(tools, planTool)
		toolsByName[planTool.Name()] = planTool
		toolRuntime = append(toolRuntime, tool.PlanMode(func(name string) (tool.Tool, bool) {
			resolved, ok := toolsByName[name]
			return resolved, ok
		}))
	}
	memoryProvider, err := buildMemory(opts, modelAdapter)
	if err != nil {
		return nil, err
	}
	guardian, err := buildGuardian(opts, modelAdapter)
	if err != nil {
		return nil, err
	}
	return zenforge.New(zenforge.Config{
		Model:              modelAdapter,
		Hooks:              hookEngine,
		Memory:             memoryProvider,
		Review:             guardian,
		Instructions:       opts.instructions,
		PersonaPrefix:      opts.personaPrefix,
		PersonaSuffix:      opts.personaSuffix,
		PromptVariables:    opts.promptVariables,
		OutputSchema:       opts.outputSchema,
		OutputSchemaName:   opts.outputSchemaName,
		OutputSchemaStrict: opts.outputSchemaStrict,
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
		PlanMode:           opts.planMode,
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
	if err := validateCheckpointType(opts.checkpointType); err != nil {
		return err
	}
	return validateSandboxBackend(opts.sandboxBackend)
}

func validateCheckpointType(value string) error {
	switch strings.ToLower(value) {
	case "jsonl", "sqlite":
		return nil
	default:
		return fmt.Errorf("unknown checkpoint type: %s", value)
	}
}

// buildWebTools assembles the web_search and web_fetch tools. A search
// endpoint is optional: without one only web_fetch is registered, and
// without web.enabled neither is.
func buildWebTools(opts options) ([]tool.Tool, error) {
	fetchPolicy := web.Policy{
		MaxBodyChars: opts.webMaxBodyChars,
		AllowPrivate: opts.webAllowPrivate,
	}
	fetchTool, err := webtools.Fetch(webtools.FetchConfig{
		Policy:          fetchPolicy,
		MaxBodyChars:    opts.webMaxBodyChars,
		RequireApproval: opts.webRequireApproval,
	})
	if err != nil {
		return nil, err
	}
	built := []tool.Tool{fetchTool}
	if strings.TrimSpace(opts.webSearchEndpoint) != "" {
		searcher := &webtools.HTTPSearcher{
			Endpoint: opts.webSearchEndpoint,
			APIKey:   webSearchKey(opts),
		}
		searchTool, err := webtools.Search(webtools.SearchConfig{
			Searcher:        searcher,
			MaxResults:      opts.webMaxResults,
			MaxQueries:      opts.webMaxQueries,
			RequireApproval: opts.webRequireApproval,
		})
		if err != nil {
			return nil, err
		}
		built = append([]tool.Tool{searchTool}, built...)
	}
	return built, nil
}

// webSearchKey resolves the search API key, preferring the environment
// variable so the secret need not live in the config file.
func webSearchKey(opts options) string {
	if opts.webSearchAPIKeyEnv != "" {
		if value := os.Getenv(opts.webSearchAPIKeyEnv); value != "" {
			return value
		}
	}
	return opts.webSearchAPIKey
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
		APIKey:    opts.apiKey,
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
	_, err := renderStreamCapturing(out, events, renderEvent)
	return err
}

// renderEventJSON prints one event per line as JSON, matching the
// `events --json` record shape.
func renderEventJSON(out io.Writer, event zenforge.Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(out, string(data))
}

// renderStreamCapturing renders every event through render and returns
// the final assistant output reported by the terminal run.done event.
func renderStreamCapturing(out io.Writer, events <-chan zenforge.Event, render func(io.Writer, zenforge.Event)) (string, error) {
	var finalErr error
	var approvalRejected bool
	var runCancelled bool
	var finalOutput string
	for event := range events {
		render(out, event)
		switch event.Type {
		case zenforge.EventApprovalResolved, zenforge.EventApprovalExpired:
			if stringValue(event.Payload["action"]) == string(approval.DecisionReject) {
				approvalRejected = true
			}
		case zenforge.EventRunCancelled:
			runCancelled = true
		case zenforge.EventRunDone:
			finalOutput = stringValue(event.Payload["output"])
		case zenforge.EventRunError:
			finalErr = fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
	}
	if runCancelled {
		return finalOutput, fmt.Errorf("%w", errRunCancelled)
	}
	if approvalRejected {
		return finalOutput, fmt.Errorf("%w", errApprovalRejected)
	}
	return finalOutput, finalErr
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
	case zenforge.EventRunReverted:
		_, _ = fmt.Fprintf(out, "run %s reverted to seq %v\n", event.RunID(), event.Value("toSeq"))
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
	_, _ = fmt.Fprintln(out, "usage: zenforge <run|exec|code|resume|fork|revert|goal|ralph|events|runs|init|version> [options]")
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
