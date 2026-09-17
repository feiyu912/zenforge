package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/configlayer"
	"github.com/feiyu912/zenforge/modelretry"
	"github.com/feiyu912/zenforge/redact"
	webtools "github.com/feiyu912/zenforge/tools/web"
)

type configFile struct {
	Model      modelConfig      `json:"model"`
	Agent      agentConfig      `json:"agent"`
	Workspace  workspaceConfig  `json:"workspace"`
	Shell      shellConfig      `json:"shell"`
	Approval   approvalConfig   `json:"approval"`
	Web        webConfig        `json:"web"`
	Checkpoint checkpointConfig `json:"checkpoint"`
}

type modelConfig struct {
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name,omitempty"`
	// APIKey is an inline secret. Every formatting path redacts it; use
	// apiKeyEnv to keep the secret out of the config file entirely.
	APIKey        redact.String    `json:"apiKey,omitempty"`
	APIKeyEnv     string           `json:"apiKeyEnv,omitempty"`
	BaseURL       string           `json:"baseUrl,omitempty"`
	ContextWindow int              `json:"contextWindow,omitempty"`
	Retry         *retryFileConfig `json:"retry,omitempty"`
}

// retryFileConfig configures model-call retry and the stream idle
// watchdog. Absent fields keep the CLI defaults (retry enabled, five
// retries, 500ms initial delay doubling to 10s, +/-10% jitter, 5m stream
// idle timeout). maxRetries 0 disables retries; streamIdleTimeout "0s"
// disables the idle watchdog.
type retryFileConfig struct {
	Enabled           *bool    `json:"enabled,omitempty"`
	MaxRetries        *int     `json:"maxRetries,omitempty"`
	InitialDelay      string   `json:"initialDelay,omitempty"`
	MaxDelay          string   `json:"maxDelay,omitempty"`
	Jitter            *float64 `json:"jitter,omitempty"`
	StreamIdleTimeout string   `json:"streamIdleTimeout,omitempty"`
}

type agentConfig struct {
	Instructions        string                         `json:"instructions,omitempty"`
	SessionTitle        string                         `json:"sessionTitle,omitempty"`
	PersonaPrefix       string                         `json:"personaPrefix,omitempty"`
	PersonaSuffix       string                         `json:"personaSuffix,omitempty"`
	PromptVariables     map[string]string              `json:"promptVariables,omitempty"`
	MaxSteps            int                            `json:"maxSteps,omitempty"`
	Mode                string                         `json:"mode,omitempty"`
	Planning            any                            `json:"planning,omitempty"`
	EnvironmentContext  *bool                          `json:"environmentContext,omitempty"`
	ProjectInstructions *projectInstructionsFileConfig `json:"projectInstructions,omitempty"`
	// PlanMode starts the run in the read-only planning phase.
	PlanMode *bool `json:"planMode,omitempty"`
	// Goals registers the create_goal/get_goal/update_goal tools.
	Goals *bool `json:"goals,omitempty"`
	// Jobs registers the long-running command tools (exec_command,
	// write_stdin, job_output, job_list, job_kill).
	Jobs *bool `json:"jobs,omitempty"`
	// GoalMaxRounds is the default round budget for a new goal.
	GoalMaxRounds *int `json:"goalMaxRounds,omitempty"`
}

// projectInstructionsFileConfig controls hierarchical AGENTS.md-compatible
// instruction discovery. When omitted, discovery is enabled with defaults:
// candidates AGENTS.override.md/AGENTS.md/ZENFORGE.md/CLAUDE.md, project
// root marker .git, 32 KiB merged budget, and a user-global file at
// ~/.zenforge/AGENTS.md when it exists.
type projectInstructionsFileConfig struct {
	Enabled     *bool    `json:"enabled,omitempty"`
	GlobalPath  string   `json:"globalPath,omitempty"`
	MaxBytes    int      `json:"maxBytes,omitempty"`
	FileNames   []string `json:"fileNames,omitempty"`
	RootMarkers []string `json:"rootMarkers,omitempty"`
}

type workspaceConfig struct {
	Root          string   `json:"root,omitempty"`
	MaxReadBytes  int64    `json:"maxReadBytes,omitempty"`
	MaxWriteBytes int64    `json:"maxWriteBytes,omitempty"`
	ReadRoots     []string `json:"readRoots,omitempty"`
	WriteRoots    []string `json:"writeRoots,omitempty"`
}

type shellConfig struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	WorkingDir     string   `json:"workingDir,omitempty"`
	Allow          []string `json:"allow,omitempty"`
	Timeout        string   `json:"timeout,omitempty"`
	MaxOutputBytes int64    `json:"maxOutputBytes,omitempty"`
	// Sandbox confines the shell in an OS-level sandbox. When it is absent
	// the shell runs directly on the host.
	Sandbox *sandboxConfig `json:"sandbox,omitempty"`
}

// sandboxConfig configures the sandbox the shell runs inside.
type sandboxConfig struct {
	// Backend is none, seatbelt (macOS), bwrap (Linux), or docker.
	Backend string `json:"backend,omitempty"`
	// Roots are the writable roots inside the sandbox. Empty selects the
	// shell working directory.
	Roots []string `json:"roots,omitempty"`
	// AllowNetwork grants the sandboxed shell network access.
	AllowNetwork bool `json:"allowNetwork,omitempty"`
	// Restricted starts the bubblewrap sandbox from an empty root instead
	// of a read-only host root.
	Restricted bool `json:"restricted,omitempty"`
	// Image is the container image for the docker backend.
	Image string `json:"image,omitempty"`
	// Timeout bounds one sandboxed command; empty uses shell.timeout.
	Timeout string `json:"timeout,omitempty"`
	// ProtectedNames selects the basenames kept read-only inside writable
	// roots. Empty selects .git and .zenforge.
	ProtectedNames []string `json:"protectedNames,omitempty"`
}

// webConfig configures the web_search and web_fetch tools, mirroring the
// reference web provider configuration. Both tools stay unregistered
// until `enabled` is true (or a search endpoint is set), so a deployment
// opts into network access explicitly; the fetch transport additionally
// refuses non-public addresses unless `allowPrivate` is set.
type webConfig struct {
	Enabled         *bool         `json:"enabled,omitempty"`
	SearchEndpoint  string        `json:"searchEndpoint,omitempty"`
	SearchAPIKey    redact.String `json:"searchApiKey,omitempty"`
	SearchAPIKeyEnv string        `json:"searchApiKeyEnv,omitempty"`
	MaxResults      int           `json:"maxResults,omitempty"`
	MaxQueries      int           `json:"maxQueries,omitempty"`
	MaxBodyChars    int           `json:"maxBodyChars,omitempty"`
	AllowPrivate    bool          `json:"allowPrivate,omitempty"`
	RequireApproval bool          `json:"requireApproval,omitempty"`
}

type approvalConfig struct {
	Mode string `json:"mode,omitempty"`
}

type checkpointConfig struct {
	Type string `json:"type,omitempty"`
	Path string `json:"path,omitempty"`
}

// defaultWebMaxBodyChars is the default model-visible page cap, matching
// the reference fetch provider.
const defaultWebMaxBodyChars = 100_000

func defaultConfigFile() configFile {
	enabled := true
	disabled := false
	defaults := defaultOptions()
	retryEnabled := true
	maxRetries := modelretry.DefaultMaxRetries
	jitter := modelretry.DefaultJitter
	instructionsEnabled := true
	return configFile{
		Model: modelConfig{
			Provider:  defaults.provider,
			Name:      defaults.model,
			APIKeyEnv: defaults.apiKeyEnv,
			Retry: &retryFileConfig{
				Enabled:           &retryEnabled,
				MaxRetries:        &maxRetries,
				InitialDelay:      modelretry.DefaultInitialDelay.String(),
				MaxDelay:          modelretry.DefaultMaxDelay.String(),
				Jitter:            &jitter,
				StreamIdleTimeout: (5 * time.Minute).String(),
			},
		},
		Agent: agentConfig{
			Instructions:        defaults.instructions,
			MaxSteps:            defaults.maxSteps,
			Mode:                "plan_execute",
			EnvironmentContext:  &enabled,
			ProjectInstructions: &projectInstructionsFileConfig{Enabled: &instructionsEnabled},
		},
		Workspace: workspaceConfig{
			Root:          defaults.workspace,
			MaxReadBytes:  1_000_000,
			MaxWriteBytes: 1_000_000,
			ReadRoots:     []string(defaults.workspaceReadRoots),
			WriteRoots:    []string(defaults.workspaceWriteRoots),
		},
		Shell: shellConfig{
			Enabled:        &enabled,
			WorkingDir:     defaults.shellWorkingDir,
			Allow:          []string(defaults.shellAllow),
			Timeout:        defaults.shellTimeout.String(),
			MaxOutputBytes: defaults.shellMaxOutputBytes,
		},
		Approval: approvalConfig{
			Mode: defaults.approve,
		},
		Checkpoint: checkpointConfig{
			Type: defaults.checkpointType,
			Path: defaults.checkpointDir,
		},
		Web: webConfig{
			Enabled:      &disabled,
			MaxResults:   webtools.DefaultMaxResults,
			MaxQueries:   webtools.DefaultMaxQueries,
			MaxBodyChars: defaultWebMaxBodyChars,
		},
	}
}

func loadConfigFile(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return configFile{}, err
	}
	var config configFile
	if err := json.Unmarshal(data, &config); err != nil {
		return configFile{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return config, nil
}

func applyConfig(opts *options, config configFile) error {
	if config.Model.Provider != "" {
		switch config.Model.Provider {
		case "openai", "anthropic":
		default:
			return fmt.Errorf("unknown model.provider: %s", config.Model.Provider)
		}
		opts.provider = config.Model.Provider
	}
	if config.Model.Name != "" {
		opts.model = config.Model.Name
	}
	if !config.Model.APIKey.IsZero() {
		opts.apiKey = config.Model.APIKey.Reveal()
	}
	if config.Model.APIKeyEnv != "" {
		opts.apiKeyEnv = config.Model.APIKeyEnv
	}
	if config.Model.BaseURL != "" {
		opts.baseURL = config.Model.BaseURL
	}
	if config.Model.ContextWindow < 0 {
		return fmt.Errorf("model.contextWindow must be non-negative")
	}
	if config.Model.ContextWindow > 0 {
		opts.contextWindow = config.Model.ContextWindow
	}
	if err := applyRetryConfig(opts, config.Model.Retry); err != nil {
		return err
	}
	if err := applyWebConfig(opts, config.Web); err != nil {
		return err
	}
	if config.Agent.Instructions != "" {
		opts.instructions = config.Agent.Instructions
	}
	if config.Agent.SessionTitle != "" {
		opts.sessionTitle = config.Agent.SessionTitle
	}
	if config.Agent.PersonaPrefix != "" {
		opts.personaPrefix = config.Agent.PersonaPrefix
	}
	if config.Agent.PersonaSuffix != "" {
		opts.personaSuffix = config.Agent.PersonaSuffix
	}
	if len(config.Agent.PromptVariables) > 0 {
		opts.promptVariables = config.Agent.PromptVariables
	}
	if config.Agent.MaxSteps < 0 {
		return fmt.Errorf("agent.maxSteps must be non-negative")
	}
	if config.Agent.MaxSteps > 0 {
		opts.maxSteps = config.Agent.MaxSteps
	}
	if config.Agent.Mode != "" {
		mode, err := parseAgentMode(config.Agent.Mode)
		if err != nil {
			return fmt.Errorf("agent.mode: %w", err)
		}
		opts.mode = string(mode)
	}
	if planning, ok, err := planningString(config.Agent.Planning); err != nil {
		return err
	} else if ok {
		if config.Agent.Mode != "" {
			return fmt.Errorf("agent.mode and agent.planning cannot both be set")
		}
		opts.planning = planning
	}
	if config.Agent.PlanMode != nil {
		opts.planMode = *config.Agent.PlanMode
	}
	if config.Agent.Goals != nil {
		opts.goalsEnabled = *config.Agent.Goals
	}
	if config.Agent.Jobs != nil {
		opts.jobsEnabled = *config.Agent.Jobs
	}
	if config.Agent.GoalMaxRounds != nil {
		opts.goalMaxRounds = *config.Agent.GoalMaxRounds
	}
	if config.Agent.EnvironmentContext != nil {
		opts.environmentContext = *config.Agent.EnvironmentContext
	}
	if project := config.Agent.ProjectInstructions; project != nil {
		if project.Enabled != nil {
			opts.instructionsEnabled = *project.Enabled
		}
		if project.GlobalPath != "" {
			opts.instructionsGlobalPath = project.GlobalPath
		}
		if project.MaxBytes < 0 {
			return fmt.Errorf("agent.projectInstructions.maxBytes must be non-negative")
		}
		if project.MaxBytes > 0 {
			opts.instructionsMaxBytes = project.MaxBytes
		}
		if len(project.FileNames) > 0 {
			opts.instructionsFileNames = append([]string(nil), project.FileNames...)
		}
		if len(project.RootMarkers) > 0 {
			opts.instructionsRootMarkers = append([]string(nil), project.RootMarkers...)
		}
	}
	if config.Workspace.Root != "" {
		opts.workspace = config.Workspace.Root
	}
	if config.Workspace.MaxReadBytes < 0 {
		return fmt.Errorf("workspace.maxReadBytes must be non-negative")
	}
	if config.Workspace.MaxReadBytes > 0 {
		opts.workspaceMaxRead = config.Workspace.MaxReadBytes
	}
	if config.Workspace.MaxWriteBytes < 0 {
		return fmt.Errorf("workspace.maxWriteBytes must be non-negative")
	}
	if config.Workspace.MaxWriteBytes > 0 {
		opts.workspaceMaxWrite = config.Workspace.MaxWriteBytes
	}
	if len(config.Workspace.ReadRoots) > 0 {
		opts.workspaceReadRoots = multiFlag(append([]string(nil), config.Workspace.ReadRoots...))
	}
	if len(config.Workspace.WriteRoots) > 0 {
		opts.workspaceWriteRoots = multiFlag(append([]string(nil), config.Workspace.WriteRoots...))
	}
	if config.Shell.WorkingDir != "" {
		opts.shellWorkingDir = config.Shell.WorkingDir
	}
	if config.Shell.Enabled != nil {
		opts.noShell = !*config.Shell.Enabled
	}
	if len(config.Shell.Allow) > 0 {
		opts.shellAllow = multiFlag(append([]string(nil), config.Shell.Allow...))
	}
	if config.Shell.Timeout != "" {
		timeout, err := time.ParseDuration(config.Shell.Timeout)
		if err != nil {
			return fmt.Errorf("parse shell.timeout: %w", err)
		}
		opts.shellTimeout = timeout
	}
	if config.Shell.Sandbox != nil {
		opts.sandboxBackend = config.Shell.Sandbox.Backend
		if len(config.Shell.Sandbox.Roots) > 0 {
			opts.sandboxRoots = multiFlag(append([]string(nil), config.Shell.Sandbox.Roots...))
		}
		opts.sandboxAllowNetwork = config.Shell.Sandbox.AllowNetwork
		opts.sandboxRestricted = config.Shell.Sandbox.Restricted
		opts.sandboxImage = config.Shell.Sandbox.Image
		opts.sandboxProtected = multiFlag(append([]string(nil), config.Shell.Sandbox.ProtectedNames...))
		if config.Shell.Sandbox.Timeout != "" {
			timeout, err := time.ParseDuration(config.Shell.Sandbox.Timeout)
			if err != nil {
				return fmt.Errorf("parse shell.sandbox.timeout: %w", err)
			}
			opts.sandboxTimeout = timeout
		}
	}
	if config.Shell.MaxOutputBytes < 0 {
		return fmt.Errorf("shell.maxOutputBytes must be non-negative")
	}
	if config.Shell.MaxOutputBytes > 0 {
		opts.shellMaxOutputBytes = config.Shell.MaxOutputBytes
	}
	if config.Approval.Mode != "" {
		switch config.Approval.Mode {
		case "prompt", "always", "never":
		default:
			return fmt.Errorf("unknown approval.mode: %s", config.Approval.Mode)
		}
		opts.approve = config.Approval.Mode
	}
	if config.Checkpoint.Type != "" {
		switch config.Checkpoint.Type {
		case "jsonl", "sqlite":
		default:
			return fmt.Errorf("unknown checkpoint.type: %s", config.Checkpoint.Type)
		}
		opts.checkpointType = config.Checkpoint.Type
	}
	if config.Checkpoint.Path != "" {
		opts.checkpointDir = config.Checkpoint.Path
	}
	return nil
}

// applyWebConfig maps the web section onto CLI options.
func applyWebConfig(opts *options, config webConfig) error {
	if config.Enabled != nil {
		opts.webEnabled = *config.Enabled
	}
	if config.SearchEndpoint != "" {
		opts.webEnabled = true
		opts.webSearchEndpoint = config.SearchEndpoint
	}
	if !config.SearchAPIKey.IsZero() {
		opts.webSearchAPIKey = config.SearchAPIKey.Reveal()
	}
	if config.SearchAPIKeyEnv != "" {
		opts.webSearchAPIKeyEnv = config.SearchAPIKeyEnv
	}
	if config.MaxResults < 0 || config.MaxQueries < 0 || config.MaxBodyChars < 0 {
		return fmt.Errorf("web limits must be non-negative")
	}
	if config.MaxResults > 0 {
		opts.webMaxResults = config.MaxResults
	}
	if config.MaxQueries > 0 {
		opts.webMaxQueries = config.MaxQueries
	}
	if config.MaxBodyChars > 0 {
		opts.webMaxBodyChars = config.MaxBodyChars
	}
	opts.webAllowPrivate = config.AllowPrivate
	opts.webRequireApproval = config.RequireApproval
	return nil
}

func applyRetryConfig(opts *options, retry *retryFileConfig) error {
	if retry == nil {
		return nil
	}
	if retry.Enabled != nil {
		opts.retryEnabled = *retry.Enabled
	}
	if retry.MaxRetries != nil {
		if *retry.MaxRetries < 0 {
			return fmt.Errorf("model.retry.maxRetries must be non-negative")
		}
		opts.retryMaxRetries = *retry.MaxRetries
	}
	if retry.InitialDelay != "" {
		delay, err := time.ParseDuration(retry.InitialDelay)
		if err != nil {
			return fmt.Errorf("parse model.retry.initialDelay: %w", err)
		}
		opts.retryInitialDelay = delay
	}
	if retry.MaxDelay != "" {
		delay, err := time.ParseDuration(retry.MaxDelay)
		if err != nil {
			return fmt.Errorf("parse model.retry.maxDelay: %w", err)
		}
		opts.retryMaxDelay = delay
	}
	if retry.Jitter != nil {
		if *retry.Jitter < 0 || *retry.Jitter >= 1 {
			return fmt.Errorf("model.retry.jitter must be in [0, 1)")
		}
		opts.retryJitter = *retry.Jitter
	}
	if retry.StreamIdleTimeout != "" {
		timeout, err := time.ParseDuration(retry.StreamIdleTimeout)
		if err != nil {
			return fmt.Errorf("parse model.retry.streamIdleTimeout: %w", err)
		}
		if timeout < 0 {
			return fmt.Errorf("model.retry.streamIdleTimeout must be non-negative")
		}
		opts.streamIdleTimeout = timeout
	}
	return nil
}

func parseAgentMode(value string) (zenforge.AgentMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "react":
		return zenforge.ModeReact, nil
	case "oneshot", "one-shot":
		return zenforge.ModeOneshot, nil
	case "plan_execute", "plan-execute":
		return zenforge.ModePlanExecute, nil
	default:
		return "", fmt.Errorf("unknown execution mode %q", value)
	}
}

func configPathFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--config" || arg == "-config" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	return ""
}

func planningString(value any) (string, bool, error) {
	switch v := value.(type) {
	case nil:
		return "", false, nil
	case string:
		switch v {
		case "":
			return "", false, nil
		case "disabled", "enabled", "plan_execute", "plan-execute", "true":
			return v, true, nil
		default:
			return "", false, fmt.Errorf("unknown agent.planning mode: %s", v)
		}
	case bool:
		if v {
			return "plan_execute", true, nil
		}
		return "disabled", true, nil
	default:
		return "", false, fmt.Errorf("agent.planning must be a string or boolean")
	}
}

// layeredSources loads the configuration stack and returns the merged
// typed config plus the layer provenance for diagnostics.
//
// Precedence, lowest first: system, user, the selected profile
// (immediately above the layer that defined it), project
// (`.zenforge/zenforge.json` found by walking up from the workspace),
// the explicit `--config` file, and finally command-line flags, which
// are applied by the caller. A managed requirements document is applied
// last: its `allowed` sets reject values and its `enforce` section
// overwrites them.
func layeredSources(args []string) (configFile, []configlayer.Source, error) {
	var sources []configlayer.Source
	stack := configlayer.New()
	if !boolFlagFromArgs(args, "ignore-user-config") {
		if err := stack.AddFile(configlayer.KindSystem, configlayer.SystemConfigPath(), false); err != nil {
			return configFile{}, nil, err
		}
		userPath, err := configlayer.UserConfigPath()
		if err != nil {
			return configFile{}, nil, err
		}
		if err := stack.AddFile(configlayer.KindUser, userPath, false); err != nil {
			return configFile{}, nil, err
		}
	}
	workspace := stringFlagFromArgs(args, "workspace")
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	if err := stack.AddFile(configlayer.KindProject, configlayer.ProjectConfigPath(workspace), false); err != nil {
		return configFile{}, nil, err
	}
	explicit := configPathFromArgs(args)
	if explicit != "" {
		if err := stack.AddFile(configlayer.KindFile, explicit, true); err != nil {
			return configFile{}, nil, err
		}
	}

	merged, provenance, err := stack.Merge()
	if err != nil {
		return configFile{}, nil, err
	}
	if profile := stringFlagFromArgs(args, "profile"); profile != "" {
		if _, ok := configlayer.Get(merged, "profiles"); !ok {
			return configFile{}, nil, fmt.Errorf("profile %q is not defined (no configuration layer declares profiles)", profile)
		}
		overrides, err := configlayer.ExtractProfile(merged, profile)
		if err != nil {
			return configFile{}, nil, err
		}
		defining := provenance["profiles"]
		stack.Add(configlayer.Source{
			Kind:    configlayer.KindProfile,
			Path:    defining.Path,
			Profile: profile,
			Rank:    defining.Precedence() + 1,
		}, overrides)
		merged, provenance, err = stack.Merge()
		if err != nil {
			return configFile{}, nil, err
		}
	}
	merged = configlayer.WithoutProfiles(merged)

	requirements, err := loadRequirements(args)
	if err != nil {
		return configFile{}, nil, err
	}
	merged, err = requirements.Apply(merged)
	if err != nil {
		return configFile{}, nil, err
	}

	if boolFlagFromArgs(args, "strict-config") {
		unknown, err := configlayer.UnknownFields(merged, &configFile{})
		if err != nil {
			return configFile{}, nil, fmt.Errorf("config is invalid: %w", err)
		}
		if len(unknown) > 0 {
			return configFile{}, nil, fmt.Errorf(
				"unknown config field %q (set by %s); remove it or drop --strict-config",
				unknown[0], sourceForField(provenance, unknown[0]).Label(),
			)
		}
	}

	data, err := json.Marshal(merged)
	if err != nil {
		return configFile{}, nil, fmt.Errorf("encode merged config: %w", err)
	}
	var config configFile
	if err := json.Unmarshal(data, &config); err != nil {
		return configFile{}, nil, fmt.Errorf("merged config is invalid: %w", err)
	}
	for _, layer := range stack.Layers() {
		if layer.Disabled != "" {
			continue
		}
		sources = append(sources, layer.Source)
	}
	return config, sources, nil
}

// loadRequirements reads the managed requirements document. An explicit
// --requirements path must exist; the host-wide default path is
// optional.
func loadRequirements(args []string) (*configlayer.Requirements, error) {
	path := stringFlagFromArgs(args, "requirements")
	required := path != ""
	if !required {
		path = configlayer.RequirementsPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return nil, nil
		}
		return nil, fmt.Errorf("read requirements %s: %w", path, err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse requirements %s: %w", path, err)
	}
	requirements, err := configlayer.ParseRequirements(
		fmt.Sprintf("requirements (%s)", path), document,
	)
	if err != nil {
		return nil, err
	}
	return requirements, nil
}

// sourceForField finds the layer that supplied a field, using the
// deepest provenance entry that ends with the reported name.
func sourceForField(provenance map[string]configlayer.Source, field string) configlayer.Source {
	if source, ok := provenance[field]; ok {
		return source
	}
	suffix := "." + field
	best := configlayer.Source{}
	for path, source := range provenance {
		if strings.HasSuffix(path, suffix) {
			if best.Kind == "" || source.Precedence() > best.Precedence() {
				best = source
			}
		}
	}
	if best.Kind == "" {
		return configlayer.Source{}
	}
	return best
}

// stringFlagFromArgs peeks a string flag before flag parsing, because
// configuration must be loaded before flags are bound.
func stringFlagFromArgs(args []string, name string) string {
	for i, arg := range args {
		if arg == "--"+name || arg == "-"+name {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(arg, "--"+name+"=") {
			return strings.TrimPrefix(arg, "--"+name+"=")
		}
	}
	return ""
}

// boolFlagFromArgs peeks a boolean flag before flag parsing.
func boolFlagFromArgs(args []string, name string) bool {
	for _, arg := range args {
		if arg == "--"+name || arg == "-"+name {
			return true
		}
		if strings.HasPrefix(arg, "--"+name+"=") {
			return strings.TrimPrefix(arg, "--"+name+"=") != "false"
		}
	}
	return false
}
