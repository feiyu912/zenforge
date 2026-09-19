package dshapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A card that mounts for a route the host already describes inherits the host's
// own registry instead of a network call, which is the answer the composer and
// the picker already render.
func TestLlmDiscoverModelsAnswersFromTheHostCatalog(t *testing.T) {
	f := llmFixture(t, LlmDirectory{Live: []LlmProviderInfo{{ID: "openai", Name: "OpenAI"}}})
	f.handler.SetModelCatalog(func() ModelCatalog {
		return ModelCatalog{Groups: []ModelProviderGroup{{
			ID:   "acme",
			Name: "Acme Gateway",
			Models: []ModelCatalogModel{
				{ID: "acme-large", Name: "Acme Large"},
				{ID: "acme-think", Name: "acme-think"},
			},
		}}}
	})
	models := discoverModels(t, f, `{"settingsNs":"llm-pi-ai","request":{"provider":"acme"}}`)
	if len(models) != 2 || models[0].ID != "acme-large" || models[0].Name != "Acme Large" {
		t.Fatalf("models = %+v, want the declared profile's models", models)
	}
	if models[1].Name != "" {
		t.Fatalf("models[1].Name = %q, want it omitted: the catalog repeated the id", models[1].Name)
	}
	// A route the host can build an adapter for but publishes no model list has an
	// empty catalog, not a failure: the card lets the operator add models by hand.
	if empty := discoverModels(t, f, `{"settingsNs":"llm-openai","request":{"provider":"openai"}}`); len(empty) != 0 {
		t.Fatalf("models = %+v, want an empty catalog for a route with no model list", empty)
	}
}

func TestLlmDiscoverModelsRefusesWhatItCannotAnswerWithoutAnEndpoint(t *testing.T) {
	f := llmFixture(t, LlmDirectory{})
	// No endpoint and no provider: there is nothing to interrogate.
	refusal := discoverRefusal(t, f, `{"settingsNs":"llm-pi-ai","request":{}}`)
	if refusal.Code != codeModelDiscoveryRejected {
		t.Fatalf("code = %q, want %q", refusal.Code, codeModelDiscoveryRejected)
	}
	if refusal.Details["settingsNs"] != "llm-pi-ai" {
		t.Fatalf("details = %v, want the namespace echoed", refusal.Details)
	}
	// A route this host has never heard of is named rather than guessed at.
	refusal = discoverRefusal(t, f, `{"settingsNs":"llm-pi-ai","request":{"provider":"nope"}}`)
	if !strings.Contains(refusal.Message, `knows no route "nope"`) {
		t.Fatalf("message = %q, want the unknown route named", refusal.Message)
	}
}

// The fetch button sends the draft endpoint and credential directly, because a
// provider being added has no route to name yet.
func TestLlmDiscoverModelsInterrogatesAnEndpoint(t *testing.T) {
	var path, authorization, accept string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path, authorization, accept = request.URL.Path, request.Header.Get("authorization"), request.Header.Get("accept")
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[
			{"id":"acme-large","name":"Acme Large","context_length":128000},
			{"id":"acme-think","display_name":"Acme Think","max_output_tokens":8192},
			{"id":"","name":"an entry with no id"},
			{"id":"acme-large","name":"a duplicate"}
		]}`))
	}))
	defer server.Close()
	f := llmFixture(t, LlmDirectory{})
	models := discoverModels(t, f, fmt.Sprintf(
		`{"settingsNs":"llm-pi-ai","request":{"baseURL":%q,"api":"openai-completions","apiKey":"sk-draft"}}`,
		server.URL+"/v1"))
	if path != "/v1/models" {
		t.Fatalf("path = %q, want the model listing under the configured base URL", path)
	}
	if authorization != "Bearer sk-draft" {
		t.Fatalf("authorization = %q, want the draft credential", authorization)
	}
	if accept != "application/json" {
		t.Fatalf("accept = %q", accept)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want the identified, de-duplicated entries", models)
	}
	if models[0].ContextWindow == nil || *models[0].ContextWindow != 128000 {
		t.Fatalf("models[0] = %+v, want the disclosed context window", models[0])
	}
	if models[1].MaxTokens == nil || *models[1].MaxTokens != 8192 || models[1].Name != "Acme Think" {
		t.Fatalf("models[1] = %+v, want the disclosed output limit and display name", models[1])
	}
}

// An Anthropic-compatible endpoint authenticates with its own headers, and
// Ollama-shaped listings name the model with "name" rather than "id".
func TestLlmDiscoverModelsSpeaksTheOtherShapes(t *testing.T) {
	var apiKey, version string
	anthropic := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		apiKey, version = request.Header.Get("x-api-key"), request.Header.Get("anthropic-version")
		_, _ = writer.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"}]}`))
	}))
	defer anthropic.Close()
	f := llmFixture(t, LlmDirectory{})
	models := discoverModels(t, f, fmt.Sprintf(
		`{"settingsNs":"llm-anthropic","request":{"baseURL":%q,"apiKey":"sk-anthropic"}}`, anthropic.URL))
	if apiKey != "sk-anthropic" || version != "2023-06-01" {
		t.Fatalf("headers = (%q, %q), want the Anthropic credential and version", apiKey, version)
	}
	if len(models) != 1 || models[0].Name != "Claude Sonnet 4.5" {
		t.Fatalf("models = %+v, want the endpoint's display name", models)
	}

	ollama := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"models":[{"name":"llama3.1:8b"},{"model":"qwen2.5"}]}`))
	}))
	defer ollama.Close()
	models = discoverModels(t, f, fmt.Sprintf(
		`{"settingsNs":"llm-openai","request":{"baseURL":%q}}`, ollama.URL))
	if len(models) != 2 || models[0].ID != "llama3.1:8b" || models[1].ID != "qwen2.5" {
		t.Fatalf("models = %+v, want Ollama's names read as ids", models)
	}
}

// Every way an interrogation can fail says which part failed, and none of them
// reports an empty catalog: an endpoint that answered something else is not an
// endpoint with no models.
func TestLlmDiscoverModelsNamesEveryFailure(t *testing.T) {
	status := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer status.Close()
	html := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("<html>not a model list</html>"))
	}))
	defer html.Close()
	shapeless := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"object":"list"}`))
	}))
	defer shapeless.Close()
	f := llmFixture(t, LlmDirectory{})
	cases := []struct {
		name       string
		args       string
		wantInWord string
	}{
		{"a rejected credential", fmt.Sprintf(`{"settingsNs":"llm-pi-ai","request":{"baseURL":%q,"apiKey":"sk-draft"}}`, status.URL), "HTTP 401"},
		{"a page instead of an API", fmt.Sprintf(`{"settingsNs":"llm-pi-ai","request":{"baseURL":%q}}`, html.URL), "not a JSON object"},
		{"an answer with no list", fmt.Sprintf(`{"settingsNs":"llm-pi-ai","request":{"baseURL":%q}}`, shapeless.URL), "neither a"},
		{"a scheme the host will not speak", `{"settingsNs":"llm-pi-ai","request":{"baseURL":"ftp://example.test/v1"}}`, "http or https"},
		{"a URL with credentials in it", `{"settingsNs":"llm-pi-ai","request":{"baseURL":"https://user:pass@example.test/v1"}}`, "must not carry credentials"},
		{"a protocol this host does not speak", `{"settingsNs":"llm-pi-ai","request":{"baseURL":"https://example.test/v1","api":"gemini-generate"}}`, "not one this host speaks"},
		{"a namespace naming no protocol", `{"settingsNs":"ui-other","request":{"baseURL":"https://example.test/v1"}}`, "names no protocol"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			refusal := discoverRefusal(t, f, testCase.args)
			if refusal.Code != codeModelDiscoveryRejected {
				t.Fatalf("code = %q, want %q", refusal.Code, codeModelDiscoveryRejected)
			}
			if !strings.Contains(refusal.Message, testCase.wantInWord) {
				t.Fatalf("message = %q, want it to mention %q", refusal.Message, testCase.wantInWord)
			}
			if strings.Contains(refusal.Message, "sk-draft") {
				t.Fatalf("message = %q, want the credential kept out of diagnostics", refusal.Message)
			}
		})
	}
}

func TestLlmDiscoverModelsEnforcesItsArguments(t *testing.T) {
	f := llmFixture(t, LlmDirectory{})
	cases := []struct {
		name   string
		args   string
		detail string
	}{
		{"the namespace is missing", `{"request":{"provider":"openai"}}`, "settingsNs"},
		{"the request is missing", `{"settingsNs":"llm-pi-ai"}`, "request"},
		{"the request is not an object", `{"settingsNs":"llm-pi-ai","request":"baseURL"}`, "request"},
		{"a misspelled request field", `{"settingsNs":"llm-pi-ai","request":{"baseUrl":"https://example.test/v1"}}`, "request.baseUrl"},
		{"a non-string request field", `{"settingsNs":"llm-pi-ai","request":{"provider":7}}`, "request.provider"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := decodeResponse(t, f.post(t, "/api/llm/discoverModels",
				rpcBody(t, "l1", "llm/discoverModels", testCase.args)))
			if response.Result.OK || response.Result.Error.Code != codeArgumentsInvalid {
				t.Fatalf("error = %+v, want an argument refusal", response.Result.Error)
			}
			if response.Result.Error.Details["argument"] == nil {
				t.Fatalf("details = %v, want the offending argument named", response.Result.Error.Details)
			}
		})
	}
	// An unexpected argument is refused rather than ignored, like every other
	// method here.
	response := decodeResponse(t, f.post(t, "/api/llm/discoverModels",
		rpcBody(t, "l1", "llm/discoverModels", `{"settingsNs":"llm-pi-ai","request":{},"signal":"x"}`)))
	if response.Result.OK || response.Result.Error.Code != codeArgumentsInvalid {
		t.Fatalf("error = %+v, want the unexpected argument refused", response.Result.Error)
	}
}

// discoverModels posts the method and decodes the model list.
func discoverModels(t *testing.T, f *fixture, args string) []LlmDiscoveredModel {
	t.Helper()
	response := decodeResponse(t, f.post(t, "/api/llm/discoverModels", rpcBody(t, "l1", "llm/discoverModels", args)))
	if !response.Result.OK {
		t.Fatalf("result = %+v, want ok", response.Result.Error)
	}
	var models []LlmDiscoveredModel
	if err := json.Unmarshal(response.Result.Value, &models); err != nil {
		t.Fatalf("decode value %s: %v", response.Result.Value, err)
	}
	return models
}

// discoverRefusal posts the method and requires a refusal.
func discoverRefusal(t *testing.T, f *fixture, args string) *responseError {
	t.Helper()
	response := decodeResponse(t, f.post(t, "/api/llm/discoverModels", rpcBody(t, "l1", "llm/discoverModels", args)))
	if response.Result.OK {
		t.Fatalf("result = ok, want a refusal (value %s)", response.Result.Value)
	}
	return response.Result.Error
}
