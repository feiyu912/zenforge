package dshapi

import (
	"strings"
	"testing"
)

func presetFixture(t *testing.T, presets ConsolePresets) *fixture {
	t.Helper()
	f := newFixture(t, Config{})
	f.handler.SetPresets(func() ConsolePresets { return presets })
	return f
}

func testPresets() ConsolePresets {
	return ConsolePresets{
		Sandbox:     "none",
		Approval:    "never",
		DefaultMode: "react",
		Modes: []ConsolePresetMode{
			{ID: "react", Name: "react", Description: "the model/tool loop"},
			{ID: "oneshot", Name: "oneshot", Description: "one final answer"},
			{ID: "plan_execute", Name: "plan_execute", Description: "plan, then execute"},
		},
	}
}

// The catalog names the host's combination when upstream has a name for it and
// says so plainly when it does not, rather than reporting a plausible preset the
// host is not running.
func TestPermissionPresetsCatalogNamesTheHostsOwnCombination(t *testing.T) {
	cases := []struct {
		name     string
		sandbox  string
		approval string
		want     string
	}{
		{"unconfined without approvals follows upstream's name", "none", "never", "danger-full-access"},
		{"a confined sandbox that asks follows upstream's name", "seatbelt", "prompt", "workspace-write"},
		{"a docker sandbox that always asks follows upstream's name", "docker", "always", "workspace-write"},
		{"unconfined but asking is this host's own combination", "none", "prompt", "custom"},
		{"confined without approvals is this host's own combination", "bwrap", "never", "custom"},
		{"an unset sandbox is reported as none", "", "never", "danger-full-access"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := presetFixture(t, ConsolePresets{Sandbox: testCase.sandbox, Approval: testCase.approval})
			var catalog PermissionCatalog
			decodeValue(t, f.post(t, "/api/permissionPresets/catalog",
				rpcBody(t, "s1", "permissionPresets/catalog", "")), &catalog)
			if len(catalog.Options) != 1 {
				t.Fatalf("options = %+v, want exactly one: this host fixes its policy", catalog.Options)
			}
			if catalog.Options[0].Value != testCase.want {
				t.Fatalf("value = %q, want %q", catalog.Options[0].Value, testCase.want)
			}
			if catalog.Options[0].Description == "" {
				t.Fatal("description is empty, want the settings the name was derived from")
			}
			// The description must state the actual settings, so an operator can
			// see why no named preset matched.
			sandbox := testCase.sandbox
			if sandbox == "" {
				sandbox = "none"
			}
			if !strings.Contains(catalog.Options[0].Description, "--sandbox "+sandbox) {
				t.Fatalf("description = %q, want it to name --sandbox %s", catalog.Options[0].Description, sandbox)
			}
		})
	}
}

func TestAgentPresetsRosterIsHonestAboutWhatTheHostCannotDo(t *testing.T) {
	f := presetFixture(t, testPresets())
	var roster AgentPresetRoster
	recorder := f.post(t, "/api/agentPresets/list", rpcBody(t, "s1", "agentPresets/list", ""))
	decodeValue(t, recorder, &roster)
	if roster.Authorable {
		t.Fatal("authorable = true, want false: this host has no preset directory")
	}
	if roster.ModeSelectionEnabled {
		t.Fatal("modeSelectionEnabled = true, want false: the host's preset is fixed at startup")
	}
	if len(roster.Presets) != len(testPresets().Modes) {
		t.Fatalf("presets = %d, want %d", len(roster.Presets), len(testPresets().Modes))
	}
	defaults := 0
	for _, row := range roster.Presets {
		if row.Trust != "system" {
			t.Fatalf("preset %s trust = %q, want system: every preset here is built in", row.ID, row.Trust)
		}
		if row.IsDefault {
			defaults++
			if row.ID != "react" {
				t.Fatalf("default preset = %s, want react", row.ID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("defaults = %d, want exactly one", defaults)
	}
	// A compiled-in preset cannot be broken, so the field is never sent.
	if strings.Contains(recorder.Body.String(), "broken") {
		t.Fatalf("roster mentions broken: %s", recorder.Body.String())
	}
}

func TestAgentPresetsReadSaysThereIsNoFile(t *testing.T) {
	f := presetFixture(t, testPresets())
	var document AgentPresetDocument
	decodeValue(t, f.post(t, "/api/agentPresets/read",
		rpcBody(t, "s1", "agentPresets/read", `{"agentPreset":"plan_execute"}`)), &document)
	if document.AgentPreset != "plan_execute" || document.Trust != "system" {
		t.Fatalf("document = %+v, want the plan_execute system preset", document)
	}
	if !strings.Contains(document.Content, "no preset file") {
		t.Fatalf("content = %q, want it to state there is no file behind the preset", document.Content)
	}
	if document.Description == "" {
		t.Fatal("description is empty, want the mode's own wording")
	}
}

func TestAgentPresetsUnknownIDIsNotFound(t *testing.T) {
	f := presetFixture(t, testPresets())
	response := decodeResponse(t, f.post(t, "/api/agentPresets/read",
		rpcBody(t, "s1", "agentPresets/read", `{"agentPreset":"nope"}`)))
	if response.Result.OK {
		t.Fatal("result = ok, want not-found")
	}
	if response.Result.Error.Code != "agent-preset/not-found" {
		t.Fatalf("code = %q, want upstream's agent-preset/not-found", response.Result.Error.Code)
	}
	available, ok := response.Result.Error.Details["available"].([]any)
	if !ok || len(available) != 3 {
		t.Fatalf("details = %v, want the available ids listed", response.Result.Error.Details)
	}
}

// Authoring and switching are refused with upstream's own codes, so a console
// that understands "this roster is read-only" shows that rather than a failure
// it cannot interpret.
func TestAgentPresetWritesAreRefusedHonestly(t *testing.T) {
	f := presetFixture(t, testPresets())
	for _, method := range []string{"agentPresets/copy", "agentPresets/deletePreset"} {
		response := decodeResponse(t, f.post(t, "/api/"+method,
			rpcBody(t, "s1", method, `{"from":"react","id":"mine"}`)))
		if response.Result.OK || response.Result.Error.Code != "agent-preset/read-only" {
			t.Fatalf("%s: code = %q, want agent-preset/read-only", method, response.Result.Error.Code)
		}
		if response.Result.Error.Details["reason"] == nil {
			t.Fatalf("%s: details = %v, want the reason named", method, response.Result.Error.Details)
		}
	}
	response := decodeResponse(t, f.post(t, "/api/agentPresets/select",
		rpcBody(t, "s1", "agentPresets/select", `{"agentId":"s","agentPreset":"oneshot"}`)))
	if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
		t.Fatalf("select: code = %q, want unimplemented", response.Result.Error.Code)
	}
	if response.Result.Error.Details["capability"] != "per-session agent preset selection" {
		t.Fatalf("select: details = %v, want the capability named", response.Result.Error.Details)
	}
}

func TestPresetSurfacesWithoutASourceAnswerUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct{ method, args string }{
		{"permissionPresets/catalog", ""},
		{"agentPresets/list", ""},
		{"agentPresets/read", `{"agentPreset":"react"}`},
	}
	for _, testCase := range cases {
		response := decodeResponse(t, f.post(t, "/api/"+testCase.method, rpcBody(t, "s1", testCase.method, testCase.args)))
		if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want unimplemented", testCase.method, response.Result.Error.Code)
		}
		if response.Result.Error.Details["dependency"] != "PresetSource" {
			t.Fatalf("%s: details = %v, want the missing dependency named", testCase.method, response.Result.Error.Details)
		}
	}
}
