package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
	// Commands declares where command definitions are discovered outside the
	// workspace. It stays out of the generated default file because both
	// layers have working defaults; naming a user directory is opt-in. The
	// workspace directory stays flag-only so a checked-in `.zenforge/commands`
	// need not be restated in configuration.
	Commands *commandsConfig `json:"commands,omitempty"`
	// MCPServers declares the MCP servers this client starts over stdio and
	// exposes as namespaced tools. It is always present in the generated
	// default file (as an empty object) so the key is discoverable.
	MCPServers mcpServersConfig `json:"mcpServers"`
}

// mcpServersConfig maps a server name to the stdio server that serves it.
type mcpServersConfig map[string]mcpServerConfig

// mcpServerConfig declares one MCP server.
type mcpServerConfig struct {
	// Command is the executable to start. Required: a server entry without a
	// command names nothing to run.
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// Env is what the server process is given on top of the scrubbed ambient
	// environment (credential-shaped names are not inherited). Values are
	// secrets by nature, so they redact in every formatting path and stay
	// transparent in JSON.
	Env map[string]redact.String `json:"env,omitempty"`
	// Deferred keeps the server's tools out of the model's initial tool list
	// until a tool_search activates them, which is what a large remote
	// catalog wants.
	Deferred bool `json:"deferred,omitempty"`
	// StartupTimeout bounds this server's initialize + tools/list handshake.
	// Empty uses the default; a server that needs longer than the default to
	// wake up (a cold container, a JVM) says so here instead of slowing every
	// other server down.
	StartupTimeout string `json:"startupTimeout,omitempty"`
	// ToolCallTimeout is this server's declared budget for one tool call.
	// Empty uses the default; a remote tool that legitimately runs for
	// minutes (a build, a crawl) declares it here.
	ToolCallTimeout string `json:"toolCallTimeout,omitempty"`
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
	// GrantsFile is where durable "always allow" grants are kept. Empty keeps
	// a grant inside the run that made it; naming a file lets a standing
	// decision outlive the process, which is what the reference does.
	GrantsFile string `json:"grantsFile,omitempty"`
	// GrantTTL bounds how long a persisted grant stays valid. Empty means it
	// does not expire.
	GrantTTL string `json:"grantTtl,omitempty"`
	// Tenant and Subject are the namespace a persisted grant belongs to, so a
	// grant recorded for one identity is never replayed for another. They may
	// only be set with a grants file; defaults are "cli" and the
	// operating-system user name.
	Tenant  string `json:"tenant,omitempty"`
	Subject string `json:"subject,omitempty"`
}

type checkpointConfig struct {
	Type string `json:"type,omitempty"`
	Path string `json:"path,omitempty"`
}

// commandsConfig declares command discovery outside the workspace.
type commandsConfig struct {
	// UserDir is the per-user command directory available in every
	// workspace. Empty uses <user config dir>/zenforge/commands; a workspace
	// command of the same name still wins.
	UserDir string `json:"userDir,omitempty"`
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
		MCPServers: mcpServersConfig{},
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
	specs, err := mcpServerSpecs(config.MCPServers)
	if err != nil {
		return err
	}
	opts.mcpServers = specs
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
	if config.Commands != nil {
		if userDir := strings.TrimSpace(config.Commands.UserDir); userDir != "" {
			opts.userCommandsDir = userDir
		}
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
	if grantsFile := strings.TrimSpace(config.Approval.GrantsFile); grantsFile != "" {
		opts.approvalGrantsFile = grantsFile
	}
	if config.Approval.GrantTTL != "" {
		ttl, err := time.ParseDuration(config.Approval.GrantTTL)
		if err != nil {
			return fmt.Errorf("parse approval.grantTtl: %w", err)
		}
		if ttl <= 0 {
			return fmt.Errorf("approval.grantTtl must be positive")
		}
		opts.approvalGrantTTL = ttl
	}
	opts.approvalTenant = strings.TrimSpace(config.Approval.Tenant)
	opts.approvalSubject = strings.TrimSpace(config.Approval.Subject)
	if opts.approvalGrantsFile == "" {
		// Persistence is the only reason these keys exist; a value without a
		// file would be a standing grant the operator configured and the
		// client silently did not keep, which is the failure mode this
		// repository refuses.
		if opts.approvalGrantTTL > 0 {
			return fmt.Errorf("approval.grantTtl requires approval.grantsFile")
		}
		if opts.approvalTenant != "" || opts.approvalSubject != "" {
			return fmt.Errorf("approval.tenant and approval.subject require approval.grantsFile")
		}
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

// mcpServerSpecs validates the MCP server section and turns it into the
// ordered list the CLI starts servers from.
//
// Validation is strict and happens before anything is spawned: a server
// entry that names no command, a server name that cannot be recovered from
// the namespaced tool name, or an environment name that cannot be passed to
// a process is a configuration error, not a server that happens to be
// unavailable. The map is sorted so two runs of the same file start (and
// register) the servers in the same order.
func mcpServerSpecs(config mcpServersConfig) ([]mcpServerSpec, error) {
	if len(config) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(config))
	for name := range config {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]mcpServerSpec, 0, len(names))
	for _, name := range names {
		server := config[name]
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("mcpServers has an entry with an empty server name")
		}
		if name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("mcpServers server name %q must not have surrounding whitespace", name)
		}
		// The model-visible name is `mcp__<server>__<tool>`; a server name
		// containing the separator would make the two halves of that name
		// unrecoverable.
		if strings.Contains(name, "__") {
			return nil, fmt.Errorf("mcpServers server name %q must not contain %q", name, "__")
		}
		command := strings.TrimSpace(server.Command)
		if command == "" {
			return nil, fmt.Errorf("mcpServers.%s.command is required", name)
		}
		env := make([]string, 0, len(server.Env))
		envNames := make([]string, 0, len(server.Env))
		for key := range server.Env {
			envNames = append(envNames, key)
		}
		sort.Strings(envNames)
		for _, key := range envNames {
			if strings.TrimSpace(key) == "" || strings.Contains(key, "=") {
				return nil, fmt.Errorf("mcpServers.%s.env has an invalid variable name %q", name, key)
			}
			env = append(env, key+"="+server.Env[key].Reveal())
		}
		startupTimeout, err := mcpTimeout("startupTimeout", name, server.StartupTimeout)
		if err != nil {
			return nil, err
		}
		toolCallTimeout, err := mcpTimeout("toolCallTimeout", name, server.ToolCallTimeout)
		if err != nil {
			return nil, err
		}
		specs = append(specs, mcpServerSpec{
			Name:            name,
			Command:         command,
			Args:            append([]string(nil), server.Args...),
			Env:             env,
			Deferred:        server.Deferred,
			StartupTimeout:  startupTimeout,
			ToolCallTimeout: toolCallTimeout,
		})
	}
	return specs, nil
}

// mcpTimeout parses one per-server time bound. An empty value keeps the
// default (the caller's spec field stays zero), and a value that cannot be a
// duration or is not positive is a configuration error: a bound the operator
// wrote but the client silently ignored would be worse than no bound at all.
func mcpTimeout(key, server, value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse mcpServers.%s.%s: %w", server, key, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("mcpServers.%s.%s must be positive", server, key)
	}
	return timeout, nil
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
