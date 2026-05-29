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
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model/openai"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

const Version = "0.0.0-dev"

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
	input := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if input == "" {
		return fmt.Errorf("run input is required")
	}
	agent, err := buildAgent(opts, ioStreams)
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
	if fs.NArg() != 1 {
		return fmt.Errorf("resume requires run id")
	}
	agent, err := buildAgent(opts, ioStreams)
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
	checkpointDir := fs.String("checkpoint-dir", opts.checkpointDir, "event/checkpoint directory")
	jsonOut := fs.Bool("json", false, "print JSON events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = configPath
	if fs.NArg() != 1 {
		return fmt.Errorf("events requires run id")
	}
	store := eventlogjsonl.New(*checkpointDir)
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
	instructions        string
	model               string
	apiKeyEnv           string
	baseURL             string
	checkpointDir       string
	maxSteps            int
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
		instructions:        "You are a senior Go backend engineer. Be concise, careful, and use tools when helpful.",
		model:               "gpt-4.1",
		apiKeyEnv:           "OPENAI_API_KEY",
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
	fs.StringVar(&opts.instructions, "instructions", opts.instructions, "agent instructions")
	fs.StringVar(&opts.model, "model", opts.model, "OpenAI-compatible model name")
	fs.StringVar(&opts.apiKeyEnv, "api-key-env", opts.apiKeyEnv, "environment variable containing API key")
	fs.StringVar(&opts.baseURL, "base-url", opts.baseURL, "OpenAI-compatible base URL")
	fs.StringVar(&opts.checkpointDir, "checkpoint-dir", opts.checkpointDir, "event/checkpoint directory")
	fs.IntVar(&opts.maxSteps, "max-steps", opts.maxSteps, "max harness steps")
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
	applyConfig(&opts, config)
	return opts, nil
}

func buildAgent(opts options, ioStreams IO) (*zenforge.Agent, error) {
	apiKey := os.Getenv(opts.apiKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("%s is not set", opts.apiKeyEnv)
	}
	ws, err := workspacelocal.New(workspacelocal.Config{
		Root:            opts.workspace,
		MaxReadBytes:    1_000_000,
		MaxWriteBytes:   1_000_000,
		CreateParentDir: true,
	})
	if err != nil {
		return nil, err
	}
	workspaceTools, err := workspacetools.Tools(workspacetools.Config{Workspace: ws})
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
	return zenforge.New(zenforge.Config{
		Model: openai.New(openai.Config{
			APIKey:  apiKey,
			Model:   opts.model,
			BaseURL: opts.baseURL,
		}),
		Instructions: opts.instructions,
		Tools:        tools,
		Approval:     approvalBroker,
		Events:       eventlogjsonl.New(opts.checkpointDir),
		Checkpoints:  checkpointjsonl.New(opts.checkpointDir),
		MaxSteps:     opts.maxSteps,
		Planning:     planningMode(opts.planning),
	}), nil
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
	items, ok := value.([]any)
	if !ok {
		data, err := json.Marshal(value)
		if err == nil && string(data) != "null" {
			_, _ = fmt.Fprintf(out, "\ntodos %s\n", data)
		}
		return
	}
	_, _ = fmt.Fprintln(out, "\ntodos")
	for _, item := range items {
		fields, _ := item.(map[string]any)
		_, _ = fmt.Fprintf(out, "  [%s] %s\n", stringValue(fields["status"]), stringValue(fields["content"]))
	}
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
	_, _ = fmt.Fprintln(out, "usage: zenforge <run|resume|events|init|version> [options]")
}

type multiFlag []string

func (m *multiFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
