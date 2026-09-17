package cli

import (
	"context"
	"flag"
	"path/filepath"
	"testing"

	"github.com/feiyu912/zenforge/memory"
)

func TestBuildMemoryRequiresADirectory(t *testing.T) {
	provider, err := buildMemory(defaultOptions(), nil)
	if err != nil || provider != nil {
		t.Fatalf("buildMemory = %#v, %v", provider, err)
	}
}

func TestBuildMemoryExpandsHomeAndScope(t *testing.T) {
	opts := defaultOptions()
	opts.memoryDir = "~/zenforge-memories"
	opts.memoryScope = "project"
	opts.workspace = "/repo/a"
	provider, err := buildMemory(opts, nil)
	if err != nil {
		t.Fatalf("buildMemory returned error: %v", err)
	}
	manager, ok := provider.(*memory.Manager)
	if !ok {
		t.Fatalf("provider = %T", provider)
	}
	store, ok := manager.Store.(*memory.FileStore)
	if !ok {
		t.Fatalf("store = %T", manager.Store)
	}
	if filepath.Base(store.Root) != "zenforge-memories" {
		t.Fatalf("the home path was not expanded: %q", store.Root)
	}
	if manager.Scope != memory.ScopeProject || manager.Project != "/repo/a" {
		t.Fatalf("manager = %#v", manager)
	}
	// Without --memory-distill the manager only injects; distillation is
	// opt-in because it costs a model call per run.
	if manager.Distiller != nil {
		t.Fatalf("a distiller was configured without the flag: %#v", manager.Distiller)
	}
	if _, err := buildMemory(options{memoryDir: t.TempDir(), memoryScope: "galaxy"}, nil); err == nil {
		t.Fatal("an unknown scope was accepted")
	}
	if _, err := buildMemory(options{memoryDir: t.TempDir(), memoryDistill: true}, nil); err == nil {
		t.Fatal("distillation without a model was accepted")
	}
}

func TestMemoryFlagsAreBound(t *testing.T) {
	opts := defaultOptions()
	fs := flag.NewFlagSet("memory-test", flag.ContinueOnError)
	bindOptions(fs, &opts)
	if err := fs.Parse([]string{"--memory", "/tmp/mem", "--memory-scope", "project", "--memory-distill"}); err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if opts.memoryDir != "/tmp/mem" || opts.memoryScope != "project" || !opts.memoryDistill {
		t.Fatalf("options = %#v", opts)
	}
}

func TestMemoryRoundTripThroughTheManager(t *testing.T) {
	// The provider the CLI builds is the one the agent uses, so a full
	// write/read cycle is exercised here rather than only in the package.
	opts := defaultOptions()
	opts.memoryDir = t.TempDir()
	opts.memoryScope = "user"
	provider, err := buildMemory(opts, nil)
	if err != nil {
		t.Fatalf("buildMemory returned error: %v", err)
	}
	// Seed the store directly: the manager only distils when a distiller is
	// configured, which is the opt-in path.
	manager := provider.(*memory.Manager)
	store := manager.Store.(*memory.FileStore)
	entry := memory.Entry{ID: memory.ID(memory.ScopeUser, "", "the repo runs make check"), Scope: memory.ScopeUser, Text: "the repo runs make check"}
	if _, err := store.Append(context.Background(), entry); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	summary, err := provider.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary returned error: %v", err)
	}
	if summary == "" {
		t.Fatal("the seeded memory was not summarized")
	}
}
