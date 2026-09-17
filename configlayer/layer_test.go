package configlayer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeLayer(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}

func TestStackMergesByPrecedenceNotInsertionOrder(t *testing.T) {
	stack := New()
	// Added out of order on purpose: the project layer outranks the
	// user layer, which outranks system.
	stack.Add(Source{Kind: KindProject, Path: ".zenforge/zenforge.json"}, map[string]any{
		"model": map[string]any{"name": "project-model", "contextWindow": float64(9)},
	})
	stack.Add(Source{Kind: KindUser, Path: "user.json"}, map[string]any{
		"model":    map[string]any{"name": "user-model", "provider": "anthropic"},
		"agent":    map[string]any{"maxSteps": float64(7)},
		"profiles": map[string]any{"ci": map[string]any{"agent": map[string]any{"maxSteps": float64(3)}}},
	})
	stack.Add(Source{Kind: KindSystem, Path: "/etc/zenforge/zenforge.json"}, map[string]any{
		"model": map[string]any{"name": "system-model", "apiKeyEnv": "SYSTEM_KEY"},
	})

	merged, provenance, err := stack.Merge()
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	model := merged["model"].(map[string]any)
	if model["name"] != "project-model" {
		t.Fatalf("model.name = %v, want the project layer to win", model["name"])
	}
	if model["provider"] != "anthropic" {
		t.Fatalf("model.provider = %v, want the user layer to win", model["provider"])
	}
	if model["apiKeyEnv"] != "SYSTEM_KEY" {
		t.Fatalf("model.apiKeyEnv = %v, want the system layer to survive", model["apiKeyEnv"])
	}
	if merged["agent"].(map[string]any)["maxSteps"] != float64(7) {
		t.Fatalf("agent.maxSteps = %v", merged["agent"])
	}
	if got := provenance["model.name"]; got.Kind != KindProject {
		t.Fatalf("provenance for model.name = %#v, want project", got)
	}
	if got := provenance["model.provider"]; got.Kind != KindUser {
		t.Fatalf("provenance for model.provider = %#v, want user", got)
	}
}

func TestStackAppliesProfilesAboveUserBelowProject(t *testing.T) {
	stack := New()
	stack.Add(Source{Kind: KindUser, Path: "user.json"}, map[string]any{
		"agent": map[string]any{"maxSteps": float64(7)},
		"profiles": map[string]any{
			"ci": map[string]any{"agent": map[string]any{"maxSteps": float64(3)}},
		},
	})
	overrides, err := ExtractProfile(stack.Layers()[0].Data, "ci")
	if err != nil {
		t.Fatalf("ExtractProfile returned error: %v", err)
	}
	stack.Add(Source{Kind: KindProfile, Path: "user.json", Profile: "ci"}, overrides)
	stack.Add(Source{Kind: KindProject, Path: "project.json"}, map[string]any{
		"agent": map[string]any{"maxSteps": float64(11)},
	})

	merged, provenance, err := stack.Merge()
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	if got := merged["agent"].(map[string]any)["maxSteps"]; got != float64(11) {
		t.Fatalf("agent.maxSteps = %v, want the project layer above the profile", got)
	}
	if got := provenance["agent.maxSteps"]; got.Kind != KindProject {
		t.Fatalf("provenance = %#v, want project", got)
	}

	// Without a project layer the profile outranks the base user file.
	profileOnly := New()
	profileOnly.Add(Source{Kind: KindUser, Path: "user.json"}, map[string]any{
		"agent": map[string]any{"maxSteps": float64(7)},
	})
	profileOnly.Add(Source{Kind: KindProfile, Profile: "ci"}, map[string]any{
		"agent": map[string]any{"maxSteps": float64(3)},
	})
	merged, _, err = profileOnly.Merge()
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	if got := merged["agent"].(map[string]any)["maxSteps"]; got != float64(3) {
		t.Fatalf("agent.maxSteps = %v, want the profile to win", got)
	}
}

func TestExtractProfileReportsUnknownNames(t *testing.T) {
	document := map[string]any{
		"profiles": map[string]any{
			"ci":  map[string]any{"agent": map[string]any{"maxSteps": float64(1)}},
			"dev": map[string]any{},
		},
	}
	_, err := ExtractProfile(document, "prod")
	if err == nil || !strings.Contains(err.Error(), "available: ci, dev") {
		t.Fatalf("unknown profile error = %v", err)
	}
	if _, err := ExtractProfile(map[string]any{}, "ci"); err == nil {
		t.Fatal("missing profiles key was accepted")
	}
	if _, err := ExtractProfile(map[string]any{"profiles": map[string]any{"ci": "nope"}}, "ci"); err == nil {
		t.Fatal("non-object profile was accepted")
	}
}

func TestStackAddFileSkipsMissingOptionalLayers(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.json")
	stack := New()
	if err := stack.AddFile(KindUser, missing, false); err != nil {
		t.Fatalf("optional missing file returned error: %v", err)
	}
	layers := stack.Layers()
	if len(layers) != 1 || layers[0].Disabled == "" {
		t.Fatalf("layers = %#v, want a disabled record", layers)
	}
	if err := stack.AddFile(KindFile, missing, true); err == nil {
		t.Fatal("required missing file was accepted")
	}

	invalid := filepath.Join(dir, "list.json")
	writeLayer(t, invalid, `[1,2]`)
	if err := stack.AddFile(KindFile, invalid, true); err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("non-object layer error = %v", err)
	}
}

func TestMergeRejectsShapeConflicts(t *testing.T) {
	stack := New()
	stack.Add(Source{Kind: KindUser, Path: "user.json"}, map[string]any{"agent": map[string]any{"maxSteps": float64(1)}})
	stack.Add(Source{Kind: KindProject, Path: "project.json"}, map[string]any{"agent": "disabled"})
	_, _, err := stack.Merge()
	if err == nil || !strings.Contains(err.Error(), "agent is an object in one layer") {
		t.Fatalf("shape conflict error = %v", err)
	}
}

func TestProjectConfigPathWalksUp(t *testing.T) {
	root := t.TempDir()
	writeLayer(t, filepath.Join(root, ".zenforge", "zenforge.json"), `{"agent":{"maxSteps":1}}`)
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if got := ProjectConfigPath(nested); got != filepath.Join(root, ".zenforge", "zenforge.json") {
		t.Fatalf("ProjectConfigPath = %q", got)
	}
	empty := t.TempDir()
	if got := ProjectConfigPath(empty); got != "" {
		t.Fatalf("ProjectConfigPath with no file = %q, want empty", got)
	}
}

func TestMergeTreatsListsAsReplacements(t *testing.T) {
	stack := New()
	stack.Add(Source{Kind: KindUser}, map[string]any{"checkpoint": map[string]any{"tags": []any{"a", "b"}}})
	stack.Add(Source{Kind: KindProject}, map[string]any{"checkpoint": map[string]any{"tags": []any{"c"}}})
	merged, _, err := stack.Merge()
	if err != nil {
		t.Fatalf("Merge returned error: %v", err)
	}
	got := merged["checkpoint"].(map[string]any)["tags"]
	if !reflect.DeepEqual(got, []any{"c"}) {
		t.Fatalf("tags = %#v, want the higher layer's list", got)
	}
}
