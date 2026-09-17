package configlayer

import (
	"strings"
	"testing"
)

type fakeConfig struct {
	Agent struct {
		MaxSteps int `json:"maxSteps"`
	} `json:"agent"`
	Shell struct {
		Enabled *bool `json:"enabled"`
	} `json:"shell"`
}

func TestRequirementsEnforceValuesOverEveryLayer(t *testing.T) {
	requirements, err := ParseRequirements("requirements (/etc/zenforge/requirements.json)", map[string]any{
		"enforce": map[string]any{
			"shell.enabled": false,
			"agent.mode":    "react",
		},
	})
	if err != nil {
		t.Fatalf("ParseRequirements returned error: %v", err)
	}
	candidate := map[string]any{
		"shell": map[string]any{"enabled": true},
		"agent": map[string]any{"maxSteps": float64(5)},
	}
	applied, err := requirements.Apply(candidate)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if applied["shell"].(map[string]any)["enabled"] != false {
		t.Fatalf("enforced shell.enabled = %v, want false", applied["shell"])
	}
	if applied["agent"].(map[string]any)["mode"] != "react" {
		t.Fatalf("enforced agent.mode missing: %#v", applied["agent"])
	}
	if applied["agent"].(map[string]any)["maxSteps"] != float64(5) {
		t.Fatalf("Apply dropped an unrelated key: %#v", applied["agent"])
	}
	// The caller's document is untouched.
	if candidate["shell"].(map[string]any)["enabled"] != true {
		t.Fatalf("Apply mutated its input: %#v", candidate)
	}
	if got := requirements.Enforced(); len(got) != 2 || got[0] != "agent.mode" || got[1] != "shell.enabled" {
		t.Fatalf("Enforced = %v, want sorted paths", got)
	}
}

func TestRequirementsRejectValuesOutsideTheAllowedSet(t *testing.T) {
	requirements, err := ParseRequirements("requirements (system)", map[string]any{
		"allowed": map[string]any{
			"agent.mode":     []any{"react", "plan_execute"},
			"model.provider": []any{"openai"},
		},
	})
	if err != nil {
		t.Fatalf("ParseRequirements returned error: %v", err)
	}
	ok, err := requirements.Apply(map[string]any{
		"agent": map[string]any{"mode": "plan_execute"},
		"model": map[string]any{"provider": "openai"},
	})
	if err != nil {
		t.Fatalf("allowed values were rejected: %v", err)
	}
	if ok["agent"].(map[string]any)["mode"] != "plan_execute" {
		t.Fatalf("Apply changed an allowed value: %#v", ok)
	}

	_, err = requirements.Apply(map[string]any{"agent": map[string]any{"mode": "oneshot"}})
	if err == nil {
		t.Fatal("disallowed value was accepted")
	}
	message := err.Error()
	for _, want := range []string{
		"invalid value for agent.mode",
		`"oneshot" is not in the allowed set ["react","plan_execute"]`,
		"set by requirements (system)",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not contain %q", message, want)
		}
	}

	// An absent key is not a violation: the layer simply did not set it.
	if _, err := requirements.Apply(map[string]any{"agent": map[string]any{"maxSteps": float64(1)}}); err != nil {
		t.Fatalf("absent key was rejected: %v", err)
	}
}

func TestParseRequirementsRejectsMalformedDocuments(t *testing.T) {
	if _, err := ParseRequirements("src", map[string]any{}); err == nil {
		t.Fatal("empty requirements were accepted")
	}
	if _, err := ParseRequirements("src", map[string]any{"allowed": []any{"nope"}}); err == nil {
		t.Fatal("non-object allowed section was accepted")
	}
	if _, err := ParseRequirements("src", map[string]any{"allowed": map[string]any{"agent.mode": []any{}}}); err == nil {
		t.Fatal("empty allowed list was accepted")
	}
	if _, err := ParseRequirements("src", map[string]any{"enforce": map[string]any{"agent.mode": nil}}); err == nil {
		t.Fatal("null enforced value was accepted")
	}
	// A scalar enforced over an object is only detectable once a
	// candidate document exists; see
	// TestRequirementsEnforcingAScalarOverAnObjectFails.
	if _, err := ParseRequirements("src", map[string]any{"enforce": map[string]any{"agent.mode": "react"}}); err != nil {
		t.Fatalf("valid enforce section was rejected: %v", err)
	}
}

func TestRequirementsEnforcingAScalarOverAnObjectFails(t *testing.T) {
	requirements, err := ParseRequirements("src", map[string]any{
		"enforce": map[string]any{"agent.mode.deep": "react"},
	})
	if err != nil {
		t.Fatalf("ParseRequirements returned error: %v", err)
	}
	_, err = requirements.Apply(map[string]any{"agent": map[string]any{"mode": "react"}})
	if err == nil || !strings.Contains(err.Error(), "enforce.agent.mode.deep") {
		t.Fatalf("path conflict error = %v", err)
	}
}

func TestUnknownFieldsReportsStrictConfigViolations(t *testing.T) {
	document := map[string]any{"agent": map[string]any{"maxStepz": float64(3)}}
	unknown, err := UnknownFields(document, &fakeConfig{})
	if err != nil {
		t.Fatalf("UnknownFields returned error: %v", err)
	}
	if len(unknown) != 1 || unknown[0] != "maxStepz" {
		t.Fatalf("unknown = %v, want [maxStepz]", unknown)
	}

	known, err := UnknownFields(map[string]any{"agent": map[string]any{"maxSteps": float64(3)}}, &fakeConfig{})
	if err != nil {
		t.Fatalf("UnknownFields returned error: %v", err)
	}
	if len(known) != 0 {
		t.Fatalf("known fields reported as unknown: %v", known)
	}
}

func TestGetSetDottedPaths(t *testing.T) {
	document := map[string]any{}
	if err := Set(document, "agent.prompt.variables", "x"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	if got, ok := Get(document, "agent.prompt.variables"); !ok || got != "x" {
		t.Fatalf("Get = %v %v", got, ok)
	}
	if _, ok := Get(document, "agent.missing.deep"); ok {
		t.Fatal("Get reported a missing path as present")
	}
	if err := Set(map[string]any{"agent": "scalar"}, "agent.mode", "react"); err == nil {
		t.Fatal("Set accepted a scalar parent")
	}
}
