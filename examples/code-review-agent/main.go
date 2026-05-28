package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/feiyu912/zenforge"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	eventlogjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model/openai"
	"github.com/feiyu912/zenforge/policy"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	workspacetools "github.com/feiyu912/zenforge/tools/workspace"
	workspacelocal "github.com/feiyu912/zenforge/workspace/local"
)

func main() {
	ctx := context.Background()
	root := env("ZENFORGE_WORKSPACE", ".")
	runDir := env("ZENFORGE_RUN_DIR", ".zenforge/runs")

	ws, err := workspacelocal.New(workspacelocal.Config{
		Root:          root,
		MaxReadBytes:  1_000_000,
		MaxWriteBytes: 1,
	})
	must(err)
	workspaceTools, err := workspacetools.Tools(workspacetools.Config{Workspace: ws})
	must(err)
	shellTool, err := shelltool.New(shelltool.Config{Policy: policy.ShellPolicy{
		WorkingDir:     root,
		AllowCommands:  []string{"go test ./...", "grep", "find"},
		MaxTimeout:     30 * time.Second,
		MaxOutputBytes: 256_000,
	}})
	must(err)

	agent := zenforge.New(zenforge.Config{
		Model: openai.New(openai.Config{
			APIKey:  os.Getenv("OPENAI_API_KEY"),
			Model:   env("OPENAI_MODEL", "gpt-4.1"),
			BaseURL: os.Getenv("OPENAI_BASE_URL"),
		}),
		Instructions: "Review code like a senior engineer. Lead with concrete findings, then mention test gaps. Do not modify files.",
		Tools:        append(workspaceTools, shellTool),
		Events:       eventlogjsonl.New(runDir),
		Checkpoints:  checkpointjsonl.New(runDir),
		MaxSteps:     12,
	})

	input := "Review this repository for likely bugs, risky areas, and missing tests."
	if len(os.Args) > 1 {
		input = os.Args[1]
	}
	result, err := agent.Run(ctx, zenforge.Task{Input: input})
	must(err)
	fmt.Println(result.Output)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
