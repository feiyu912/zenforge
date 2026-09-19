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
	// sections holds the namespaces the console owns (ADR 0094).
	sections map[string]map[string]any
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

func (s *stubSettings) ConsoleSection(namespace string) map[string]any {
	section := make(map[string]any, len(s.sections[namespace]))
	for key, value := range s.sections[namespace] {
		section[key] = value
	}
	return section
}

func (s *stubSettings) SetConsoleSection(namespace string, section map[string]any) error {
	if s.fail != nil {
		return s.fail
	}
	if s.sections == nil {
		s.sections = make(map[string]map[string]any)
	}
	stored := make(map[string]any, len(section))
	for key, value := range section {
		stored[key] = value
	}
	s.sections[namespace] = stored
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
	// One view per provider route, plus the namespace the console owns (ADR
	// 0094), so the count moving is deliberate rather than a route going missing.
	if len(value.Namespaces) != len(consoleProviderRoutes)+1 {
		t.Fatalf("namespaces = %d, want one per route plus the console's own", len(value.Namespaces))
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
		{"a mismatched revision", fmt.Sprintf(`{"ns":"llm-openai","expectedRevision":%d,"ops":[{"op":"set","path":["providers","openai","api","model"],"value":"m"}]}`, settingsRevision+1), codeSettingsConflict},
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

// sectionOf reads a namespace view's value as the section object it is on the
// wire (SettingsNamespaceView.Value is `any` because a section is open-shaped).
func sectionOf(t *testing.T, view SettingsNamespaceView) map[string]any {
	t.Helper()
	section, ok := view.Value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want a section object", view.Value)
	}
	return section
}

// The console's "Internal Testing Notice" is acknowledged through the settings
// wire, into a namespace the host holds for the console rather than for its own
// configuration. Without it a loopback browser can only fail the write, which is
// what the Continue button did.
func TestSettingsDescribeCarriesTheConsoleNamespace(t *testing.T) {
	f, _ := settingsFixture(t)
	var described SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s1", "settings/describe", "")), &described)
	var onboarding *SettingsNamespaceView
	for index := range described.Namespaces {
		if described.Namespaces[index].NS == "ui-onboarding" {
			onboarding = &described.Namespaces[index]
		}
	}
	if onboarding == nil {
		t.Fatalf("namespaces = %+v, want ui-onboarding beside the provider namespaces", described.Namespaces)
	}
	// The console refuses a namespace whose schemastery envelope it cannot
	// rehydrate, so the envelope has to declare the field and leave it optional.
	schema := string(onboarding.Schema)
	if !strings.Contains(schema, "welcomeNoticeVersion") {
		t.Fatalf("schema = %s, want the acknowledged field declared", schema)
	}
	if !strings.Contains(schema, `"uid"`) {
		t.Fatalf("schema = %s, want a schemastery envelope", schema)
	}
	if len(sectionOf(t, *onboarding)) != 0 {
		t.Fatalf("value = %v, want an empty section before anything is acknowledged", onboarding.Value)
	}
	if onboarding.Applies != "live" || len(onboarding.Secrets) != 0 {
		t.Fatalf("view = %+v, want a live namespace with no secret slot", onboarding)
	}
}

func TestSettingsMutateStoresTheWelcomeAcknowledgement(t *testing.T) {
	f, store := settingsFixture(t)
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate",
		`{"ns":"ui-onboarding","ops":[{"op":"set","path":["welcomeNoticeVersion"],"value":"2026-08-13.1"}]}`)), &view)
	if view.NS != "ui-onboarding" || sectionOf(t, view)["welcomeNoticeVersion"] != "2026-08-13.1" {
		t.Fatalf("view = %+v, want the acknowledgement stored and returned", view)
	}
	if store.sections["ui-onboarding"]["welcomeNoticeVersion"] != "2026-08-13.1" {
		t.Fatalf("stored sections = %v, want the field in the console's namespace", store.sections)
	}
	// The read path the notice decides from must see it too, otherwise the page
	// would write successfully and still show the notice.
	var described SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s2", "settings/describe", "")), &described)
	for _, namespace := range described.Namespaces {
		if namespace.NS == "ui-onboarding" && sectionOf(t, namespace)["welcomeNoticeVersion"] != "2026-08-13.1" {
			t.Fatalf("describe value = %v, want the stored acknowledgement", namespace.Value)
		}
	}
}

func TestSettingsMutateUnsetsTheWelcomeAcknowledgement(t *testing.T) {
	f, store := settingsFixture(t)
	store.sections = map[string]map[string]any{"ui-onboarding": {"welcomeNoticeVersion": "2026-08-13.1"}}
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate",
		`{"ns":"ui-onboarding","ops":[{"op":"unset","path":["welcomeNoticeVersion"]}]}`)), &view)
	if len(sectionOf(t, view)) != 0 {
		t.Fatalf("value = %v, want the field cleared", view.Value)
	}
}

func TestSettingsUpdateAndReplaceTheConsoleSection(t *testing.T) {
	f, store := settingsFixture(t)
	store.sections = map[string]map[string]any{"ui-onboarding": {"welcomeNoticeVersion": "old"}}
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/update", rpcBody(t, "s1", "settings/update",
		`{"ns":"ui-onboarding","patch":{"welcomeNoticeVersion":"2026-08-13.1"}}`)), &view)
	if sectionOf(t, view)["welcomeNoticeVersion"] != "2026-08-13.1" {
		t.Fatalf("update value = %v, want the new version", view.Value)
	}
	decodeValue(t, f.post(t, "/api/settings/replace", rpcBody(t, "s2", "settings/replace",
		`{"ns":"ui-onboarding","section":{}}`)), &view)
	if len(sectionOf(t, view)) != 0 {
		t.Fatalf("replace value = %v, want the section swapped for the empty one", view.Value)
	}
}

func TestSettingsConsoleNamespaceRefusesWhatItDoesNotDeclare(t *testing.T) {
	f, store := settingsFixture(t)
	cases := []struct {
		name   string
		method string
		body   string
		code   string
	}{
		{"an undeclared path", "mutate", `{"ns":"ui-onboarding","ops":[{"op":"set","path":["theme"],"value":"dark"}]}`, codeUnimplemented},
		{"a nested path", "mutate", `{"ns":"ui-onboarding","ops":[{"op":"set","path":["a","b"],"value":"x"}]}`, codeUnimplemented},
		{"a non-string value", "mutate", `{"ns":"ui-onboarding","ops":[{"op":"set","path":["welcomeNoticeVersion"],"value":7}]}`, codeBadRequest},
		{"an unknown op", "mutate", `{"ns":"ui-onboarding","ops":[{"op":"merge","path":["welcomeNoticeVersion"]}]}`, codeBadRequest},
		{"an undeclared section key", "update", `{"ns":"ui-onboarding","patch":{"theme":"dark"}}`, codeUnimplemented},
		{"a namespace the host does not serve", "mutate", `{"ns":"ui-theme","ops":[{"op":"set","path":["theme"],"value":"dark"}]}`, codeBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			method := "settings/" + testCase.method
			response := decodeResponse(t, f.post(t, "/api/"+method, rpcBody(t, "s1", method, testCase.body)))
			if response.Result.OK || response.Result.Error.Code != testCase.code {
				t.Fatalf("code = %q, want %q (%s)", response.Result.Error.Code, testCase.code, response.Result.Error.Message)
			}
		})
	}
	if len(store.sections) != 0 {
		t.Fatalf("sections = %v, want every refused write to leave the namespace untouched", store.sections)
	}
}

// The provider namespaces keep working: the console namespace is an addition,
// not a replacement for the write path the Models page uses.
func TestSettingsMutateStillWritesTheProviderProfile(t *testing.T) {
	f, store := settingsFixture(t)
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate",
		`{"ns":"llm-openai","ops":[{"op":"set","path":["providers","openai","api","model"],"value":"qwen-max"}]}`)), &view)
	if view.NS != "llm-openai" || len(store.models) != 1 || store.models[0] != "qwen-max" {
		t.Fatalf("models = %v, view = %+v, want the provider write to land as before", store.models, view)
	}
}

// namespaceIn returns one namespace's view from a describe payload.
func namespaceIn(t *testing.T, described SettingsDescribeValue, ns string) SettingsNamespaceView {
	t.Helper()
	for _, view := range described.Namespaces {
		if view.NS == ns {
			return view
		}
	}
	t.Fatalf("namespaces = %+v, want %s", described.Namespaces, ns)
	return SettingsNamespaceView{}
}

// userOf returns one view's raw user layer. The console's editors read it to know
// what the operator set and what an accepted write actually stored.
func userOf(t *testing.T, view SettingsNamespaceView) map[string]any {
	t.Helper()
	if view.User == nil {
		t.Fatalf("view %s = %+v, want a user layer", view.NS, view)
	}
	layer, ok := view.User.(map[string]any)
	if !ok {
		t.Fatalf("user = %#v, want a section object", view.User)
	}
	return layer
}

// The console's editors read a namespace's raw user layer and fence their next
// write with the revision they were handed. This host used to report neither:
// the layer was always absent, so a custom provider's fields came back blank
// after every accepted write (ui-settings-models/src/client/ProviderEditor.tsx
// reads namespace.user, then written.view.user and written.view.revision), and a
// constant revision left the editor unable to tell an accepted write from a lost
// one.
func TestSettingsViewsCarryTheUserLayerAndAMovingRevision(t *testing.T) {
	f, profiles := profilesFixture(t)
	if _, err := profiles.SetProviderProfile(ProviderProfile{
		Provider:    "qwen",
		DisplayName: "qwen",
		APIKeyEnv:   "QWEN_API_KEY",
		API:         "openai-completions",
		BaseURL:     "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models:      []ProviderModel{{ID: "qwen3.8-flash"}},
	}); err != nil {
		t.Fatalf("declare the profile: %v", err)
	}
	var described SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s1", "settings/describe", "")), &described)

	// The declared profile is the user layer of the custom-provider namespace.
	user := userOf(t, namespaceIn(t, described, PiAiNamespace))
	providers, ok := user["providers"].(map[string]any)
	if !ok {
		t.Fatalf("user = %#v, want the declared providers", user)
	}
	qwen, ok := providers["qwen"].(map[string]any)
	if !ok {
		t.Fatalf("user providers = %#v, want qwen declared", providers)
	}
	if qwen["baseURL"] != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("user qwen = %#v, want the endpoint the operator declared", qwen)
	}
	if models, ok := qwen["models"].([]any); !ok || len(models) != 1 {
		t.Fatalf("user qwen models = %#v, want the declared row", qwen["models"])
	}
	// The host's own namespace reports its layer too, so its editor behaves the
	// same way rather than marking every field unset.
	user = userOf(t, namespaceIn(t, described, "llm-openai"))
	if providers, ok := user["providers"].(map[string]any); !ok || providers["openai"] == nil {
		t.Fatalf("user = %#v, want the live route's configured provider", user)
	}

	// A committed write moves the revision, and the view it hands back carries
	// both the revision the namespace now stands at and what the write stored.
	var written SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s2", "settings/mutate",
		`{"ns":"llm-pi-ai","expectedRevision":1,"ops":[{"op":"set","path":["providers","qwen","baseURL"],"value":"https://moved.example.test/v1"}]}`)), &written)
	if written.Revision != settingsRevision+1 {
		t.Fatalf("revision = %d, want %d after an accepted write", written.Revision, settingsRevision+1)
	}
	stored, ok := userOf(t, written)["providers"].(map[string]any)
	if !ok || stored["qwen"] == nil {
		t.Fatalf("user = %#v, want the write's own view to carry what landed", written.User)
	}
	if moved := stored["qwen"].(map[string]any)["baseURL"]; moved != "https://moved.example.test/v1" {
		t.Fatalf("user qwen baseURL = %v, want the value just written", moved)
	}

	// The revision the write was fenced at is stale now. The code is the one the
	// console maps to a conflict, and the details say which revision to re-read
	// (ui-settings-models/src/client/operations.ts:100).
	stale := assertMethodFailure(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s3", "settings/mutate",
		`{"ns":"llm-pi-ai","expectedRevision":1,"ops":[{"op":"set","path":["providers","qwen","baseURL"],"value":"https://again.example.test/v1"}]}`)),
		codeSettingsConflict)
	if stale.Result.Error.Details["revision"] == nil || stale.Result.Error.Details["expectedRevision"] == nil {
		t.Fatalf("details = %v, want the expected and actual revisions named", stale.Result.Error.Details)
	}

	// The revision the write produced fences the next one in.
	fresh := f.post(t, "/api/settings/mutate", rpcBody(t, "s4", "settings/mutate",
		`{"ns":"llm-pi-ai","expectedRevision":2,"ops":[{"op":"set","path":["providers","qwen","baseURL"],"value":"https://third.example.test/v1"}]}`))
	if envelope := decodeResponse(t, fresh); !envelope.Result.OK {
		t.Fatalf("the write fenced at the revision it was handed was refused: %+v", envelope.Result.Error)
	}
}
