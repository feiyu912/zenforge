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
	sandboxBackend, err := buildSandbox(sandboxOptions{
		Backend:        opts.sandboxBackend,
		Roots:          []string(opts.sandboxRoots),
		AllowNetwork:   opts.sandboxAllowNetwork,
		Restricted:     opts.sandboxRestricted,
		Image:          opts.sandboxImage,
		Timeout:        opts.sandboxTimeout,
		ProtectedNames: []string(opts.sandboxProtected),
	}, opts.shellWorkingDir, opts.shellTimeout)
	if err != nil {
		return nil, err
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
	return shelltool.New(shellConfig)
}
