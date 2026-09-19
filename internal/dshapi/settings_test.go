package dshapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// stubSettings is the injected settings face the tests drive. It records every
// write so a test can prove the field arrived, and it never holds a value it
// would report back: SettingsProfile has no field for one.
type stubSettings struct {
	profile   SettingsProfile
	endpoints []string
	models    []string
	keys      []string
	cleared   int
	fail      error
}

func (s *stubSettings) SettingsProfile() SettingsProfile { return s.profile }

func (s *stubSettings) SetSettingsEndpoint(baseURL string) error {
	if s.fail != nil {
		return s.fail
	}
	s.endpoints = append(s.endpoints, baseURL)
	s.profile.BaseURL = baseURL
	return nil
}

func (s *stubSettings) SetSettingsModel(model string) error {
	if s.fail != nil {
		return s.fail
	}
	s.models = append(s.models, model)
	s.profile.Model = model
	return nil
}

func (s *stubSettings) SetSettingsKey(value string) error {
	if s.fail != nil {
		return s.fail
	}
	s.keys = append(s.keys, value)
	s.profile.HasKey = true
	return nil
}

func (s *stubSettings) ClearSettingsKey() error {
	if s.fail != nil {
		return s.fail
	}
	s.cleared++
	s.profile.HasKey = false
	return nil
}

func settingsFixture(t *testing.T) (*fixture, *stubSettings) {
	t.Helper()
	f := newFixture(t, Config{})
	store := &stubSettings{profile: SettingsProfile{
		Provider: "openai",
		Model:    "qwen-plus",
		BaseURL:  "https://dashscope.aliyuncs.com/compatible-mode/v1",
		HasKey:   true,
	}}
	f.handler.SetSettingsDocument(store)
	return f, store
}

func TestSettingsDescribeCarriesTheSchemaAndNoSecret(t *testing.T) {
	f, _ := settingsFixture(t)
	recorder := f.post(t, "/api/settings/describe", rpcBody(t, "s1", "settings/describe", ""))
	if recorder.Code != 200 {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var value SettingsDescribeValue
	decodeValue(t, recorder, &value)
	if !value.Writable || value.HasDocument {
		t.Fatalf("writable = %v hasDocument = %v, want true and false", value.Writable, value.HasDocument)
	}
	if len(value.Namespaces) != len(consoleProviderRoutes) {
		t.Fatalf("namespaces = %d, want one per route", len(value.Namespaces))
	}
	view := value.Namespaces[0]
	if view.NS != "llm-openai" {
		t.Fatalf("ns = %q, want llm-openai", view.NS)
	}
	if view.Applies != "live" || view.Revision != settingsRevision {
		t.Fatalf("applies = %q revision = %d, want live and %d", view.Applies, view.Revision, settingsRevision)
	}
	if len(view.Secrets) != 1 || !view.Secrets[0].Set {
		t.Fatalf("secrets = %+v, want the apiKey slot reported as set", view.Secrets)
	}
	wantPath := "providers.openai.api.apiKey"
	if strings.Join(view.Secrets[0].Path, ".") != wantPath {
		t.Fatalf("secret path = %v, want %s", view.Secrets[0].Path, wantPath)
	}
	// The value carries the configuration and never the credential.
	encoded, err := json.Marshal(view.Value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if strings.Contains(string(encoded), "apiKey") {
		t.Fatalf("value mentions apiKey: %s", encoded)
	}
	if !strings.Contains(string(encoded), "qwen-plus") {
		t.Fatalf("value does not carry the model: %s", encoded)
	}
}

// The page resolves nodeAtPath(schema, ['providers', <probe>, 'api']) before it
// renders a card, so the envelope this host serves must contain that path: a
// providers dict whose inner object declares the api fields.
func TestSettingsSchemaDeclaresTheProbePath(t *testing.T) {
	f, _ := settingsFixture(t)
	var value SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s1", "settings/describe", "")), &value)
	var envelope struct {
		UID  int                        `json:"uid"`
		Refs map[string]json.RawMessage `json:"refs"`
	}
	if err := json.Unmarshal(value.Namespaces[0].Schema, &envelope); err != nil {
		t.Fatalf("schema is not a schemastery envelope: %v", err)
	}
	root, ok := envelope.Refs[fmt.Sprint(envelope.UID)]
	if !ok {
		t.Fatalf("schema has no ref for its uid %d", envelope.UID)
	}
	providersRef, failure := schemaChild(root, "dict", "providers")
	if failure != "" {
		t.Fatal(failure)
	}
	innerRef, failure := schemaField(envelope.Refs, providersRef, "inner")
	if failure != "" {
		t.Fatalf("providers must be a dict with an inner schema: %s", failure)
	}
	apiRef, failure := schemaChild(envelope.Refs[innerRef], "dict", "api")
	if failure != "" {
		t.Fatalf("the provider profile must declare an api object: %s", failure)
	}
	fields := map[string]bool{}
	for _, name := range []string{"baseURL", "apiKey", "model"} {
		if _, failure := schemaChild(envelope.Refs[apiRef], "dict", name); failure != "" {
			t.Fatalf("api.%s is missing: %s", name, failure)
		}
		fields[name] = true
	}
	if len(fields) != 3 {
		t.Fatalf("fields = %v, want baseURL, apiKey and model", fields)
	}
}

// schemaChild reads one child reference out of a schema node's named map.
func schemaChild(raw json.RawMessage, container, key string) (string, string) {
	node := map[string]any{}
	if err := json.Unmarshal(raw, &node); err != nil {
		return "", "schema node is not an object: " + err.Error()
	}
	children, ok := node[container].(map[string]any)
	if !ok {
		return "", "schema node has no " + container
	}
	value, ok := children[key]
	if !ok {
		return "", "schema node " + container + " has no " + key
	}
	return fmt.Sprint(value), ""
}

// schemaField resolves a reference number and reads one top-level field from it,
// which is where a dict node keeps the reference to its inner schema.
func schemaField(refs map[string]json.RawMessage, uid, key string) (string, string) {
	raw, ok := refs[uid]
	if !ok {
		return "", "no ref " + uid
	}
	node := map[string]any{}
	if err := json.Unmarshal(raw, &node); err != nil {
		return "", "schema node is not an object: " + err.Error()
	}
	value, ok := node[key]
	if !ok {
		return "", "schema node has no " + key
	}
	return fmt.Sprint(value), ""
}

func TestSettingsMutateAppliesTheFieldsItCanHold(t *testing.T) {
	f, store := settingsFixture(t)
	const secret = "sk-settings-sentinel-31ac"
	body := fmt.Sprintf(`{"ns":"llm-openai","expectedRevision":%d,"ops":[
		{"op":"set","path":["providers","openai","api","baseURL"],"value":"https://example.test/v1"},
		{"op":"set","path":["providers","openai","api","model"],"value":"qwen-max"},
		{"op":"set","path":["providers","openai","api","apiKey"],"value":%q}]}`,
		settingsRevision, secret)
	recorder := f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", body))
	if envelope := decodeResponse(t, recorder); !envelope.Result.OK {
		t.Fatalf("mutate failed: %s", recorder.Body.String())
	}
	if len(store.endpoints) != 1 || store.endpoints[0] != "https://example.test/v1" {
		t.Fatalf("endpoints = %q, want the written endpoint", store.endpoints)
	}
	if len(store.models) != 1 || store.models[0] != "qwen-max" {
		t.Fatalf("models = %q, want the written model", store.models)
	}
	if len(store.keys) != 1 || store.keys[0] != secret {
		t.Fatalf("keys = %q, want the written credential", store.keys)
	}
	// The reply is a namespace view, so it must carry the new configuration and
	// never the credential that was just written.
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("the credential came back in the reply: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "qwen-max") {
		t.Fatalf("the reply does not show the written model: %s", recorder.Body.String())
	}
}

func TestSettingsWritesAreValidated(t *testing.T) {
	cases := []struct {
		name string
		args string
		code string
	}{
		{"an unknown namespace", `{"ns":"llm-not-a-route","ops":[{"op":"set","path":["providers","x","api","model"],"value":"m"}]}`, codeBadRequest},
		{"a mismatched revision", fmt.Sprintf(`{"ns":"llm-openai","expectedRevision":%d,"ops":[{"op":"set","path":["providers","openai","api","model"],"value":"m"}]}`, settingsRevision+1), codeSessionConflict},
		{"an unknown op", `{"ns":"llm-openai","ops":[{"op":"delete","path":["providers","openai","api","model"]}]}`, codeBadRequest},
		{"a path this host does not store", `{"ns":"llm-openai","ops":[{"op":"set","path":["telemetry","endpoint"],"value":"x"}]}`, codeUnimplemented},
		{"another provider's profile", `{"ns":"llm-openai","ops":[{"op":"set","path":["providers","anthropic","api","model"],"value":"m"}]}`, codeUnimplemented},
		{"a non-string value", `{"ns":"llm-openai","ops":[{"op":"set","path":["providers","openai","api","model"],"value":42}]}`, codeBadRequest},
		{"clearing the model", `{"ns":"llm-openai","ops":[{"op":"unset","path":["providers","openai","api","model"]}]}`, codeUnimplemented},
		{"a missing ops array", `{"ns":"llm-openai"}`, codeArgumentsInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f, store := settingsFixture(t)
			response := decodeResponse(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", testCase.args)))
			if response.Result.OK {
				t.Fatalf("result = ok, want %s", testCase.code)
			}
			if response.Result.Error.Code != testCase.code {
				t.Fatalf("code = %q, want %q (%s)", response.Result.Error.Code, testCase.code, response.Result.Error.Message)
			}
			if len(store.endpoints)+len(store.models)+len(store.keys)+store.cleared != 0 {
				t.Fatalf("a refused write reached the store: %+v", store)
			}
		})
	}
}

func TestSettingsUnsetClearsTheCredential(t *testing.T) {
	f, store := settingsFixture(t)
	recorder := f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate",
		`{"ns":"llm-openai","ops":[{"op":"unset","path":["providers","openai","api","apiKey"]}]}`))
	var view SettingsNamespaceView
	decodeValue(t, recorder, &view)
	if store.cleared != 1 {
		t.Fatalf("cleared = %d, want the credential removed", store.cleared)
	}
	if len(view.Secrets) != 1 || view.Secrets[0].Set {
		t.Fatalf("secrets = %+v, want the slot reported as unset", view.Secrets)
	}
}

// The page's other two write shapes carry a section rather than ops; both must
// reach the same field writes, and a section naming a profile this host does not
// hold is refused rather than silently dropped.
func TestSettingsUpdateAndReplaceApplyASection(t *testing.T) {
	for _, method := range []string{"settings/update", "settings/replace"} {
		t.Run(method, func(t *testing.T) {
			f, store := settingsFixture(t)
			key := "patch"
			if method == "settings/replace" {
				key = "section"
			}
			body := fmt.Sprintf(`{"ns":"llm-openai","%s":{"providers":{"openai":{"api":{"baseURL":"https://example.test/v1","model":"qwen-turbo"}}}}}`,
				key)
			response := decodeResponse(t, f.post(t, "/api/"+method, rpcBody(t, "s1", method, body)))
			if !response.Result.OK {
				t.Fatalf("%s failed: %+v", method, response.Result.Error)
			}
			if len(store.endpoints) != 1 || len(store.models) != 1 {
				t.Fatalf("edits = %q %q, want the section applied", store.endpoints, store.models)
			}
		})
	}
}

func TestSettingsWithoutAStoreAnswerUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct{ endpoint, method, args string }{
		{"/api/settings/describe", "settings/describe", ""},
		{"/api/settings/mutate", "settings/mutate", `{"ns":"llm-openai","ops":[]}`},
		{"/api/settings/update", "settings/update", `{"ns":"llm-openai","patch":{}}`},
		{"/api/settings/replace", "settings/replace", `{"ns":"llm-openai","section":{}}`},
	}
	for _, testCase := range cases {
		response := decodeResponse(t, f.post(t, testCase.endpoint, rpcBody(t, "s1", testCase.method, testCase.args)))
		if response.Result.OK {
			t.Fatalf("%s: result = ok, want unimplemented", testCase.method)
		}
		if response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want %q", testCase.method, response.Result.Error.Code, codeUnimplemented)
		}
		if response.Result.Error.Details["dependency"] != "SettingsDocumentStore" {
			t.Fatalf("%s: details = %v, want the missing dependency named", testCase.method, response.Result.Error.Details)
		}
	}
}

func TestSettingsNativeOpenIsAnHonestGap(t *testing.T) {
	f, _ := settingsFixture(t)
	if response := decodeResponse(t, f.post(t, "/api/settings/canOpenAgentPresetDirectory",
		rpcBody(t, "s1", "settings/canOpenAgentPresetDirectory", ""))); !response.Result.OK ||
		string(response.Result.Value) != "false" {
		t.Fatalf("canOpen = %s (%+v), want false", response.Result.Value, response.Result.Error)
	}
	for _, method := range []string{"settings/openSettingsDocument", "settings/openAgentPresetDirectory"} {
		response := decodeResponse(t, f.post(t, "/api/"+method, rpcBody(t, "s1", method, "")))
		if response.Result.OK || response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: result = %s, want a named unimplemented gap", method, response.Result.Value)
		}
		if response.Result.Error.Details["capability"] != "native editor" {
			t.Fatalf("%s: details = %v, want the capability named", method, response.Result.Error.Details)
		}
	}
}

func TestSettingsWriteFailureIsReportedWithoutTheSecret(t *testing.T) {
	const secret = "sk-settings-refused-9d21"
	f := newFixture(t, Config{})
	f.handler.SetSettingsDocument(&stubSettings{
		profile: SettingsProfile{Provider: "openai"},
		fail:    fmt.Errorf("provider rejected %s", secret),
	})
	body := fmt.Sprintf(`{"ns":"llm-openai","ops":[{"op":"set","path":["providers","openai","api","apiKey"],"value":%q}]}`, secret)
	response := decodeResponse(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", body)))
	if response.Result.OK {
		t.Fatal("result = ok, want the refusal reported")
	}
	if strings.Contains(response.Result.Error.Message, secret) {
		t.Fatalf("message = %q, want the secret redacted", response.Result.Error.Message)
	}
	if !strings.Contains(response.Result.Error.Message, "[redacted]") {
		t.Fatalf("message = %q, want the redaction marked", response.Result.Error.Message)
	}
}
