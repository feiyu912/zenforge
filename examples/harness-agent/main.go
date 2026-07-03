package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	approvalcli "github.com/feiyu912/zenforge/approval/cli"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/docker"
	"github.com/feiyu912/zenforge/skill"
	skillfs "github.com/feiyu912/zenforge/skill/fs"
	"github.com/feiyu912/zenforge/tools"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
)

type inspectInput struct {
	Path string `json:"path" jsonschema:"required,description=Workspace-relative path to inspect"`
}

type inspectOutput struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

func main() {
	var question string
	var image string
	var skillRoot string
	flag.StringVar(&question, "question", "", "task for the agent (defaults to one line from stdin)")
	flag.StringVar(&image, "image", "alpine:3.20", "Docker image used by the shell tool")
	flag.StringVar(&skillRoot, "skill-root", envOrDefault("ZENFORGE_SKILL_ROOT", "examples/harness-agent/skills"),
		"directory containing Agent Skill packages")
	flag.Parse()

	input := bufio.NewReader(os.Stdin)
	if strings.TrimSpace(question) == "" {
		fmt.Fprint(os.Stderr, "Question: ")
		line, err := input.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fatal(err)
		}
		question = strings.TrimSpace(line)
	}
	if question == "" {
		fatal(errors.New("question is required"))
	}

	modelClient, err := provider.FromEnv()
	if err != nil {
		fatal(err)
	}
	container, err := docker.New(docker.Config{DefaultImage: image})
	if err != nil {
		fatal(err)
	}
	hostWorkspace, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	hostWorkspace, err = filepath.Abs(hostWorkspace)
	if err != nil {
		fatal(err)
	}
	catalog, err := skillfs.New(skillRoot, skillfs.Options{Source: "harness-agent"})
	if err != nil {
		fatal(err)
	}
	skills, err := skill.NewBundle(context.Background(), catalog, nil)
	if err != nil {
		fatal(err)
	}

	inspect := tools.Must("inspect_path", "Classify a workspace-relative path without reading its contents.",
		func(ctx context.Context, in inspectInput) (inspectOutput, error) {
			clean := filepath.Clean(in.Path)
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return inspectOutput{}, errors.New("path must stay inside the workspace")
			}
			info, err := os.Stat(filepath.Join(hostWorkspace, clean))
			if err != nil {
				return inspectOutput{}, err
			}
			kind := "file"
			if info.IsDir() {
				kind = "directory"
			}
			return inspectOutput{Path: filepath.ToSlash(clean), Kind: kind}, nil
		})

	shell := shelltool.Must(shelltool.Config{
		Backend:       shelltool.ShellBackendSandbox,
		Sandbox:       container,
		EnvironmentID: image,
		Mounts: []sandbox.Mount{{
			Source: hostWorkspace, Destination: "/workspace", Mode: "ro",
		}},
		Policy: policy.ShellPolicy{
			WorkingDir:      hostWorkspace,
			RequireApproval: true,
			MaxTimeout:      30 * time.Second,
			MaxOutputBytes:  1 << 20,
		},
	})

	agent := zenforge.New(zenforge.Config{
		Model: modelClient,
		Instructions: "Answer using evidence from the mounted workspace. Load an Agent Skill when its " +
			"description matches the task. inspect_path remains an ordinary typed tool for local metadata; " +
			"it is not a skill. Use shell for containerized read-only inspection. Keep the final answer concise.",
		Skills:   skills,
		Tools:    []zenforge.Tool{inspect, shell},
		Approval: approvalcli.New(input, os.Stderr),
		MaxSteps: 12,
	})
	result, err := agent.Run(context.Background(), zenforge.Task{Input: question})
	if err != nil {
		fatal(err)
	}
	fmt.Println(result.Output)
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "harness-agent:", err)
	os.Exit(1)
}
