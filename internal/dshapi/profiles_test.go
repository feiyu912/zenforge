package dshapi

import (
	"encoding/json"
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
