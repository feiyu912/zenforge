// http-harness-agent runs ZenForge as a local HTTP service. It is deliberately
// loopback-only: real deployments must add their own auth and tenancy layer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalsqlite "github.com/feiyu912/zenforge/approval/sqlite"
	checkpointsqlite "github.com/feiyu912/zenforge/checkpoint/sqlite"
	eventlogsqlite "github.com/feiyu912/zenforge/eventlog/sqlite"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/docker"
	"github.com/feiyu912/zenforge/server/harnesshttp"
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
	var address string
	var dataDir string
	var workspace string
	var image string
	var skillRoot string
	var recoverStale bool
	var recoveryMax int
	flag.StringVar(&address, "addr", "127.0.0.1:8080", "loopback HTTP listen address")
	flag.StringVar(&dataDir, "data-dir", ".zenforge/http-harness", "directory for SQLite state")
	flag.StringVar(&workspace, "workspace", ".", "host workspace mounted read-only into Docker")
	flag.StringVar(&image, "image", "alpine:3.20", "Docker image used by the shell tool")
	flag.StringVar(&skillRoot, "skill-root", envOrDefault("ZENFORGE_SKILL_ROOT", "examples/harness-agent/skills"), "Agent Skill catalog directory")
	flag.BoolVar(&recoverStale, "recover-stale", false, "explicitly resume expired detached runs during startup")
	flag.IntVar(&recoveryMax, "recovery-max", 32, "maximum stale runs to recover when -recover-stale is set")
	flag.Parse()

	if !isLoopbackAddress(address) {
		fatal(errors.New("-addr must be a loopback address; add application authentication before binding externally"))
	}
	if recoveryMax < 0 {
		fatal(errors.New("-recovery-max must not be negative"))
	}
	ctx := context.Background()
	workspaceRoot, err := filepath.Abs(workspace)
	if err != nil {
		fatal(err)
	}
	if info, err := os.Stat(workspaceRoot); err != nil || !info.IsDir() {
		fatal(fmt.Errorf("workspace %q must be an existing directory", workspaceRoot))
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fatal(err)
	}

	modelClient, err := provider.FromEnv()
	if err != nil {
		fatal(err)
	}
	container, err := docker.New(docker.Config{DefaultImage: image})
	if err != nil {
		fatal(err)
	}
	catalog, err := skillfs.New(skillRoot, skillfs.Options{Source: "http-harness-agent"})
	if err != nil {
		fatal(err)
	}
	skills, err := skill.NewBundle(ctx, catalog, nil)
	if err != nil {
		fatal(err)
	}

	events, err := eventlogsqlite.Open(ctx, filepath.Join(dataDir, "events.db"))
	if err != nil {
		fatal(err)
	}
	defer closeOrLog("event store", events.Close)
	checkpoints, err := checkpointsqlite.Open(ctx, filepath.Join(dataDir, "checkpoints.db"))
	if err != nil {
		fatal(err)
	}
	defer closeOrLog("checkpoint store", checkpoints.Close)
	approvalStore, err := approvalsqlite.OpenInbox(ctx, filepath.Join(dataDir, "approvals.db"))
	if err != nil {
		fatal(err)
	}
	defer closeOrLog("approval store", approvalStore.Close)
	approvalInbox, err := approval.NewStoreBroker(approvalStore, approval.StoreBrokerOptions{})
	if err != nil {
		fatal(err)
	}
	runRegistry, err := harnesshttp.OpenSQLiteRunRegistry(ctx, filepath.Join(dataDir, "runs.db"))
	if err != nil {
		fatal(err)
	}
	defer closeOrLog("run registry", runRegistry.Close)

	inspect := tools.Must("inspect_path", "Classify a workspace-relative path without reading its contents.",
		func(_ context.Context, in inspectInput) (inspectOutput, error) {
			clean := filepath.Clean(in.Path)
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return inspectOutput{}, errors.New("path must stay inside the workspace")
			}
			info, err := os.Stat(filepath.Join(workspaceRoot, clean))
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
			Source: workspaceRoot, Destination: "/workspace", Mode: "ro",
		}},
		Policy: policy.ShellPolicy{
			WorkingDir:      workspaceRoot,
			RequireApproval: true,
			MaxTimeout:      30 * time.Second,
			MaxOutputBytes:  1 << 20,
		},
	})

	runtime, err := harnesshttp.NewRuntime(zenforge.Config{
		Model: modelClient,
		Instructions: "Answer using evidence from the mounted workspace. Load an Agent Skill when its description matches the task. " +
			"Use inspect_path for local metadata and shell for containerized read-only inspection. Keep the final answer concise.",
		Skills:      skills,
		Tools:       []zenforge.Tool{inspect, shell},
		Checkpoints: checkpoints,
		MaxSteps:    12,
	}, events, harnesshttp.RuntimeOptions{
		ApprovalInbox: approvalInbox,
		Manager: harnesshttp.RunManagerOptions{
			MaxActive:         16,
			RunTimeout:        10 * time.Minute,
			TerminalRetention: 10 * time.Minute,
			Registry:          runRegistry,
			OwnerID:           "local-http-harness",
			LeaseDuration:     30 * time.Second,
		},
	})
	if err != nil {
		fatal(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runtime.Close(shutdownCtx); err != nil {
			log.Printf("close runtime: %v", err)
		}
	}()
	if recoverStale {
		results, err := runtime.Manager.RecoverStale(ctx, harnesshttp.RecoveryOptions{Max: recoveryMax})
		if err != nil {
			fatal(fmt.Errorf("recover stale runs: %w", err))
		}
		for _, result := range results {
			if result.Error != "" {
				log.Printf("stale run %s was not recovered: %s", result.RunID, result.Error)
			}
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/runs/start", runtime.Handler.ServeDetachedStart)
	mux.HandleFunc("/runs/resume", runtime.Handler.ServeDetachedResume)
	mux.HandleFunc("/runs/status", runtime.Handler.ServeDetachedStatus)
	mux.HandleFunc("/runs", runtime.Handler.ServeDetachedRuns)
	mux.HandleFunc("/runs/attach", runtime.Handler.ServeDetachedAttach)
	mux.HandleFunc("/runs/cancel", runtime.Handler.ServeDetachedCancel)
	mux.HandleFunc("/approvals", runtime.Handler.ServeApprovals)
	mux.HandleFunc("/approval", runtime.Handler.ServeApproval)

	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("ZenForge HTTP harness listening on http://%s", address)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fatal(err)
		}
	}
}

func isLoopbackAddress(address string) bool {
	return strings.HasPrefix(address, "127.0.0.1:") || strings.HasPrefix(address, "[::1]:") || strings.HasPrefix(address, "localhost:")
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func closeOrLog(name string, close func() error) {
	if err := close(); err != nil {
		log.Printf("close %s: %v", name, err)
	}
}

func fatal(err error) {
	log.Fatal("http-harness-agent: ", err)
}
