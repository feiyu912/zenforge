package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/feiyu912/zenforge/memory"
	"github.com/feiyu912/zenforge/model"
)

// buildMemory wires the durable memory store. The store is always readable
// when a directory is configured; distillation additionally needs a model
// and is opt-in, because it costs one model call per run.
func buildMemory(opts options, provider model.Model) (memory.Provider, error) {
	root := strings.TrimSpace(opts.memoryDir)
	if root == "" {
		return nil, nil
	}
	scope, err := memory.ParseScope(opts.memoryScope)
	if err != nil {
		return nil, err
	}
	// "~" is a shell convention, not a path: the flag arrives unexpanded
	// when it is set from a config file.
	expanded := root
	if expanded == "~" || strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(expanded, "~"), "/"))
		}
	}
	manager := &memory.Manager{
		Store:   memory.NewFileStore(expanded),
		Scope:   scope,
		Project: opts.workspace,
	}
	if opts.memoryDistill {
		if provider == nil {
			return nil, fmt.Errorf("memory distillation requires a model")
		}
		manager.Distiller = memory.ModelDistiller{Model: provider}
	}
	return manager, nil
}
