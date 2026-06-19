package cli

import (
	"context"
	"encoding/json"
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
	"github.com/feiyu912/zenforge/eventlog"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/anthropic"
	"github.com/feiyu912/zenforge/model/openai"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

const Version = "0.1.0"

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
		return 2
	}
	var err error
	switch args[0] {
	case "run":
		err = run(ctx, args[1:], ioStreams)
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
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(ioStreams.Stderr, "error:", err)
		return 1
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
		return err
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return err
	}
	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		return fmt.Errorf("run input is required")
	}
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
		return err
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("resume requires run id")
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
		return err
	}
	_ = configPath
	if fs.NArg() != 1 {
		return fmt.Errorf("events requires run id")
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
		return err
	}
	_ = configPath
	if fs.NArg() != 0 {
		return fmt.Errorf("runs does not accept positional arguments")
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
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("init does not accept positional arguments")
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
	provider            string
	model               string
	apiKeyEnv           string
	baseURL             string
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
		checkpointType:      "jsonl",
		checkpointDir:       ".zenforge/runs",
		maxSteps:            20,
		planning:            "plan_execute",
		approve:             "prompt",
		shellTimeout:        30 * time.Second,
		shellMaxOutputBytes: 256_000,
		shellAllow:          multiFlag{"go test ./...", "go vet ./...", "grep", "find"},
	}
}

func bindOptions(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.configPath, "config", opts.configPath, "config file path")
	fs.StringVar(&opts.workspace, "workspace", opts.workspace, "workspace root")
	fs.Var(&opts.workspaceReadRoots, "workspace-read-root", "workspace-relative readable root; repeatable")
	fs.Var(&opts.workspaceWriteRoots, "workspace-write-root", "workspace-relative writable root; repeatable")
	fs.StringVar(&opts.instructions, "instructions", opts.instructions, "agent instructions")
	fs.StringVar(&opts.provider, "provider", opts.provider, "model provider: openai|anthropic")
	fs.StringVar(&opts.model, "model", opts.model, "OpenAI-compatible model name")
	fs.StringVar(&opts.apiKeyEnv, "api-key-env", opts.apiKeyEnv, "environment variable containing API key")
	fs.StringVar(&opts.baseURL, "base-url", opts.baseURL, "OpenAI-compatible base URL")
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
		return opts, err
	}
	opts.configPath = configPath
	if err := applyConfig(&opts, config); err != nil {
		return opts, err
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
	workspaceTools, err := workspacetools.Tools(workspacetools.Config{
		Workspace:              ws,
		Snapshots:              workspacetools.NewSnapshotStore(),
		RequireReadBeforeWrite: true,
		Policy:                 workspaceFilePolicy(opts),
	})
	if err != nil {
		return nil, err
	}
	tools := append([]tool.Tool(nil), workspaceTools...)
	if !opts.noShell {
		shell, err := shelltool.New(shelltool.Config{Policy: policy.ShellPolicy{
			WorkingDir:      opts.workspace,
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
	return zenforge.New(zenforge.Config{
		Model:        modelAdapter,
		Instructions: opts.instructions,
		Tools:        tools,
		Approval:     approvalBroker,
		Events:       events,
		Checkpoints:  checkpoints,
		MaxSteps:     opts.maxSteps,
		Mode:         executionMode,
		Planning:     planningMode(opts.planning),
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
	apiKey := os.Getenv(opts.apiKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("%s is not set", opts.apiKeyEnv)
	}
	switch strings.ToLower(opts.provider) {
	case "", "openai":
		return openai.New(openai.Config{
			APIKey:  apiKey,
			Model:   opts.model,
			BaseURL: opts.baseURL,
		}), nil
	case "anthropic":
		return anthropic.New(anthropic.Config{
			APIKey:  apiKey,
			Model:   opts.model,
			BaseURL: opts.baseURL,
		}), nil
	default:
		return nil, fmt.Errorf("unknown model provider: %s", opts.provider)
	}
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
	for event := range events {
		renderEvent(out, event)
		if event.Type == zenforge.EventRunError {
			finalErr = fmt.Errorf("%s", stringValue(event.Payload["error"]))
		}
	}
	return finalErr
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
	_, _ = fmt.Fprintln(out, "usage: zenforge <run|resume|events|runs|init|version> [options]")
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
