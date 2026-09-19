package dshapi

import (
	"encoding/json"
	"testing"
)

// llmFixture installs a directory on a fresh handler.
func llmFixture(t *testing.T, directory LlmDirectory) *fixture {
	t.Helper()
	f := newFixture(t, Config{})
	source := directory
	f.handler.SetLlmDirectory(func() LlmDirectory { return source })
	return f
}

func TestLlmListProvidersAnswersTheLiveDirectory(t *testing.T) {
	f := llmFixture(t, LlmDirectory{Live: []LlmProviderInfo{{ID: "openai", Name: "OpenAI"}}})
	response := decodeResponse(t, f.post(t, "/api/llm/listProviders",
		rpcBody(t, "l1", "llm/listProviders", "")))
	if !response.Result.OK {
		t.Fatalf("result = %+v, want ok", response.Result.Error)
	}
	var providers []LlmProviderInfo
	if err := json.Unmarshal(response.Result.Value, &providers); err != nil {
		t.Fatalf("decode value %s: %v", response.Result.Value, err)
	}
	if len(providers) != 1 || providers[0].ID != "openai" || providers[0].Name != "OpenAI" {
		t.Fatalf("providers = %+v, want the configured route with its display name", providers)
	}
}

// The page reads both halves as arrays; null would fail its decode where an
// empty array renders an empty directory.
func TestLlmDirectoryArraysAreNeverNull(t *testing.T) {
	f := llmFixture(t, LlmDirectory{})
	cases := []struct {
		endpoint string
		method   string
	}{
		{"/api/llm/listProviders", "llm/listProviders"},
		{"/api/llm/listConfigurableProviders", "llm/listConfigurableProviders"},
	}
	for _, testCase := range cases {
		response := decodeResponse(t, f.post(t, testCase.endpoint, rpcBody(t, "l1", testCase.method, "")))
		if !response.Result.OK {
			t.Fatalf("%s: result = %+v, want ok", testCase.method, response.Result.Error)
		}
		if string(response.Result.Value) != "[]" {
			t.Fatalf("%s: value = %s, want an empty array rather than null", testCase.method, response.Result.Value)
		}
	}
}

func TestLlmListConfigurableProvidersDescribesEveryRoute(t *testing.T) {
	f := llmFixture(t, LlmDirectory{Configurable: []LlmConfigurableProvider{
		{Provider: "openai", DisplayName: "OpenAI", SettingsNS: "llm-openai"},
		{Provider: "anthropic", DisplayName: "Anthropic", SettingsNS: "llm-anthropic", SettingsPath: []string{"providers", "anthropic"}},
	}})
	response := decodeResponse(t, f.post(t, "/api/llm/listConfigurableProviders",
		rpcBody(t, "l1", "llm/listConfigurableProviders", "")))
	if !response.Result.OK {
		t.Fatalf("result = %+v, want ok", response.Result.Error)
	}
	var providers []LlmConfigurableProvider
	if err := json.Unmarshal(response.Result.Value, &providers); err != nil {
		t.Fatalf("decode value %s: %v", response.Result.Value, err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %+v, want both routes", providers)
	}
	// settingsPath is required on the wire; an empty path must be [] rather than
	// null, and a declared path must survive untouched.
	if providers[0].SettingsPath == nil || len(providers[0].SettingsPath) != 0 {
		t.Fatalf("settingsPath = %v, want an empty array", providers[0].SettingsPath)
	}
	if len(providers[1].SettingsPath) != 2 || providers[1].SettingsPath[1] != "anthropic" {
		t.Fatalf("settingsPath = %v, want the declared path kept", providers[1].SettingsPath)
	}
}

func TestLlmMethodsRejectUnexpectedArguments(t *testing.T) {
	f := llmFixture(t, LlmDirectory{})
	for _, method := range []string{"llm/listProviders", "llm/listConfigurableProviders"} {
		response := decodeResponse(t, f.post(t, "/api/"+method, rpcBody(t, "l1", method, `{"unexpected":1}`)))
		if response.Result.OK {
			t.Fatalf("%s: result = ok, want the extra argument refused", method)
		}
		if response.Result.Error.Code != codeArgumentsInvalid {
			t.Fatalf("%s: code = %q, want %q", method, response.Result.Error.Code, codeArgumentsInvalid)
		}
	}
}

func TestLlmWithoutADirectoryAnswersUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	for _, method := range []string{"llm/listProviders", "llm/listConfigurableProviders"} {
		response := decodeResponse(t, f.post(t, "/api/"+method, rpcBody(t, "l1", method, "")))
		if response.Result.OK {
			t.Fatalf("%s: result = ok, want unimplemented", method)
		}
		if response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want %q", method, response.Result.Error.Code, codeUnimplemented)
		}
		if response.Result.Error.Details["dependency"] != "LlmDirectorySource" {
			t.Fatalf("%s: details = %v, want the missing dependency named", method, response.Result.Error.Details)
		}
	}
}

// Discovery is registered and honestly unsupported: a named capability gap the
// page can show, instead of a transport failure that reads like a broken host.
func TestLlmDiscoverModelsReportsTheUnsupportedCapability(t *testing.T) {
	f := llmFixture(t, LlmDirectory{})
	response := decodeResponse(t, f.post(t, "/api/llm/discoverModels",
		rpcBody(t, "l1", "llm/discoverModels", `{"settingsNs":"llm-openai","request":{"baseURL":"https://example.test/v1"}}`)))
	if response.Result.OK {
		t.Fatal("result = ok, want the capability gap reported")
	}
	if response.Result.Error.Code != codeUnimplemented {
		t.Fatalf("code = %q, want %q", response.Result.Error.Code, codeUnimplemented)
	}
	if response.Result.Error.Details["capability"] != "model discovery" {
		t.Fatalf("details = %v, want the capability named", response.Result.Error.Details)
	}
}
