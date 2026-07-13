package containerhub

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

// TestAdapterRunsAgainstRealContainerHub is opt-in because it creates a real
// Container Hub session. Set ZENFORGE_CONTAINERHUB_INTEGRATION_URL to a
// loopback or otherwise disposable Hub endpoint. ZENFORGE_CONTAINERHUB_TOKEN
// and ZENFORGE_CONTAINERHUB_ENVIRONMENT are optional.
func TestAdapterRunsAgainstRealContainerHub(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZENFORGE_CONTAINERHUB_INTEGRATION_URL"))
	if baseURL == "" {
		t.Skip("set ZENFORGE_CONTAINERHUB_INTEGRATION_URL to run Container Hub integration")
	}

	environmentID := strings.TrimSpace(os.Getenv("ZENFORGE_CONTAINERHUB_ENVIRONMENT"))
	if environmentID == "" {
		environmentID = "shell"
	}
	adapter, err := New(Config{
		BaseURL:      baseURL,
		AuthToken:    os.Getenv("ZENFORGE_CONTAINERHUB_TOKEN"),
		DefaultEnvID: environmentID,
		Timeout:      45 * time.Second,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	session, err := adapter.Open(ctx, sandbox.OpenRequest{
		RunID:      "zenforge-containerhub-" + time.Now().UTC().Format("20060102t150405.000000000"),
		WorkingDir: "/workspace",
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.Background(), session); err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	})

	result, err := adapter.Execute(ctx, session, sandbox.ExecuteRequest{
		Command: "printf zenforge-containerhub-ok",
		CWD:     "/workspace",
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "zenforge-containerhub-ok") {
		t.Fatalf("Execute result = %#v", result)
	}
}
