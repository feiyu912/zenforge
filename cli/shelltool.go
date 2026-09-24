package cli

import (
	policy "github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
)

// buildShellTool builds the shell tool from the CLI options. It is used by
// the agent's tool set and by inline shell in a command definition, so both
// paths share one policy: a command file cannot grant itself a shell the
// user did not configure.
func buildShellTool(opts options) (tool.Tool, error) {
	config, err := shellToolConfig(opts)
	if err != nil {
		return nil, err
	}
	return shelltool.New(config)
}

// shellToolConfig resolves the shell tool configuration, including the
// sandbox backend and the mounts a container backend needs. It is separate
// from buildShellTool so the wiring is testable without executing a command.
func shellToolConfig(opts options) (shelltool.Config, error) {
	sandboxOptions := sandboxOptions{
		Backend:        opts.sandboxBackend,
		Roots:          []string(opts.sandboxRoots),
		AllowNetwork:   opts.sandboxAllowNetwork,
		Restricted:     opts.sandboxRestricted,
		Image:          opts.sandboxImage,
		Timeout:        opts.sandboxTimeout,
		ProtectedNames: []string(opts.sandboxProtected),
	}
	sandboxBackend, err := buildSandbox(sandboxOptions, opts.shellWorkingDir, opts.shellTimeout)
	if err != nil {
		return shelltool.Config{}, err
	}
	shellConfig := shelltool.Config{Policy: policy.ShellPolicy{
		WorkingDir:      opts.shellWorkingDir,
		AllowCommands:   []string(opts.shellAllow),
		RequireApproval: opts.approve != "never",
		MaxTimeout:      opts.shellTimeout,
		MaxOutputBytes:  opts.shellMaxOutputBytes,
	}}
	if sandboxBackend != nil {
		// Confined mode: the shell runs through the sandbox and the session
		// stays open so later calls reuse the same layout, which is what
		// makes escalation (and its checkpointed state) meaningful.
		shellConfig.Backend = shelltool.ShellBackendSandbox
		shellConfig.Sandbox = sandboxBackend
		shellConfig.EnvironmentID = opts.sandboxImage
		shellConfig.KeepSessionOpen = true
	}
	if isDockerBackend(opts.sandboxBackend) {
		mounts, err := dockerMounts(sandboxRoots(sandboxOptions, opts.shellWorkingDir), opts.sandboxRestricted)
		if err != nil {
			return shelltool.Config{}, err
		}
		shellConfig.Mounts = mounts
	}
	return shellConfig, nil
}
