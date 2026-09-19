package dshapi

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// stubProfiles is the injected profile store. It records what was written and
// answers serviceability the way the serve command does.
type stubProfiles struct {
	order         []string
	profiles      map[string]ProviderProfileStatus
	unserviceable string
}

func newStubProfiles() *stubProfiles {
	return &stubProfiles{profiles: map[string]ProviderProfileStatus{}}
}

func (s *stubProfiles) ProviderProfiles() []ProviderProfileStatus {
	listed := make([]ProviderProfileStatus, 0, len(s.order))
	for _, id := range s.order {
		if status, ok := s.profiles[id]; ok {
			listed = append(listed, status)
		}
	}
	return listed
}

func (s *stubProfiles) SetProviderProfile(profile ProviderProfile) (ProviderProfileStatus, error) {
	status := ProviderProfileStatus{Profile: profile, Error: s.unserviceable}
	if _, exists := s.profiles[profile.Provider]; !exists {
		s.order = append(s.order, profile.Provider)
	}
	s.profiles[profile.Provider] = status
	return status, nil
}

func (s *stubProfiles) RemoveProviderProfile(provider string) error {
	delete(s.profiles, provider)
	kept := s.order[:0]
	for _, existing := range s.order {
		if existing != provider {
			kept = append(kept, existing)
		}
	}
	s.order = kept
	return nil
}

func profilesFixture(t *testing.T) (*fixture, *stubProfiles) {
	t.Helper()
	f, _ := settingsFixture(t)
	store := newStubProfiles()
	f.handler.SetProviderProfiles(store)
	return f, store
}

// The exact profile the console's "Add a custom provider" card writes
// (client/CustomProviderCard.tsx:144-170): one set at providers.<route>.
const cardProfileOp = `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme"],"value":{` +
	`"displayName":"Acme Gateway","apiKeyEnv":"ACME_API_KEY","api":"openai-completions",` +
	`"baseURL":"https://gateway.acme.example/v1","models":[{"id":"acme-large","contextWindow":65536,"maxTokens":4096}]}}]}`

func piAiNamespaceView(t *testing.T, described SettingsDescribeValue) *SettingsNamespaceView {
	t.Helper()
	for index := range described.Namespaces {
		if described.Namespaces[index].NS == PiAiNamespace {
			return &described.Namespaces[index]
		}
	}
	return nil
}

// The page reads the protocols it may offer out of this namespace's schema and
// disables the "Add a custom provider" button when it finds none
// (ModelsSection.tsx:535-547, store.ts:126-135). This walks the envelope the way
// that code does, so a schema that stops declaring the union fails here instead of
// silently disabling the entry point in the browser.
// envelopeNode is one schemastery ref as this test reads it: enough of the
// envelope to follow the path the client follows.
type envelopeNode struct {
	Type  string                     `json:"type"`
	Dict  map[string]json.RawMessage `json:"dict"`
	Inner json.RawMessage            `json:"inner"`
	List  []json.Number              `json:"list"`
	Value json.RawMessage            `json:"value"`
}

func TestPiAiSchemaDeclaresTheProtocolsTheCardOffers(t *testing.T) {
	var envelope struct {
		UID  json.Number             `json:"uid"`
		Refs map[string]envelopeNode `json:"refs"`
	}
	if err := json.Unmarshal(piAiSchemaJSON, &envelope); err != nil {
		t.Fatalf("decode the envelope: %v", err)
	}
	resolve := func(raw json.RawMessage) envelopeNode {
		t.Helper()
		var uid json.Number
		if err := json.Unmarshal(raw, &uid); err != nil {
			t.Fatalf("a reference must be a uid: %v (%s)", err, raw)
		}
		node, ok := envelope.Refs[uid.String()]
		if !ok {
			t.Fatalf("refs[%s] is missing", uid)
		}
		return node
	}
	root := resolve(json.RawMessage(envelope.UID.String()))
	providers := resolve(root.Dict["providers"])
	if providers.Type != "dict" {
		t.Fatalf("providers type = %s, want dict so the probe path navigates into the profile", providers.Type)
	}
	profile := resolve(providers.Inner)
	api := resolve(profile.Dict["api"])
	if api.Type != "union" {
		t.Fatalf("providers.<route>.api type = %s, want union: the page only offers protocols a union declares", api.Type)
	}
	offered := make([]string, 0, len(api.List))
	for _, uid := range api.List {
		node, ok := envelope.Refs[uid.String()]
		if !ok {
			t.Fatalf("union member refs[%s] is missing", uid)
		}
		var value string
		if err := json.Unmarshal(node.Value, &value); err != nil {
			t.Fatalf("a union member must carry a string value: %v", err)
		}
		offered = append(offered, value)
	}
	if strings.Join(offered, ",") != strings.Join(hostProtocols, ",") {
		t.Fatalf("offered protocols = %v, want exactly the ones this host speaks (%v)", offered, hostProtocols)
	}
	// The model fields the card writes must be declared too, or the console would
	// reject the section it just wrote when it validates the read.
	model := resolve(resolve(profile.Dict["models"]).Inner)
	for _, field := range []string{"id", "name", "contextWindow", "maxTokens"} {
		if _, ok := model.Dict[field]; !ok {
			t.Errorf("the model schema does not declare %q", field)
		}
	}
	for _, field := range []string{"displayName", "apiKeyEnv", "baseURL"} {
		if _, ok := profile.Dict[field]; !ok {
			t.Errorf("the profile schema does not declare %q", field)
		}
	}
	if len(model.Dict) != 4 || len(profile.Dict) != 5 {
		t.Fatalf("profile fields = %d, model fields = %d, want 5 and 4", len(profile.Dict), len(model.Dict))
	}
}

func TestDescribeCarriesTheProviderProfileNamespace(t *testing.T) {
	f, store := profilesFixture(t)
	store.SetProviderProfile(ProviderProfile{Provider: "acme", API: ProtocolOpenAICompletions,
		BaseURL: "https://gateway.acme.example/v1", Models: []ProviderModel{{ID: "acme-large"}}})
	var described SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s1", "settings/describe", "")), &described)
	view := piAiNamespaceView(t, described)
	if view == nil {
		t.Fatalf("namespaces = %+v, want the provider profile namespace", described.Namespaces)
	}
	providers, ok := view.Value.(map[string]any)["providers"].(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want a providers object", view.Value)
	}
	if _, ok := providers["acme"]; !ok {
		t.Fatalf("providers = %v, want the declared route", providers)
	}
	if view.Applies != "live" || len(view.Secrets) != 0 {
		t.Fatalf("view = %+v, want a live namespace with no secret slot", view)
	}
}

// A host with no profile store does not advertise the namespace, which is what
// keeps the card's entry point hidden rather than offering a write it cannot hold.
func TestTheProfileNamespaceIsAbsentWithoutAStore(t *testing.T) {
	f, _ := settingsFixture(t)
	var described SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s1", "settings/describe", "")), &described)
	if view := piAiNamespaceView(t, described); view != nil {
		t.Fatalf("view = %+v, want no provider profile namespace without a store", view)
	}
	response := decodeResponse(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s2", "settings/mutate", cardProfileOp)))
	if response.Result.OK || response.Result.Error.Code != codeBadRequest {
		t.Fatalf("code = %q, want bad-request for a namespace this host does not hold", response.Result.Error.Code)
	}
}

func TestMutateStoresTheCardProfile(t *testing.T) {
	f, store := profilesFixture(t)
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", cardProfileOp)), &view)
	if view.NS != PiAiNamespace {
		t.Fatalf("ns = %q, want %s", view.NS, PiAiNamespace)
	}
	stored, ok := store.profiles["acme"]
	if !ok {
		t.Fatalf("profiles = %v, want the card's route stored", store.profiles)
	}
	profile := stored.Profile
	if profile.API != ProtocolOpenAICompletions || profile.BaseURL != "https://gateway.acme.example/v1" {
		t.Fatalf("profile = %+v, want the card's protocol and endpoint", profile)
	}
	if profile.APIKeyEnv != "ACME_API_KEY" || len(profile.Models) != 1 || profile.Models[0].ID != "acme-large" {
		t.Fatalf("profile = %+v, want the card's credential reference and model", profile)
	}
	if profile.Models[0].ContextWindow == nil || *profile.Models[0].ContextWindow != 65536 {
		t.Fatalf("model = %+v, want the declared context window", profile.Models[0])
	}
	// The read path the page renders from must carry it back, or the card would
	// look like it saved nothing.
	var described SettingsDescribeValue
	decodeValue(t, f.post(t, "/api/settings/describe", rpcBody(t, "s2", "settings/describe", "")), &described)
	providers := piAiNamespaceView(t, described).Value.(map[string]any)["providers"].(map[string]any)
	section, ok := providers["acme"].(map[string]any)
	if !ok || section["apiKeyEnv"] != "ACME_API_KEY" {
		t.Fatalf("providers = %v, want the stored profile read back", providers)
	}
}

// The provider editor saves an edit by diffing the keys of the profile's subtree
// and addressing each one (ProviderEditor.tsx:123-126), so a field write merges
// into the stored profile instead of replacing it.
func TestProviderProfileFieldWritesMerge(t *testing.T) {
	f, store := profilesFixture(t)
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", cardProfileOp)), &SettingsNamespaceView{})
	fieldOp := func(id, ops string) *SettingsNamespaceView {
		t.Helper()
		var view SettingsNamespaceView
		decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, id, "settings/mutate", ops)), &view)
		return &view
	}
	// A new endpoint keeps the display name, protocol, credential reference and
	// models the editor did not touch.
	fieldOp("s2", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme","baseURL"],"value":"https://gateway2.acme.example/v1"}]}`)
	profile := store.profiles["acme"].Profile
	if profile.BaseURL != "https://gateway2.acme.example/v1" {
		t.Fatalf("baseURL = %q, want the edited endpoint", profile.BaseURL)
	}
	if profile.DisplayName != "Acme Gateway" || profile.API != ProtocolOpenAICompletions ||
		profile.APIKeyEnv != "ACME_API_KEY" || len(profile.Models) != 1 || profile.Models[0].ID != "acme-large" {
		t.Fatalf("profile = %+v, want every untouched field preserved", profile)
	}
	// The model list is one field like any other, and a window the operator
	// corrected survives.
	fieldOp("s3", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme","models"],"value":[{"id":"acme-large","contextWindow":200000},{"id":"acme-mini"}]}]}`)
	profile = store.profiles["acme"].Profile
	if len(profile.Models) != 2 || profile.Models[0].ContextWindow == nil || *profile.Models[0].ContextWindow != 200000 {
		t.Fatalf("models = %+v, want the edited list", profile.Models)
	}
	// An optional field the operator cleared is removed, and the read path the
	// card renders from must show it gone rather than still there.
	view := fieldOp("s4", `{"ns":"llm-pi-ai","ops":[{"op":"unset","path":["providers","acme","apiKeyEnv"]},{"op":"unset","path":["providers","acme","displayName"]}]}`)
	profile = store.profiles["acme"].Profile
	if profile.APIKeyEnv != "" || profile.DisplayName != "" {
		t.Fatalf("profile = %+v, want the cleared fields removed", profile)
	}
	providers := view.Value.(map[string]any)["providers"].(map[string]any)
	section := providers["acme"].(map[string]any)
	if _, present := section["apiKeyEnv"]; present {
		t.Fatalf("section = %v, want the cleared field absent from the view", section)
	}
	if section["baseURL"] != "https://gateway2.acme.example/v1" {
		t.Fatalf("section = %v, want the edited endpoint read back", section)
	}
}

// A field write that cannot be honoured changes nothing: no half-merged profile
// is stored, and every refusal names the field it will not hold.
func TestProviderProfileFieldWriteRefusals(t *testing.T) {
	f, store := profilesFixture(t)
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", cardProfileOp)), &SettingsNamespaceView{})
	before := store.profiles["acme"].Profile
	cases := []struct {
		name      string
		ops       string
		wantCode  string
		wantInMsg string
	}{
		{"a field this host does not store",
			`{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme","apiVersion"],"value":"v2"}]}`,
			codeUnimplemented, `field "apiVersion" is not one this host stores`},
		{"an edit to a route that is not there",
			`{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","nope","baseURL"],"value":"https://nope.example/v1"}]}`,
			codeBadRequest, `no stored profile for provider "nope"`},
		{"a protocol this host does not speak",
			`{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme","api"],"value":"gemini-generate"}]}`,
			codeBadRequest, `protocol "gemini-generate" is not one this host speaks`},
		{"clearing a required field",
			`{"ns":"llm-pi-ai","ops":[{"op":"unset","path":["providers","acme","baseURL"]}]}`,
			codeBadRequest, "baseURL"},
		{"a path deeper than one field",
			`{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme","models","0","id"],"value":"acme-large"}]}`,
			codeUnimplemented, "is not stored by this host"},
		{"a model field this host does not store",
			`{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme","models"],"value":[{"id":"acme-large","input":["text"]}]}]}`,
			codeBadRequest, `model field "input" is not one this host stores`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := decodeResponse(t, f.post(t, "/api/settings/mutate",
				rpcBody(t, "s1", "settings/mutate", testCase.ops)))
			if response.Result.OK {
				t.Fatalf("result = ok, want a refusal (value %s)", response.Result.Value)
			}
			if response.Result.Error.Code != testCase.wantCode {
				t.Fatalf("code = %q, want %q (message %q)", response.Result.Error.Code, testCase.wantCode, response.Result.Error.Message)
			}
			if !strings.Contains(response.Result.Error.Message, testCase.wantInMsg) {
				t.Fatalf("message = %q, want it to mention %q", response.Result.Error.Message, testCase.wantInMsg)
			}
			if after := store.profiles["acme"].Profile; after.BaseURL != before.BaseURL ||
				after.API != before.API || len(after.Models) != len(before.Models) ||
				after.APIKeyEnv != before.APIKeyEnv || after.DisplayName != before.DisplayName {
				t.Fatalf("profile = %+v, want every refused write to change nothing (was %+v)", after, before)
			}
		})
	}
}

// The field list this host accepts is the schema's own, so a field the console
// can render is never refused and a field it cannot is never stored.
func TestProviderProfileFieldsMatchTheSchema(t *testing.T) {
	var envelope struct {
		UID  json.Number             `json:"uid"`
		Refs map[string]envelopeNode `json:"refs"`
	}
	if err := json.Unmarshal(piAiSchemaJSON, &envelope); err != nil {
		t.Fatalf("decode the envelope: %v", err)
	}
	// The profile node is the providers dict's inner object; the model node is
	// the models array's inner object.
	var profileFields, modelFields []string
	for _, node := range envelope.Refs {
		if node.Type != "object" {
			continue
		}
		if _, ok := node.Dict["baseURL"]; ok {
			for name := range node.Dict {
				profileFields = append(profileFields, name)
			}
		}
		if _, ok := node.Dict["contextWindow"]; ok {
			for name := range node.Dict {
				modelFields = append(modelFields, name)
			}
		}
	}
	sort.Strings(profileFields)
	sort.Strings(modelFields)
	storedProfiles := append([]string{}, providerProfileFields...)
	storedModels := append([]string{}, providerModelFields...)
	sort.Strings(storedProfiles)
	sort.Strings(storedModels)
	if strings.Join(profileFields, ",") != strings.Join(storedProfiles, ",") {
		t.Fatalf("schema profile fields = %v, want the stored set %v", profileFields, storedProfiles)
	}
	if strings.Join(modelFields, ",") != strings.Join(storedModels, ",") {
		t.Fatalf("schema model fields = %v, want the stored set %v", modelFields, storedModels)
	}
}

func TestMutateUnsetsAndUpdatesDeclaredRoutes(t *testing.T) {
	f, store := profilesFixture(t)
	store.SetProviderProfile(ProviderProfile{Provider: "acme", API: ProtocolOpenAICompletions,
		BaseURL: "https://gateway.acme.example/v1", Models: []ProviderModel{{ID: "acme-large"}}})
	store.SetProviderProfile(ProviderProfile{Provider: "other", API: ProtocolAnthropicMessages,
		BaseURL: "https://other.example", Models: []ProviderModel{{ID: "other-large"}}})
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate",
		`{"ns":"llm-pi-ai","ops":[{"op":"unset","path":["providers","acme"]}]}`)), &view)
	if _, present := store.profiles["acme"]; present {
		t.Fatal("acme is still stored after unset")
	}
	if len(store.profiles) != 1 {
		t.Fatalf("profiles = %v, want only the other route left", store.profiles)
	}
	// update merges the named routes into what is stored.
	decodeValue(t, f.post(t, "/api/settings/update", rpcBody(t, "s2", "settings/update",
		`{"ns":"llm-pi-ai","patch":{"providers":{"third":{"api":"anthropic-messages","baseURL":"https://third.example","models":[{"id":"third-large"}]}}}}`)), &view)
	if len(store.profiles) != 2 {
		t.Fatalf("profiles = %v, want the merge to keep other and add third", store.profiles)
	}
	// replace swaps the section, so a route it does not name is removed.
	decodeValue(t, f.post(t, "/api/settings/replace", rpcBody(t, "s3", "settings/replace",
		`{"ns":"llm-pi-ai","section":{"providers":{}}}`)), &view)
	if len(store.profiles) != 0 {
		t.Fatalf("profiles = %v, want an empty section to remove every route", store.profiles)
	}
}

// A profile this host cannot serve yet is stored with the reason, not refused: the
// card writes the profile before the credential it names, and the reason is what
// the directory shows for repair.
func TestAnUnserviceableProfileIsStoredWithItsReason(t *testing.T) {
	f, store := profilesFixture(t)
	store.unserviceable = "ACME_API_KEY is not set"
	view := SettingsNamespaceView{}
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", cardProfileOp)), &view)
	if _, ok := store.profiles["acme"]; !ok {
		t.Fatal("an unserviceable profile was not stored")
	}
	if strings.Contains(view.NS, "error") {
		t.Fatal("the settings view must not carry the directory diagnostic")
	}
}

func TestProfileWritesAreRefusedByShape(t *testing.T) {
	f, store := profilesFixture(t)
	cases := []struct {
		name string
		body string
	}{
		{"a route that is not an identifier", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","9acme"],"value":{"api":"openai-completions","baseURL":"https://a.example","models":[{"id":"m"}]}}]}`},
		{"a protocol this host does not speak", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme"],"value":{"api":"gemini-generate","baseURL":"https://a.example","models":[{"id":"m"}]}}]}`},
		{"a relative baseURL", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme"],"value":{"api":"openai-completions","baseURL":"/v1","models":[{"id":"m"}]}}]}`},
		{"no baseURL", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme"],"value":{"api":"openai-completions","models":[{"id":"m"}]}}]}`},
		{"no models", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme"],"value":{"api":"openai-completions","baseURL":"https://a.example","models":[]}}]}`},
		{"an unusable credential reference", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers","acme"],"value":{"api":"openai-completions","baseURL":"https://a.example","apiKeyEnv":"not a ref","models":[{"id":"m"}]}}]}`},
		{"an undeclared path", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["theme"],"value":"dark"}]}`},
		{"the profile itself", `{"ns":"llm-pi-ai","ops":[{"op":"set","path":["providers"],"value":{}}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := decodeResponse(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", testCase.body)))
			if response.Result.OK {
				t.Fatalf("the write was accepted: %s", response.Result.Value)
			}
			if response.Result.Error.Code != codeBadRequest && response.Result.Error.Code != codeUnimplemented {
				t.Fatalf("code = %q, want a refusal that names the shape", response.Result.Error.Code)
			}
		})
	}
	if len(store.profiles) != 0 {
		t.Fatalf("profiles = %v, want every refused write to leave the store untouched", store.profiles)
	}
}

func TestProfileModelWindowsRoundTrip(t *testing.T) {
	// Numbers on the wire are JSON numbers; the page renders them and compares
	// them, so a stored window must come back as a number rather than a string.
	f, _ := profilesFixture(t)
	var view SettingsNamespaceView
	decodeValue(t, f.post(t, "/api/settings/mutate", rpcBody(t, "s1", "settings/mutate", cardProfileOp)), &view)
	raw, err := json.Marshal(view.Value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"contextWindow":65536`) {
		t.Fatalf("value = %s, want the context window as a number", raw)
	}
}
