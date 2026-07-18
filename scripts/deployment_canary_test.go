package scripts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlatformDeploymentCanaryRunsCatalogAndSSELifecycle(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required by the deployment canary")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer platform-token" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/agents":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"code":0,"data":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/query":
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for _, key := range []string{"agentKey", "chatId", "runId", "requestId", "message"} {
				if strings.TrimSpace(request[key]) == "" {
					http.Error(w, "missing "+key, http.StatusBadRequest)
					return
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []string{"request.query", "run.start", "content.delta", "run.complete"} {
				_, _ = fmt.Fprintf(w, "data: {\"type\":\"%s\"}\n\n", event)
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := runCanary(t, "verify-platform-deployment.sh", []string{"--run-query"}, map[string]string{
		"ZENFORGE_PLATFORM_BASE_URL":  server.URL,
		"ZENFORGE_PLATFORM_TOKEN":     "platform-token",
		"ZENFORGE_PLATFORM_AGENT_KEY": "canary-agent",
	})
	if !strings.Contains(output, "ZenForge SSE lifecycle verified") {
		t.Fatalf("canary output = %q", output)
	}
}

func TestContainerHubDeploymentCanaryCreatesExecutesAndStopsSession(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required by the deployment canary")
	}
	var mu sync.Mutex
	sessions := make(map[string]bool)
	stopped := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer hub-token" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtime-info":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"engine":"docker"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/create":
			var request struct {
				SessionID       string `json:"session_id"`
				EnvironmentName string `json:"environment_name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.SessionID == "" || request.EnvironmentName != "shell" {
				http.Error(w, "invalid create request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			sessions[request.SessionID] = true
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"session_id":%q}`, request.SessionID)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/sessions/") && strings.HasSuffix(r.URL.Path, "/execute"):
			sessionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/execute")
			mu.Lock()
			created := sessions[sessionID]
			mu.Unlock()
			if !created {
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			var request struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Command != "/bin/sh" || len(request.Args) != 2 || request.Args[1] != "printf zenforge-containerhub-ok" {
				http.Error(w, "invalid execute request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"exit_code":0,"stdout":"zenforge-containerhub-ok"}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/sessions/") && strings.HasSuffix(r.URL.Path, "/stop"):
			sessionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/stop")
			mu.Lock()
			created := sessions[sessionID]
			stopped[sessionID] = created
			mu.Unlock()
			if !created {
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := runCanary(t, "verify-containerhub-deployment.sh", []string{"--run-session"}, map[string]string{
		"ZENFORGE_CONTAINERHUB_URL":         server.URL,
		"ZENFORGE_CONTAINERHUB_TOKEN":       "hub-token",
		"ZENFORGE_CONTAINERHUB_ENVIRONMENT": "shell",
	})
	if !strings.Contains(output, "create, execute, and cleanup verified") {
		t.Fatalf("canary output = %q", output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(stopped) != 1 {
		t.Fatalf("stopped sessions = %#v", stopped)
	}
	for sessionID, wasStopped := range stopped {
		if !wasStopped || !sessions[sessionID] {
			t.Fatalf("session %q cleanup state = %v", sessionID, wasStopped)
		}
	}
}

func runCanary(t *testing.T, script string, args []string, values map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	commandArgs := append([]string{filepath.Join(".", script)}, args...)
	command := exec.CommandContext(ctx, "bash", commandArgs...)
	command.Env = os.Environ()
	for key, value := range values {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, output)
	}
	return string(output)
}
