package sandbox

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSandboxMetadataJSONRoundTrip(t *testing.T) {
	req := OpenRequest{
		RunID:         "run_1",
		SubtaskID:     "sub_1",
		EnvironmentID: "go",
		WorkingDir:    "/workspace",
		Env:           map[string]string{"A": "B"},
		Mounts:        []Mount{{Source: ".", Destination: "/workspace", Mode: "rw"}},
		Metadata:      map[string]any{"k": "v"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var decoded OpenRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded.RunID != req.RunID || decoded.Mounts[0].Destination != "/workspace" {
		t.Fatalf("decoded request = %#v", decoded)
	}

	execReq := ExecuteRequest{Command: "go test ./...", Timeout: time.Second}
	data, err = json.Marshal(execReq)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var decodedExec ExecuteRequest
	if err := json.Unmarshal(data, &decodedExec); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decodedExec.Timeout != time.Second {
		t.Fatalf("timeout = %s", decodedExec.Timeout)
	}
}

func TestSessionKey(t *testing.T) {
	if got := SessionKey("run_1", ""); got != "run-run_1" {
		t.Fatalf("SessionKey main = %q", got)
	}
	if got := SessionKey("run_1", "task_1"); got != "run-run_1-task_1" {
		t.Fatalf("SessionKey subtask = %q", got)
	}
	if got := SessionKey(" run_1 ", " task_1 "); got != "run-run_1-task_1" {
		t.Fatalf("SessionKey normalized = %q", got)
	}
	if got := SessionKey(" ", "task_1"); got != "" {
		t.Fatalf("SessionKey empty run = %q", got)
	}
}
