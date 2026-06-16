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
	workspaceRoot := env("ZENFORGE_WORKSPACE", ".")
	runDir := env("ZENFORGE_RUN_DIR", ".zenforge/runs")

	ws, err := workspacelocal.New(workspacelocal.Config{
		Root:            workspaceRoot,
		MaxReadBytes:    1_000_000,
		MaxWriteBytes:   1,
		CreateParentDir: false,
	})
	must(err)

	workspaceTools, err := workspacetools.Tools(workspacetools.Config{
		Workspace:              ws,
		Snapshots:              workspacetools.NewSnapshotStore(),
		RequireReadBeforeWrite: true,
		Policy: policy.FilePolicy{
			ReadRoots:       []string{"."},
			WriteRoots:      []string{".zenforge/generated"},
			RequireApproval: false,
		},
	})
	must(err)
	shellTool, err := shelltool.New(shelltool.Config{Policy: policy.ShellPolicy{
		WorkingDir:     workspaceRoot,
		AllowCommands:  []string{"go test ./...", "go vet ./...", "grep", "find"},
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
		Instructions: "You are a senior Go backend engineer. Produce practical refactor plans with concrete file references. Do not write files.",
		Tools:        append(workspaceTools, shellTool),
		Events:       eventlogjsonl.New(runDir),
		Checkpoints:  checkpointjsonl.New(runDir),
		MaxSteps:     20,
		Planning:     zenforge.PlanningPlanExecute,
	})

	input := "Analyze this repository and produce a prioritized refactor plan. Read files and grep as needed. Run tests only if useful."
	if len(os.Args) > 1 {
		input = os.Args[1]
	}
	events, err := agent.Stream(ctx, zenforge.Task{Input: input})
	must(err)
	for event := range events {
		render(event)
	}
}

func render(event zenforge.Event) {
	switch event.Type {
	case zenforge.EventModelDelta:
		fmt.Print(event.Payload["textDelta"])
	case zenforge.EventToolCall:
		fmt.Printf("\n> tool %s\n", event.Payload["toolName"])
	case zenforge.EventTodoUpdated:
		fmt.Printf("\n> todos updated\n")
	case zenforge.EventRunDone:
		if output, _ := event.Payload["output"].(string); output != "" {
			fmt.Printf("\n%s\n", output)
		}
		fmt.Printf("\nrun %s done\n", event.RunID())
	case zenforge.EventRunError:
		fmt.Fprintf(os.Stderr, "\nrun %s error: %v\n", event.RunID(), event.Payload["error"])
	default:
		fmt.Printf("\n%s\n", event.Type)
	}
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
