package main

import (
	"context"
	"fmt"
	"os"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/model/openai"
	"github.com/feiyu912/zenforge/tools"
)

type lookupInput struct {
	Query string `json:"query" jsonschema:"required,description=Lookup query"`
}

type lookupOutput struct {
	Result string `json:"result"`
}

func main() {
	lookup := tools.Must("lookup_project_fact", "Look up one hard-coded project fact.", func(ctx context.Context, in lookupInput) (lookupOutput, error) {
		return lookupOutput{Result: "ZenForge is a Go agent harness with tools, checkpoints, planning, and streaming events."}, nil
	})

	agent := zenforge.New(zenforge.Config{
		Model: openai.New(openai.Config{
			APIKey:  os.Getenv("OPENAI_API_KEY"),
			Model:   env("OPENAI_MODEL", "gpt-4.1"),
			BaseURL: os.Getenv("OPENAI_BASE_URL"),
		}),
		Instructions: "Use the lookup tool when asked about this project.",
		Tools:        []zenforge.Tool{lookup},
		MaxSteps:     4,
	})

	result, err := agent.Run(context.Background(), zenforge.Task{
		Input: "Use the tool to explain what ZenForge is in one sentence.",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Output)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
