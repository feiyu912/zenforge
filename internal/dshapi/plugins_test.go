package dshapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func pluginFixture(t *testing.T, snapshot PluginInventorySnapshot) *fixture {
	t.Helper()
	f := newFixture(t, Config{})
	f.handler.SetPluginInventory(func() PluginInventorySnapshot { return snapshot })
	return f
}

func testPluginSnapshot() PluginInventorySnapshot {
	return PluginInventorySnapshot{
		ManagementAvailable: false,
		Entries: []PluginInventoryEntry{
			{EntryID: "client/ui-commands", ModuleName: "@deepseek-ai/dsh-client-ui-commands", Enabled: true},
			{EntryID: "client/ui-directory-picker-native", ModuleName: "@deepseek-ai/dsh-client-ui-directory-picker-native", Enabled: false},
		},
	}
}

func TestPluginInventoryListReportsWhatTheHostPublishes(t *testing.T) {
	f := pluginFixture(t, testPluginSnapshot())
	recorder := f.post(t, "/api/pluginInventory/list", rpcBody(t, "s1", "pluginInventory/list", ""))
	var snapshot PluginInventorySnapshot
	decodeValue(t, recorder, &snapshot)
	if snapshot.ManagementAvailable {
		t.Fatal("managementAvailable = true, want false: this host has no loader")
	}
	if len(snapshot.Entries) != 2 || !snapshot.Entries[0].Enabled {
		t.Fatalf("entries = %+v, want the published inventory", snapshot.Entries)
	}
	if snapshot.Entries[1].Enabled {
		t.Fatal("the withheld module is reported enabled")
	}
	// fiberPhase is a client-side fact the host does not observe, so it is sent
	// as null rather than as a phase the host cannot see.
	body := recorder.Body.String()
	if !strings.Contains(body, `"fiberPhase":null`) {
		t.Fatalf("body = %s, want fiberPhase null", body)
	}
	// agentPresets is absent, not empty: this host's presets are not plugin
	// compositions, and an empty array would say they are.
	if strings.Contains(body, "agentPresets") {
		t.Fatalf("body = %s, want agentPresets omitted", body)
	}
}

func TestPluginInventoryAnswersAnEmptyArrayRatherThanNull(t *testing.T) {
	f := pluginFixture(t, PluginInventorySnapshot{})
	recorder := f.post(t, "/api/pluginInventory/list", rpcBody(t, "s1", "pluginInventory/list", ""))
	var body struct {
		Result struct {
			Value struct {
				Entries json.RawMessage `json:"entries"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(body.Result.Value.Entries) != "[]" {
		t.Fatalf("entries = %s, want []", body.Result.Value.Entries)
	}
}

func TestPluginInventoryWithoutASourceAnswersUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	response := decodeResponse(t, f.post(t, "/api/pluginInventory/list",
		rpcBody(t, "s1", "pluginInventory/list", "")))
	if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
		t.Fatalf("code = %q, want unimplemented", response.Result.Error.Code)
	}
	if response.Result.Error.Details["dependency"] != "PluginInventorySource" {
		t.Fatalf("details = %v, want the dependency named", response.Result.Error.Details)
	}
}

// Every plugin-manager write is refused with the same reason, and the details
// repeat what the inventory says so a caller that skipped it still learns why.
func TestPluginManagerWritesAreRefusedWithTheReason(t *testing.T) {
	f := pluginFixture(t, testPluginSnapshot())
	methods := []string{
		"pluginManager/listBundles", "pluginManager/listPlugins", "pluginManager/inspect",
		"pluginManager/installBundle", "pluginManager/removeBundle",
		"pluginManager/setBundleEnabled", "pluginManager/setPluginEnabled",
		"pluginManager/cancelInstall",
	}
	for _, method := range methods {
		response := decodeResponse(t, f.post(t, "/api/"+method, rpcBody(t, "s1", method, `{}`)))
		if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want unimplemented", method, response.Result.Error.Code)
		}
		if response.Result.Error.Details["capability"] != "host plugin management" {
			t.Fatalf("%s: details = %v, want the capability named", method, response.Result.Error.Details)
		}
		if response.Result.Error.Details["managementAvailable"] != false {
			t.Fatalf("%s: details = %v, want managementAvailable false", method, response.Result.Error.Details)
		}
	}
}
