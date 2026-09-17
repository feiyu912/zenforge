package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/modelretry"
)

type configFile struct {
	Model      modelConfig      `json:"model"`
	Agent      agentConfig      `json:"agent"`
	Workspace  workspaceConfig  `json:"workspace"`
	Shell      shellConfig      `json:"shell"`
	Approval   approvalConfig   `json:"approval"`
	Checkpoint checkpointConfig `json:"checkpoint"`
}

type modelConfig struct {
	Provider      string           `json:"provider,omitempty"`
	Name          string           `json:"name,omitempty"`
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
	MaxSteps            int                            `json:"maxSteps,omitempty"`
	Mode                string                         `json:"mode,omitempty"`
	Planning            any                            `json:"planning,omitempty"`
	EnvironmentContext  *bool                          `json:"environmentContext,omitempty"`
	ProjectInstructions *projectInstructionsFileConfig `json:"projectInstructions,omitempty"`
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
}

type approvalConfig struct {
	Mode string `json:"mode,omitempty"`
}

type checkpointConfig struct {
	Type string `json:"type,omitempty"`
	Path string `json:"path,omitempty"`
}

func defaultConfigFile() configFile {
	enabled := true
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
	if config.Agent.Instructions != "" {
		opts.instructions = config.Agent.Instructions
	}
	if config.Agent.SessionTitle != "" {
		opts.sessionTitle = config.Agent.SessionTitle
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
