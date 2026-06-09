package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
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
	Provider  string `json:"provider,omitempty"`
	Name      string `json:"name,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	BaseURL   string `json:"baseUrl,omitempty"`
}

type agentConfig struct {
	Instructions string `json:"instructions,omitempty"`
	MaxSteps     int    `json:"maxSteps,omitempty"`
	Planning     any    `json:"planning,omitempty"`
}

type workspaceConfig struct {
	Root          string `json:"root,omitempty"`
	MaxReadBytes  int64  `json:"maxReadBytes,omitempty"`
	MaxWriteBytes int64  `json:"maxWriteBytes,omitempty"`
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
	return configFile{
		Model: modelConfig{
			Provider:  defaults.provider,
			Name:      defaults.model,
			APIKeyEnv: defaults.apiKeyEnv,
		},
		Agent: agentConfig{
			Instructions: defaults.instructions,
			MaxSteps:     defaults.maxSteps,
			Planning:     defaults.planning,
		},
		Workspace: workspaceConfig{
			Root:          defaults.workspace,
			MaxReadBytes:  1_000_000,
			MaxWriteBytes: 1_000_000,
		},
		Shell: shellConfig{
			Enabled:        &enabled,
			WorkingDir:     defaults.workspace,
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
	if config.Agent.Instructions != "" {
		opts.instructions = config.Agent.Instructions
	}
	if config.Agent.MaxSteps > 0 {
		opts.maxSteps = config.Agent.MaxSteps
	}
	if planning, ok, err := planningString(config.Agent.Planning); err != nil {
		return err
	} else if ok {
		opts.planning = planning
	}
	if config.Workspace.Root != "" {
		opts.workspace = config.Workspace.Root
	}
	if config.Workspace.MaxReadBytes > 0 {
		opts.workspaceMaxRead = config.Workspace.MaxReadBytes
	}
	if config.Workspace.MaxWriteBytes > 0 {
		opts.workspaceMaxWrite = config.Workspace.MaxWriteBytes
	}
	if config.Shell.WorkingDir != "" {
		opts.workspace = config.Shell.WorkingDir
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
		opts.checkpointType = config.Checkpoint.Type
	}
	if config.Checkpoint.Path != "" {
		opts.checkpointDir = config.Checkpoint.Path
	}
	return nil
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
