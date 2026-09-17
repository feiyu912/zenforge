package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/hooks"
)

// hooksFile is the on-disk hook configuration. The map form matches the
// reference's hooks JSON: event name to a list of commands.
type hooksFile struct {
	Hooks map[string][]hooks.Command `json:"hooks"`
}

// loadHooks reads and validates a hooks file. An empty path returns no
// hooks, so the caller can treat "not configured" and "configured empty"
// the same way.
func loadHooks(path string) (hooks.Config, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("read hooks file %s: %w", trimmed, err)
	}
	var file hooksFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	// A hook configuration controls what may run, so a typo in a field name
	// must be an error rather than a silently ignored hook.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse hooks file %s: %w", trimmed, err)
	}
	if len(file.Hooks) == 0 {
		return nil, fmt.Errorf("hooks file %s defines no hooks", trimmed)
	}
	config := hooks.Config{}
	for name, commands := range file.Hooks {
		event, err := hooks.ParseEvent(name)
		if err != nil {
			return nil, fmt.Errorf("hooks file %s: %w", trimmed, err)
		}
		config[event] = append(config[event], commands...)
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("hooks file %s: %w", trimmed, err)
	}
	return config, nil
}

// hookEventNames lists the configured events, for diagnostics.
func hookEventNames(config hooks.Config) []string {
	names := make([]string, 0, len(config))
	for event := range config {
		names = append(names, string(event))
	}
	sort.Strings(names)
	return names
}

// buildHookEngine loads the configured hooks, if any.
func buildHookEngine(opts options) (*hooks.Engine, error) {
	if strings.TrimSpace(opts.hooksPath) == "" {
		return nil, nil
	}
	config, err := loadHooks(opts.hooksPath)
	if err != nil {
		return nil, err
	}
	return hooks.New(hooks.Options{Hooks: config, CWD: opts.shellWorkingDir})
}
