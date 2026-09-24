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
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalcli "github.com/feiyu912/zenforge/approval/cli"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/docker"
	"github.com/feiyu912/zenforge/skill"
	skillfs "github.com/feiyu912/zenforge/skill/fs"
	"github.com/feiyu912/zenforge/tool"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
)

// The Q&A agent is a small application-shaped program: it loads Agent Skills
// from a filesystem catalog, confines the shell tool to a Docker sandbox (or
// runs it locally), and asks the operator to approve every command before it
// executes. Its transcript is one greppable line per observable step so a test
// can assert what the model was told and what the operator decided.
func main() {
	var question string
	var workspace string
	var skillRoot string
	var sandboxMode string
	var image string
	flag.StringVar(&question, "question", "", "question for the agent (defaults to one line from stdin)")
	flag.StringVar(&workspace, "workspace", ".", "workspace the shell tool may inspect")
	flag.StringVar(&skillRoot, "skill-root", envOrDefault("ZENFORGE_SKILL_ROOT", "skills"),
		"directory containing Agent Skill packages")
	flag.StringVar(&sandboxMode, "sandbox", "docker", "shell backend: docker or local")
	flag.StringVar(&image, "image", "alpine:3.20", "Docker image used by the sandbox shell backend")
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
	hostWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		fatal(err)
	}
	if info, err := os.Stat(hostWorkspace); err != nil || !info.IsDir() {
		fatal(fmt.Errorf("workspace %q is not a directory", workspace))
	}

	catalog, err := skillfs.New(skillRoot, skillfs.Options{Source: "qa-agent"})
	if err != nil {
		fatal(err)
	}
	skills, err := skill.NewBundle(context.Background(), catalog, nil)
	if err != nil {
		fatal(err)
	}
	// The bundle is the model-facing view of the catalog; listing the catalog
	// prints exactly the skills it advertised. The body stays out of the prompt
	// and is disclosed only when the model calls load_skill.
	descriptors, err := catalog.List(context.Background())
	if err != nil {
		fatal(err)
	}
	for _, descriptor := range descriptors {
		fmt.Printf("skill: loaded %s\n", descriptor.Name)
	}

	shellConfig := shelltool.Config{
		Policy: policy.ShellPolicy{
			WorkingDir:      hostWorkspace,
			RequireApproval: true,
			MaxTimeout:      30 * time.Second,
			MaxOutputBytes:  1 << 20,
		},
	}
	switch strings.TrimSpace(sandboxMode) {
	case "docker":
		container, err := docker.New(docker.Config{DefaultImage: image})
		if err != nil {
			fatal(err)
		}
		shellConfig.Backend = shelltool.ShellBackendSandbox
		shellConfig.Sandbox = container
		shellConfig.EnvironmentID = image
		shellConfig.Mounts = []sandbox.Mount{{
			Source: hostWorkspace, Destination: "/workspace", Mode: "ro",
		}}
	case "local":
		// No sandbox and no Docker: the command runs in the workspace with the
		// operator's approval. This is the backend CI and the test use.
		shellConfig.Backend = shelltool.ShellBackendLocal
	default:
		fatal(fmt.Errorf("unsupported -sandbox %q: want docker or local", sandboxMode))
	}
	shell := shelltool.Must(shellConfig)

	agent := zenforge.New(zenforge.Config{
		Model: modelClient,
		Instructions: "Answer the operator's question from observed evidence. When an available skill's " +
			"description matches the question, load that skill's instructions before acting on it. " +
			"Use the shell tool for read-only inspection of the workspace; the operator approves each " +
			"command. Keep the final answer concise and name the command that produced each fact.",
		Skills:      skills,
		Tools:       []zenforge.Tool{shell},
		Approval:    transcriptApproval{inner: approvalcli.New(input, os.Stderr)},
		ToolRuntime: []tool.Middleware{transcriptTools(os.Stdout)},
		MaxSteps:    12,
	})
	result, err := agent.Run(context.Background(), zenforge.Task{Input: question})
	if err != nil {
		fatal(err)
	}
	fmt.Printf("answer: %s\n", result.Output)
}

// transcriptApproval wraps the CLI broker so every answered request is one
// greppable line on stdout, while the CLI keeps ownership of the prompt on
// stderr: it prints the title, the risk, the numbered options and the "> "
// cursor, and the operator answers with an option number.
type transcriptApproval struct {
	inner approval.Broker
}

func (b transcriptApproval) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	decision, err := b.inner.Request(ctx, req)
	if err != nil {
		return decision, err
	}
	fmt.Printf("approval: %s %s\n", req.ToolName, decision.Action)
	return decision, nil
}

// transcriptTools prints one `tool:` line per model-requested call. An approval
// makes the agent re-invoke the same call with approved metadata, so the call
// id -- not the invocation -- is what makes a line unique.
func transcriptTools(out io.Writer) tool.Middleware {
	var mu sync.Mutex
	seen := make(map[string]struct{})
	return func(next tool.Invoker) tool.Invoker {
		return tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
			mu.Lock()
			_, duplicate := seen[call.ID]
			if !duplicate {
				seen[call.ID] = struct{}{}
			}
			mu.Unlock()
			if !duplicate {
				fmt.Fprintf(out, "tool: %s\n", call.Name)
			}
			return next.Invoke(ctx, call)
		})
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "qa-agent:", err)
	os.Exit(1)
}
