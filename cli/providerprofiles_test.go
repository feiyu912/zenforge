package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/model/provider"
)

// profileSettingsStore is a settings store holding exactly the given key, so a
// test can tell "resolved from the host's key" from "resolved from the named
// environment variable".
func profileSettingsStore(t *testing.T, apiKey string) *settingsStore {
	t.Helper()
	return &settingsStore{
		current: serverSettings{
			baseURL:  testSettingsBaseURL,
			model:    "qwen-plus",
			provider: provider.OpenAI,
			apiKey:   apiKey,
		},
		model: newSwappableModel(),
	}
}

// profileStoreFixture builds the store over a settings store with no key, the way
// a host started without --api-key looks.
func profileStoreFixture(t *testing.T) (*consoleProviderProfiles, *settingsStore) {
	t.Helper()
	settings := profileSettingsStore(t, "")
	return newConsoleProviderProfiles(settings), settings
}

func declaredProfile(providerID, api, baseURL, keyEnv string, models ...string) dshapi.ProviderProfile {
	merged := make([]dshapi.ProviderModel, 0, len(models))
	for _, id := range models {
		merged = append(merged, dshapi.ProviderModel{ID: id})
	}
	return dshapi.ProviderProfile{Provider: providerID, API: api, BaseURL: baseURL, APIKeyEnv: keyEnv, Models: merged}
}

// The console's card writes the profile before the credential it names, so a
// profile whose key is still missing must be stored with the reason rather than
// refused -- otherwise that order could never complete.
func TestDeclaredProfileIsStoredBeforeItsCredentialExists(t *testing.T) {
	store, _ := profileStoreFixture(t)
	t.Setenv("ACME_API_KEY", "")
	status, err := store.SetProviderProfile(declaredProfile("acme", dshapi.ProtocolOpenAICompletions,
		"https://gateway.acme.example/v1", "ACME_API_KEY", "acme-large"))
	if err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	if status.Error == "" {
		t.Fatal("a profile with no resolvable credential was reported serviceable")
	}
	if !strings.Contains(status.Error, "ACME_API_KEY") {
		t.Fatalf("error = %q, want it to name the missing credential", status.Error)
	}
	if stored := declaredOnly(store.ProviderProfiles()); len(stored) != 1 || stored[0].Profile.Provider != "acme" {
		t.Fatalf("profiles = %+v, want the profile stored anyway", stored)
	}
}

// Serviceability is decided by building the adapter the run would use, so a
// resolvable credential makes the route genuinely serviceable.
func TestDeclaredProfileIsServiceableWhenItsCredentialResolves(t *testing.T) {
	store, _ := profileStoreFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	status, err := store.SetProviderProfile(declaredProfile("anthropic-gw", dshapi.ProtocolAnthropicMessages,
		"https://anthropic.example", "ANTHROPIC_API_KEY", "claude-sonnet-4-5"))
	if err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	if status.Error != "" {
		t.Fatalf("error = %q, want a serviceable profile", status.Error)
	}
}

// The settings page's own key is the host's credential (ADR 0084), so a profile
// must be serviceable off it even when the environment names nothing.
func TestDeclaredProfileResolvesTheConfiguredKey(t *testing.T) {
	settings := profileSettingsStore(t, "sk-host")
	store := newConsoleProviderProfiles(settings)
	t.Setenv("ACME_API_KEY", "")
	status, err := store.SetProviderProfile(declaredProfile("acme", dshapi.ProtocolOpenAICompletions,
		"https://gateway.acme.example/v1", "ACME_API_KEY", "acme-large"))
	if err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	if status.Error != "" {
		t.Fatalf("error = %q, want the host's configured key to resolve", status.Error)
	}
}

// The card writes the profile and then the credential it names, so a diagnostic
// captured at write time would keep calling a configured route unconfigured. The
// answer must follow the credential instead.
func TestDiagnosticFollowsACredentialThatArrivesLater(t *testing.T) {
	store, settings := profileStoreFixture(t)
	t.Setenv("ACME_API_KEY", "")
	if _, err := store.SetProviderProfile(declaredProfile("acme", dshapi.ProtocolOpenAICompletions,
		"https://gateway.acme.example/v1", "ACME_API_KEY", "acme-large")); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	if listed := store.ProviderProfiles(); listed[0].Error == "" {
		t.Fatal("the route was reported serviceable before any credential existed")
	}
	// The card's next call is credentials/set, which stores the host's key.
	credentials := consoleCredentials{settings: settings}
	if err := credentials.StoreCredential("sk-arrived"); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	if listed := store.ProviderProfiles(); listed[0].Error != "" {
		t.Fatalf("error = %q, want the route serviceable once the credential arrived", listed[0].Error)
	}
}

// The Models page picks a provider's editor by the name of the namespace its
// directory entry points at: the console knows llm-deepseek and llm-pi-ai, and
// reports anything else as unknown, which renders a card with no fields and a
// disabled save. Every route this host offers must therefore be a pi-ai card, and
// the two it is built to serve must carry what the host is actually configured
// with rather than an empty form (ADR 0122).
func TestBuiltinRoutesAreEditablePiAiCards(t *testing.T) {
	settings := profileSettingsStore(t, "sk-host")
	settings.rebuild()
	store := newConsoleProviderProfiles(settings)
	listed := store.ProviderProfiles()
	if len(listed) < 2 {
		t.Fatalf("profiles = %+v, want the built-in routes listed", listed)
	}
	live := profileByRoute(t, listed, provider.OpenAI)
	if live.Profile.BaseURL != testSettingsBaseURL || len(live.Profile.Models) != 1 || live.Profile.Models[0].ID != "qwen-plus" {
		t.Fatalf("the live route's card = %+v, want the host's own endpoint and model", live.Profile)
	}
	if live.Profile.APIKeyEnv != "" {
		t.Fatalf("the live route names credential %q, want the host's own", live.Profile.APIKeyEnv)
	}
	other := profileByRoute(t, listed, provider.Anthropic)
	if other.Profile.API != dshapi.ProtocolAnthropicMessages {
		t.Fatalf("anthropic api = %q, want the messages protocol", other.Profile.API)
	}
	// A route the host is not running on names the variable it reads, so it cannot
	// borrow a key for a different service.
	if other.Profile.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Fatalf("anthropic credential reference = %q, want its own variable", other.Profile.APIKeyEnv)
	}
	for _, status := range listed {
		entry := consoleDeclaredProvider(status)
		if entry.SettingsNS != dshapi.PiAiNamespace || len(entry.SettingsPath) != 2 ||
			entry.SettingsPath[0] != "providers" || entry.SettingsPath[1] != status.Profile.Provider {
			t.Fatalf("entry = %+v, want a pi-ai card at providers.%s", entry, status.Profile.Provider)
		}
	}
}

// declaredOnly drops the routes every store is seeded with -- the two this host is
// built to serve, advertised in the pi-ai namespace (ADR 0122) -- so a test can
// assert on what the console declared.
func declaredOnly(listed []dshapi.ProviderProfileStatus) []dshapi.ProviderProfileStatus {
	kept := make([]dshapi.ProviderProfileStatus, 0, len(listed))
	for _, status := range listed {
		if status.Profile.Provider == provider.OpenAI || status.Profile.Provider == provider.Anthropic {
			continue
		}
		kept = append(kept, status)
	}
	return kept
}

// profileByRoute finds one route's status, wherever the seeded routes put it.
func profileByRoute(t *testing.T, listed []dshapi.ProviderProfileStatus, route string) dshapi.ProviderProfileStatus {
	t.Helper()
	for _, status := range listed {
		if status.Profile.Provider == route {
			return status
		}
	}
	t.Fatalf("no profile for %q in %+v", route, listed)
	return dshapi.ProviderProfileStatus{}
}

func TestDeclaredProfilesKeepDeclarationOrderAndAreRemovable(t *testing.T) {
	store, _ := profileStoreFixture(t)
	t.Setenv("A_API_KEY", "k")
	t.Setenv("B_API_KEY", "k")
	for _, id := range []string{"second", "first"} {
		if _, err := store.SetProviderProfile(declaredProfile(id, dshapi.ProtocolOpenAICompletions,
			"https://"+id+".example/v1", strings.ToUpper(id)+"_API_KEY", "m")); err != nil {
			t.Fatalf("SetProviderProfile(%s): %v", id, err)
		}
	}
	// Replacing a stored route must not move it to the end.
	if _, err := store.SetProviderProfile(declaredProfile("second", dshapi.ProtocolOpenAICompletions,
		"https://second.example/v2", "A_API_KEY", "m2")); err != nil {
		t.Fatalf("SetProviderProfile(second): %v", err)
	}
	listed := declaredOnly(store.ProviderProfiles())
	if len(listed) != 2 || listed[0].Profile.Provider != "second" || listed[1].Profile.Provider != "first" {
		t.Fatalf("profiles = %+v, want declaration order preserved across a replace", listed)
	}
	if listed[0].Profile.BaseURL != "https://second.example/v2" {
		t.Fatalf("profile = %+v, want the replacement stored", listed[0].Profile)
	}
	if err := store.RemoveProviderProfile("second"); err != nil {
		t.Fatalf("RemoveProviderProfile: %v", err)
	}
	if listed := declaredOnly(store.ProviderProfiles()); len(listed) != 1 || listed[0].Profile.Provider != "first" {
		t.Fatalf("profiles = %+v, want only first left", listed)
	}
}

// A declared route must appear in the directory with the settings address it was
// written to, and carry its diagnostic, so the Models page can show and repair it.
func TestDeclaredProfileRendersAsADirectoryEntry(t *testing.T) {
	entry := consoleDeclaredProvider(dshapi.ProviderProfileStatus{
		Profile: dshapi.ProviderProfile{Provider: "acme", DisplayName: "Acme Gateway"},
		Error:   "ACME_API_KEY is not set",
	})
	if entry.Provider != "acme" || entry.DisplayName != "Acme Gateway" {
		t.Fatalf("entry = %+v, want the declared identity", entry)
	}
	if entry.SettingsNS != dshapi.PiAiNamespace {
		t.Fatalf("settingsNs = %q, want %q", entry.SettingsNS, dshapi.PiAiNamespace)
	}
	if strings.Join(entry.SettingsPath, ".") != "providers.acme" {
		t.Fatalf("settingsPath = %v, want providers.acme", entry.SettingsPath)
	}
	if entry.Declared == nil || !*entry.Declared {
		t.Fatalf("declared = %v, want true for a hand-declared route", entry.Declared)
	}
	if entry.Error != "ACME_API_KEY is not set" {
		t.Fatalf("error = %q, want the diagnostic carried through", entry.Error)
	}
	// A profile with no display name still needs a label the page can render.
	plain := consoleDeclaredProvider(dshapi.ProviderProfileStatus{Profile: dshapi.ProviderProfile{Provider: "bare"}})
	if plain.DisplayName != "bare" {
		t.Fatalf("displayName = %q, want the route id as a fallback", plain.DisplayName)
	}
	if plain.Error != "" {
		t.Fatalf("error = %q, want a serviceable route to carry none", plain.Error)
	}
}

func TestConsoleProtocolsAreTheOnesTheHostBuilds(t *testing.T) {
	cases := []struct {
		api      string
		protocol string
		ok       bool
	}{
		{dshapi.ProtocolOpenAICompletions, "openai", true},
		{dshapi.ProtocolAnthropicMessages, "anthropic", true},
		{"gemini-generate", "", false},
	}
	for _, testCase := range cases {
		protocol, ok := consoleProtocol(testCase.api)
		if ok != testCase.ok || protocol != testCase.protocol {
			t.Errorf("consoleProtocol(%q) = (%q, %v), want (%q, %v)", testCase.api, protocol, ok, testCase.protocol, testCase.ok)
		}
	}
}

func TestDeclaredProfilesReachTheConsoleDirectory(t *testing.T) {
	// The directory closure is what the llm namespace answers from; a declared
	// route it does not list is a route the page cannot show or edit.
	store, _ := profileStoreFixture(t)
	t.Setenv("ACME_API_KEY", "sk-test")
	if _, err := store.SetProviderProfile(declaredProfile("acme", dshapi.ProtocolOpenAICompletions,
		"https://gateway.acme.example/v1", "ACME_API_KEY", "acme-large")); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	entries := make([]dshapi.LlmConfigurableProvider, 0, 1)
	for _, status := range declaredOnly(store.ProviderProfiles()) {
		entries = append(entries, consoleDeclaredProvider(status))
	}
	if len(entries) != 1 || entries[0].Provider != "acme" || entries[0].Error != "" {
		t.Fatalf("entries = %+v, want one serviceable declared route", entries)
	}
	// The built-in routes are listed too, and every one of them points at the
	// pi-ai namespace: that name is what gives the card its editor.
	for _, status := range store.ProviderProfiles() {
		entry := consoleDeclaredProvider(status)
		if entry.SettingsNS != dshapi.PiAiNamespace || len(entry.SettingsPath) != 2 {
			t.Fatalf("entry = %+v, want a pi-ai settings address", entry)
		}
	}
}

func TestProfileStoreLogsNothingOnItsOwn(t *testing.T) {
	// The store is silent: the API layer answers the refusal, and a second log
	// line would be a second place to keep honest.
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)
	store, _ := profileStoreFixture(t)
	t.Setenv("ACME_API_KEY", "")
	if _, err := store.SetProviderProfile(declaredProfile("acme", dshapi.ProtocolOpenAICompletions,
		"https://gateway.acme.example/v1", "ACME_API_KEY", "acme-large")); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("log = %q, want no output", buf.String())
	}
}
