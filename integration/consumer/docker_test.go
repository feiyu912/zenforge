package consumer_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/sandbox/docker"
)

func TestDockerAdapterRunsInsideContainerWithWorkspaceMount(t *testing.T) {
	if os.Getenv("ZENFORGE_DOCKER_INTEGRATION") != "1" {
		t.Skip("set ZENFORGE_DOCKER_INTEGRATION=1 to run Docker integration")
	}
	if runtime.GOOS == "windows" {
		t.Fatal("Docker integration requires Unix host path mounts")
	}

	hostWorkspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostWorkspace, "consumer-marker.txt"), []byte("mounted-from-host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter, err := docker.New(docker.Config{
		DefaultImage: "alpine:3.20", DefaultTimeout: 90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := adapter.Open(ctx, sandbox.OpenRequest{
		RunID:      "consumer-docker-integration",
		WorkingDir: hostWorkspace,
		Mounts: []sandbox.Mount{{
			Source: hostWorkspace, Destination: "/workspace", Mode: "ro",
		}},
	})
	if err != nil {
		t.Fatalf("open Docker sandbox (gate is enabled): %v", err)
	}
	defer func() {
		if err := adapter.Close(context.Background(), session); err != nil {
			t.Errorf("close Docker sandbox: %v", err)
		}
	}()

	result, err := adapter.Execute(ctx, session, sandbox.ExecuteRequest{
		Command: `printf 'kernel=%s\ncwd=%s\nmarker=%s\n' "$(uname -s)" "$PWD" "$(cat consumer-marker.txt)"`,
	})
	if err != nil {
		t.Fatalf("execute in Docker sandbox: %v", err)
	}
	for _, want := range []string{"kernel=Linux", "cwd=/workspace", "marker=mounted-from-host"} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("stdout missing %q: %s", want, result.Stdout)
		}
	}
}
