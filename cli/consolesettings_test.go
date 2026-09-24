package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/model/provider"
)

// The tests here are the assertions ADR 0102 makes about the console's settings
// document: the credential and the configuration around it survive a restart, the
// file that holds them is reachable only by its owner, it is never somewhere the
// console can already read, a damaged one stops the host by name, and the key never
// appears anywhere but in that file.

const (
	documentTestBaseURL = "https://example.test/v1"
	documentTestKey     = "sk-document-sentinel-9d2c"
)

// documentHarness is the set of stores a settings document is wired around. The
// wiring order matters and matches newServeApp: the model-selection store exists
// before the document is read, because the document carries the model each session
// chose and the restart restores it through that store (ADR 0103).
type documentHarness struct {
	store      *settingsStore
	profiles   *consoleProviderProfiles
	models     consoleModels
	selections *consoleModelSelection
}

// newDocumentStores wires the stores around a settings document the way
// newServeApp does, so a test that calls it twice with the same path is a host
// before a restart and the same host after one.
func newDocumentStores(t *testing.T, path string, seed serverSettings) (*settingsStore, *consoleProviderProfiles) {
	t.Helper()
	harness := newDocumentHarness(t, path, seed)
	return harness.store, harness.profiles
}

func newDocumentHarness(t *testing.T, path string, seed serverSettings) documentHarness {
	t.Helper()
	store := &settingsStore{
		current:     seed,
		seed:        seed,
		model:       newSwappableModel(),
		allowRemote: false,
	}
	profiles := newConsoleProviderProfiles(store)
	store.profiles = profiles
	models := consoleModels{settings: store, profiles: profiles}
	selections := newConsoleModelSelection(store, models)
	store.selections = selections
	if _, err := newConsoleSettingsDocument(path, "", store, profiles); err != nil {
		t.Fatalf("wire the settings document at %s: %v", path, err)
	}
	store.rebuild()
	return documentHarness{store: store, profiles: profiles, models: models, selections: selections}
}

func documentSeed() serverSettings {
	return serverSettings{
		baseURL:  "https://seed.test/v1",
		model:    "seed-model",
		provider: provider.OpenAI,
	}
}

func TestConsoleSettingsDocumentSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console-settings.json")
	store, profiles := newDocumentStores(t, path, documentSeed())

	if store.HasSettingsDocument() {
		t.Fatal("hasDocument = true before anything was written; the host creates the file on the first write")
	}
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, "qwen-max"); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	if err := store.setAPIKey(documentTestKey); err != nil {
		t.Fatalf("setAPIKey: %v", err)
	}
	if err := store.SetConsoleSection("ui-onboarding", map[string]any{"welcomeNoticeVersion": "2026-09-19.1"}); err != nil {
		t.Fatalf("SetConsoleSection: %v", err)
	}
	if _, err := profiles.SetProviderProfile(dshapi.ProviderProfile{
		Provider:    "acme",
		DisplayName: "Acme",
		API:         dshapi.ProtocolOpenAICompletions,
		BaseURL:     "https://acme.test/v1",
		Models:      []dshapi.ProviderModel{{ID: "acme-1"}},
	}); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	if got := store.AdvanceSettingsRevision("llm-openai"); got != 2 {
		t.Fatalf("revision after one write = %d, want 2", got)
	}
	if !store.HasSettingsDocument() {
		t.Fatal("hasDocument = false after a committed write")
	}

	// The restart: a fresh set of stores over the same file, seeded with the flags
	// this host was started with.
	restarted, restartedProfiles := newDocumentStores(t, path, serverSettings{
		baseURL:  "https://a-different-flag.test/v1",
		model:    "flag-model",
		provider: provider.Anthropic,
	})
	view := restarted.view()
	if view.BaseURL != documentTestBaseURL {
		t.Errorf("baseUrl after restart = %q, want the endpoint the console wrote, not the flag", view.BaseURL)
	}
	if view.Model != "qwen-max" {
		t.Errorf("model after restart = %q, want the model the console wrote", view.Model)
	}
	if !view.HasAPIKey {
		t.Errorf("hasApiKey after restart = false, want the stored credential back")
	}
	if got := restarted.configuredAPIKey(); got != documentTestKey {
		t.Errorf("the reloaded credential is not the one that was stored")
	}
	if got := restarted.SettingsRevision("llm-openai"); got != 2 {
		t.Errorf("revision after restart = %d, want the number the document carries: a fence that resets is not a fence", got)
	}
	if got := restarted.ConsoleSection("ui-onboarding")["welcomeNoticeVersion"]; got != "2026-09-19.1" {
		t.Errorf("the welcome notice acknowledgement after restart = %v, want the stored version", got)
	}
	listed := declaredAfterBuiltins(t, restartedProfiles.ProviderProfiles())
	if len(listed) != 1 || listed[0].Profile.Provider != "acme" {
		t.Fatalf("declared profiles after restart = %+v, want the one route the console declared", listed)
	}
	if listed[0].Profile.Models[0].ID != "acme-1" {
		t.Errorf("declared profile models = %+v, want the declared row", listed[0].Profile.Models)
	}
	if !restarted.HasSettingsDocument() {
		t.Error("hasDocument = false with a document on disk")
	}
}

func TestConsoleSettingsDocumentIsOwnerReadableOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "console-settings.json")
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.setAPIKey(documentTestKey); err != nil {
		t.Fatalf("setAPIKey: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the document was not created where it was configured: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("document mode = %04o, want 0600: it holds the credential", mode)
	}
	// The staging file is renamed into place, so a completed write leaves nothing
	// else behind in the directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read the document directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			t.Errorf("the write left %q behind in the document directory", entry.Name())
		}
	}
	// What is on disk is the document, and it holds the key -- the one place this
	// host writes it.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}
	if !strings.Contains(string(raw), documentTestKey) {
		t.Fatal("the document does not carry the credential it exists to persist")
	}
}

func TestConsoleSettingsDocumentNeverLandsInsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "console-settings.json")
	if err := refuseSettingsFileInWorkspace(inside, workspace); err == nil {
		t.Fatal("a settings document inside the served workspace was accepted; the console reads workspace files back to the browser")
	} else if !strings.Contains(err.Error(), inside) || !strings.Contains(err.Error(), "--settings-file") {
		t.Fatalf("refusal = %q, want it to name the path and the flag", err)
	}
	for _, allowed := range []string{
		filepath.Join(filepath.Dir(workspace), "console-settings.json"),
		filepath.Join(workspace, "..", "console-settings.json"),
		"",
	} {
		if err := refuseSettingsFileInWorkspace(allowed, workspace); err != nil {
			t.Errorf("refuseSettingsFileInWorkspace(%q) = %v, want it accepted", allowed, err)
		}
	}
}

func TestConsoleSettingsDocumentPathIsTheHostConfigDir(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "zenforge-config")
	t.Setenv("ZENFORGE_CONFIG_DIR", configDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	defaultPath, err := consoleSettingsPath("")
	if err != nil {
		t.Fatalf("consoleSettingsPath: %v", err)
	}
	if want := filepath.Join(configDir, consoleSettingsFileName); defaultPath != want {
		t.Fatalf("default path = %q, want %q", defaultPath, want)
	}
	repository, err := os.Getwd()
	if err != nil {
		t.Fatalf("get the working directory: %v", err)
	}
	if relative, err := filepath.Rel(repository, defaultPath); err == nil &&
		!strings.HasPrefix(relative, "..") && !filepath.IsAbs(relative) {
		t.Fatalf("default path %q is inside the checkout at %q", defaultPath, repository)
	}
	explicit, err := consoleSettingsPath(filepath.Join(t.TempDir(), "elsewhere.json"))
	if err != nil {
		t.Fatalf("consoleSettingsPath with an explicit file: %v", err)
	}
	if filepath.Base(explicit) != "elsewhere.json" {
		t.Fatalf("explicit path = %q, want the operator's own file", explicit)
	}
}

func TestConsoleSettingsDocumentWithoutAConfigDirStaysProcessLocal(t *testing.T) {
	// No home, no XDG directory, no explicit flag: there is nowhere host-owned to
	// write, and the host says so rather than putting a credential somewhere
	// surprising.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ZENFORGE_CONFIG_DIR", "")
	path, err := consoleSettingsPath("")
	if err != nil {
		t.Fatalf("consoleSettingsPath: %v", err)
	}
	if path != "" {
		t.Fatalf("path = %q, want the empty answer that means no durable home", path)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	store, _ := newDocumentStores(t, "", documentSeed())
	if err := store.setAPIKey(documentTestKey); err != nil {
		t.Fatalf("setAPIKey on a host with no durable home: %v", err)
	}
	if !store.view().HasAPIKey {
		t.Error("the key was not held in memory by a host with no document")
	}
	if store.HasSettingsDocument() {
		t.Error("hasDocument = true with no document anywhere")
	}
	line := logs.String()
	if !strings.Contains(line, "restart") {
		t.Errorf("the host did not say its settings will not survive a restart: %s", line)
	}
	if strings.Contains(line, documentTestKey) {
		t.Error("the credential reached the log")
	}
}

func TestConsoleSettingsDocumentRefusesADamagedFileByName(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content string
		mode    os.FileMode
		absent  string
	}{
		{
			name:    "half a JSON object",
			content: `{"version":1,"apiKey":"` + documentTestKey + `"`,
		},
		{
			name:    "a field of the wrong type",
			content: `{"version":1,"apiKey":[` + documentTestKey + `]}`,
		},
		{
			name:    "a field this host does not store",
			content: `{"version":1,"secrets":"` + documentTestKey + `"}`,
		},
		{
			name:    "a document from a newer host",
			content: `{"version":99,"apiKey":"` + documentTestKey + `"}`,
		},
		{
			name:    "an unversioned file",
			content: `{"apiKey":"` + documentTestKey + `"}`,
		},
		{
			name:    "a file other users can read",
			content: `{"version":1,"apiKey":"` + documentTestKey + `"}`,
			mode:    0o644,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), consoleSettingsFileName)
			if err := os.WriteFile(path, []byte(testCase.content), 0o600); err != nil {
				t.Fatalf("write the test document: %v", err)
			}
			if testCase.mode != 0 {
				if err := os.Chmod(path, testCase.mode); err != nil {
					t.Fatalf("set the test mode: %v", err)
				}
			}
			_, found, err := loadConsoleSettingsFile(path)
			if err == nil {
				t.Fatalf("a damaged document was accepted (found = %v)", found)
			}
			if found {
				t.Error("a refused document reported itself as loaded")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("refusal = %q, want it to name the file the operator has to fix", err)
			}
			// The message describes the damage and never quotes the file: the
			// bytes it is failing on are the credential.
			if strings.Contains(err.Error(), documentTestKey) {
				t.Errorf("refusal = %q, want it to name the damage rather than repeat the document", err)
			}
		})
	}
}

func TestConsoleSettingsDocumentWriteFailureKeepsTheOldValue(t *testing.T) {
	// The host starts with a document it can write, and then the document stops
	// being writable: the path's parent becomes a plain file. A write that cannot
	// reach the file has to be refused with the value it already had left standing,
	// because a setting that lives only in this process is the disagreement between
	// the Models page and the next restart that this document exists to remove.
	directory := t.TempDir()
	path := filepath.Join(directory, consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, ""); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write the blocking file: %v", err)
	}
	store.document = newConsoleSettingsWriter(filepath.Join(blocker, consoleSettingsFileName), store.documentFile)
	before := store.view()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	err := store.replaceEndpoint(provider.OpenAI, "https://moved.test/v1", "")
	if err == nil {
		t.Fatal("a settings write to an unwritable document reported success")
	}
	after := store.view()
	if after.BaseURL != before.BaseURL {
		t.Errorf("baseUrl = %q after a failed write, want the value the document still holds (%q)", after.BaseURL, before.BaseURL)
	}
	if line := logs.String(); !strings.Contains(line, blocker) {
		t.Errorf("the failed write was not logged with the file it could not write: %s", line)
	}
	if strings.Contains(logs.String(), documentTestKey) || strings.Contains(err.Error(), documentTestKey) {
		t.Error("a failed write put the credential into an error or a log line")
	}
	// A revision advance whose document write failed leaves the field change
	// standing: the number goes back to the one the file holds.
	if err := store.setAPIKey(documentTestKey); err == nil {
		t.Fatal("storing a credential with an unwritable document reported success")
	}
	if store.view().HasAPIKey {
		t.Error("the credential was kept by a host whose write was refused")
	}
}

func TestConsoleSettingsDocumentNamesTheFieldsItOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, "qwen-max"); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	if err := store.setAPIKey(documentTestKey); err != nil {
		t.Fatalf("setAPIKey: %v", err)
	}

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	// Restart with different flags: the document wins, and the host says which
	// fields it overrode -- by name, never by value.
	restarted, _ := newDocumentStores(t, path, serverSettings{
		baseURL:  "https://a-different-flag.test/v1",
		model:    "flag-model",
		provider: provider.Anthropic,
		apiKey:   "flag-supplied-key",
	})
	if got := restarted.view(); got.BaseURL != documentTestBaseURL || got.Model != "qwen-max" {
		t.Fatalf("view after restart = %+v, want the document's endpoint and model", got)
	}
	line := logs.String()
	for _, want := range []string{"baseUrl", "model", "api-key"} {
		if !strings.Contains(line, want) {
			t.Errorf("the startup line did not name the overridden %q field: %s", want, line)
		}
	}
	for _, value := range []string{"a-different-flag.test", "flag-supplied-key", documentTestKey} {
		if strings.Contains(line, value) {
			t.Errorf("the startup line carried the value %q rather than a field name: %s", value, line)
		}
	}
	// The console never moved the provider, so the document does not name it and
	// the flag keeps answering that field: an override is recorded only where an
	// operator's later intent actually contradicted an earlier one.
	if got := restarted.view().Provider; got != provider.Anthropic {
		t.Errorf("provider after restart = %q, want the flag's value for a field the console never wrote", got)
	}
	if strings.Contains(line, "provider") {
		t.Errorf("the startup line named a field the console never moved: %s", line)
	}
}

func TestConsoleSettingsDocumentLeavesAFieldItNeverMovedAlone(t *testing.T) {
	// The sharp case for that rule: a host configured with --api-key, whose
	// console then writes only the endpoint and the model. Writing the whole live
	// state would name the key as the empty string, and the next start would read
	// that as "the console cleared the credential" -- wiping a key the operator
	// keeps in a flag or an environment variable.
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	seeded := serverSettings{
		provider: provider.OpenAI,
		baseURL:  "https://seed.test/v1",
		model:    "seed-model",
		apiKey:   "flag-supplied-key",
	}
	store, _ := newDocumentStores(t, path, seeded)
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, "qwen-max"); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the document: %v", err)
	}
	// The credential *value* field, not apiKeyEnv: a seeded provider profile names
	// the environment variable its route reads, which is a reference the operator
	// can see, while the value field is what must never be written (ADR 0122).
	if strings.Contains(string(raw), `"apiKey":`) {
		t.Fatalf("the document named a credential the console never wrote: %s", raw)
	}
	restarted, _ := newDocumentStores(t, path, seeded)
	if !restarted.view().HasAPIKey {
		t.Fatal("the restart lost the flag-supplied key the console never touched")
	}
	if got := restarted.view().BaseURL; got != documentTestBaseURL {
		t.Fatalf("baseUrl after restart = %q, want the endpoint the console did write", got)
	}
}

func TestConsoleSettingsDocumentFillingAGapIsNotAnOverride(t *testing.T) {
	// A host started with no flags has nothing for the document to override, so
	// the startup line must not claim it did: the document filled a hole.
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, "qwen-max"); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	restarted, _ := newDocumentStores(t, path, serverSettings{})
	if got := restarted.view().BaseURL; got != documentTestBaseURL {
		t.Fatalf("baseUrl after restart = %q, want the document to supply what the flags did not", got)
	}
	line := logs.String()
	if !strings.Contains(line, "loaded the console settings document") {
		t.Fatalf("startup did not report the load: %s", line)
	}
	if strings.Contains(line, "overrides") {
		t.Errorf("a host with no startup configuration was told its flags were overridden: %s", line)
	}
}

func TestConsoleSettingsDocumentClearsStayCleared(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, "qwen-max"); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	if err := store.replaceEndpoint(provider.OpenAI, "", ""); err != nil {
		t.Fatalf("clearing the endpoint override: %v", err)
	}
	restarted, _ := newDocumentStores(t, path, documentSeed())
	if got := restarted.view().BaseURL; got != "" {
		t.Fatalf("baseUrl after restart = %q, want the cleared override to stay cleared rather than falling back to the seed", got)
	}
}

func TestConsoleSettingsDocumentNeverWritesTheKeyAnywhereElse(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	if err := store.setAPIKey(documentTestKey); err != nil {
		t.Fatalf("setAPIKey: %v", err)
	}
	// The console's own adapter answers, and the reply shape carries no key.
	adapter := consoleSettings{settings: store}
	if profile := adapter.SettingsProfile(); profile.HasKey != true {
		t.Fatalf("SettingsProfile().HasKey = %v, want the stored key reported as present", profile.HasKey)
	}
	encodedProfile, err := json.Marshal(profileSettingsView(adapter.SettingsProfile()))
	if err != nil {
		t.Fatalf("encode the profile: %v", err)
	}
	if strings.Contains(string(encodedProfile), documentTestKey) {
		t.Errorf("the settings profile carries the key: %s", encodedProfile)
	}
	if response := storeGET(t, store); strings.Contains(response, documentTestKey) {
		t.Errorf("the settings endpoint response carries the key: %s", response)
	}
	if describe := settingsDescribeValue(t, adapter); strings.Contains(describe, documentTestKey) {
		t.Errorf("settings/describe carries the key: %s", describe)
	}
	if strings.Contains(logs.String(), documentTestKey) {
		t.Errorf("a settings write put the key into the log: %s", logs.String())
	}
	// Every file the host wrote, in the whole directory.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if entry.Name() != consoleSettingsFileName && strings.Contains(string(data), documentTestKey) {
			t.Errorf("%s carries the credential; only the settings document may", entry.Name())
		}
	}
}

// profileSettingsView renders the profile the way the console sees it, so the test
// asks about bytes rather than about a Go struct that happens to have no key field.
func profileSettingsView(profile dshapi.SettingsProfile) any { return profile }

// settingsDescribeValue drives the namespace's describe through the adapter the
// console reaches it with.
func settingsDescribeValue(t *testing.T, adapter consoleSettings) string {
	t.Helper()
	profile := adapter.SettingsProfile()
	view := map[string]any{
		"hasDocument": adapter.HasSettingsDocument(),
		"profile":     profile,
		"onboarding":  adapter.ConsoleSection("ui-onboarding"),
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode the described view: %v", err)
	}
	return string(encoded)
}

// storeGET reads the plain settings endpoint the serve command also mounts.
func storeGET(t *testing.T, store *settingsStore) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	store.serveHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	return recorder.Body.String()
}

func TestConsoleSettingsDocumentCarriesRevisionsAndSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, profiles := newDocumentStores(t, path, documentSeed())
	for range 3 {
		store.AdvanceSettingsRevision(dshapi.PiAiNamespace)
	}
	if _, err := profiles.SetProviderProfile(dshapi.ProviderProfile{
		Provider: "acme", API: dshapi.ProtocolAnthropicMessages, BaseURL: "https://acme.test",
	}); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	file, found, err := loadConsoleSettingsFile(path)
	if err != nil || !found {
		t.Fatalf("load the document: found=%v err=%v", found, err)
	}
	if got := file.Revisions[dshapi.PiAiNamespace]; got != 4 {
		t.Errorf("document revision for %s = %d, want 4 (the initial revision plus three writes)", dshapi.PiAiNamespace, got)
	}
	if len(file.ProviderProfiles) != 3 || file.ProviderProfiles[2].Provider != "acme" {
		t.Fatalf("document profiles = %+v, want the two built-in routes and the declared one", file.ProviderProfiles)
	}
	// The document is JSON a operator can read, and it numbers itself.
	if file.Version != consoleSettingsFileVersion {
		t.Errorf("document version = %d, want %d", file.Version, consoleSettingsFileVersion)
	}
}

func TestConsoleSettingsDocumentRemovesAProfileFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	_, profiles := newDocumentStores(t, path, documentSeed())
	for _, route := range []string{"acme", "other"} {
		if _, err := profiles.SetProviderProfile(dshapi.ProviderProfile{
			Provider: route, API: dshapi.ProtocolOpenAICompletions, BaseURL: "https://" + route + ".test/v1",
			Models: []dshapi.ProviderModel{{ID: route + "-1"}},
		}); err != nil {
			t.Fatalf("declare %s: %v", route, err)
		}
	}
	if err := profiles.RemoveProviderProfile("acme"); err != nil {
		t.Fatalf("RemoveProviderProfile: %v", err)
	}
	restarted, restartedProfiles := newDocumentStores(t, path, documentSeed())
	if got := restarted.SettingsRevision("llm-openai"); got != dshapi.SettingsInitialRevision {
		t.Errorf("an untouched namespace reports revision %d, want the initial revision", got)
	}
	listed := declaredAfterBuiltins(t, restartedProfiles.ProviderProfiles())
	if len(listed) != 1 || listed[0].Profile.Provider != "other" {
		t.Fatalf("profiles after restart = %+v, want only the route that was not removed", listed)
	}
}

func TestConsoleSettingsDocumentIsWrittenForTheLegacyEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"model":"qwen-max","apiKey":"`+documentTestKey+`"}`))
	request.RemoteAddr = "127.0.0.1:5555"
	store.serveHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/settings = %d: %s", recorder.Code, recorder.Body.String())
	}
	file, found, err := loadConsoleSettingsFile(path)
	if err != nil || !found {
		t.Fatalf("the plain settings endpoint did not write the document: found=%v err=%v", found, err)
	}
	if file.Model == nil || *file.Model != "qwen-max" {
		t.Errorf("document model = %v, want the model that was posted", file.Model)
	}
	restarted, _ := newDocumentStores(t, path, documentSeed())
	if !restarted.view().HasAPIKey {
		t.Error("the credential posted to the plain endpoint did not survive the restart")
	}
}

func TestConsoleSettingsDocumentLoadedBeforeTheFirstView(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.replaceEndpoint(provider.OpenAI, documentTestBaseURL, "qwen-max"); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	if err := store.setAPIKey(documentTestKey); err != nil {
		t.Fatalf("setAPIKey: %v", err)
	}
	// A host that starts from the document must answer its first catalog read from
	// the document, not from the flags.
	restarted, restartedProfiles := newDocumentStores(t, path, serverSettings{})
	models := consoleModels{settings: restarted, profiles: restartedProfiles}
	catalog := models.Catalog()
	encoded := fmt.Sprintf("%+v", catalog)
	if !strings.Contains(encoded, "qwen-max") {
		t.Fatalf("the first model catalog after a restart = %s, want the stored model", encoded)
	}
}

// TestConsoleUserLayerReportsOnlyWhatTheConsoleWrote is the assertion ADR 0103
// makes about the provider namespace: a flag is not a saved setting. A host
// started with an endpoint and a model reports neither as the page's user layer,
// because an operator who never opened the page must not be shown one they saved
// -- and must not be offered a configuration to save over.
func TestConsoleUserLayerReportsOnlyWhatTheConsoleWrote(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())

	if section, found := store.SettingsUserSection(provider.OpenAI); found {
		t.Fatalf("the user layer reported %v before any console write; the flags this host was started with are not saved settings", section)
	}
	// The endpoint and the model are still what a run would use: the resolved view
	// is the live configuration, and only the user layer is narrower than it.
	view := store.view()
	if view.BaseURL != documentSeed().baseURL || view.Model != documentSeed().model {
		t.Fatalf("resolved view = %+v, want the seed configuration untouched", view)
	}

	// Writing only the model reports only the model. If this marked the endpoint as
	// well, the page would show a saved endpoint the operator never entered.
	if err := store.replaceModel(provider.OpenAI, "qwen-max"); err != nil {
		t.Fatalf("replaceModel: %v", err)
	}
	section, found := store.SettingsUserSection(provider.OpenAI)
	if !found {
		t.Fatal("the user layer is absent after the console wrote the model")
	}
	encoded := fmt.Sprintf("%+v", section)
	if !strings.Contains(encoded, "qwen-max") {
		t.Fatalf("user layer = %s, want the model the console wrote", encoded)
	}
	if strings.Contains(encoded, documentSeed().baseURL) {
		t.Fatalf("user layer = %s, want no endpoint: the console never wrote one", encoded)
	}
	file := readDocumentFile(t, path)
	if file.BaseURL != nil {
		t.Errorf("document baseUrl = %q, want it absent: nothing wrote it", *file.BaseURL)
	}
	if file.Model == nil || *file.Model != "qwen-max" {
		t.Errorf("document model = %v, want the model the console wrote", file.Model)
	}

	// A field the console did write stays reported and recorded across a restart,
	// even when it happens to equal the flag it was started with.
	if err := store.replaceEndpoint(provider.OpenAI, documentSeed().baseURL, documentSeed().model); err != nil {
		t.Fatalf("replaceEndpoint: %v", err)
	}
	file = readDocumentFile(t, path)
	if file.BaseURL == nil || *file.BaseURL != documentSeed().baseURL {
		t.Errorf("document baseUrl = %v, want it recorded: the console wrote the value the flag happened to carry", file.BaseURL)
	}
	if file.Model == nil || *file.Model != documentSeed().model {
		t.Errorf("document model = %v, want it recorded for the same reason", file.Model)
	}
	restarted, _ := newDocumentStores(t, path, serverSettings{})
	section, found = restarted.SettingsUserSection(provider.OpenAI)
	if !found {
		t.Fatal("the user layer is absent after a restart, want the console's own write restored")
	}
	encoded = fmt.Sprintf("%+v", section)
	if !strings.Contains(encoded, documentSeed().baseURL) || !strings.Contains(encoded, documentSeed().model) {
		t.Fatalf("user layer after a restart = %s, want the console's endpoint and model", encoded)
	}
}

// TestConsoleModelSelectionSurvivesARestart is the other half of ADR 0103: the
// model an operator picks in the composer is console-written state like the
// endpoint, so a restart restores it for that session instead of falling back to
// whatever the host was launched with.
func TestConsoleModelSelectionSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	first := newDocumentHarness(t, path, documentSeed())
	profile := dshapi.ProviderProfile{
		Provider: "acme",
		API:      dshapi.ProtocolOpenAICompletions,
		BaseURL:  "https://acme.test/v1",
		Models:   []dshapi.ProviderModel{{ID: "acme-1"}},
	}
	if _, err := first.profiles.SetProviderProfile(profile); err != nil {
		t.Fatalf("declare the profile: %v", err)
	}
	chosen := dshapi.ModelSelection{Provider: "acme", Model: "acme-1"}
	if _, err := first.selections.SelectModel("session-1", chosen); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	file := readDocumentFile(t, path)
	stored, ok := file.ModelSelections["session-1"]
	if !ok {
		t.Fatalf("document modelSelections = %v, want the session the operator chose for", file.ModelSelections)
	}
	if stored.Provider != "acme" || stored.Model != "acme-1" {
		t.Fatalf("stored selection = %+v, want acme/acme-1", stored)
	}

	restarted := newDocumentHarness(t, path, documentSeed())
	state, ok := restarted.selections.States()["session-1"]
	if !ok || state.Projection.Next == nil {
		t.Fatalf("selection after the restart = %+v, want the chosen model projected for the session", state)
	}
	if state.Projection.Next.Provider != "acme" || state.Projection.Next.Model != "acme-1" {
		t.Fatalf("selection after the restart = %+v, want acme/acme-1", *state.Projection.Next)
	}
	// A session nobody chose for is absent rather than recorded as an empty
	// choice: a restart restores exactly the selections that were made.
	restarted.selections.RegisterSession("session-2")
	if _, recorded := readDocumentFile(t, path).ModelSelections["session-2"]; recorded {
		t.Error("a session with no chosen model was written to the document")
	}
}

// TestConsoleModelSelectionRefusedWhenTheDocumentWillNotTakeIt keeps the rule the
// settings document exists for: a choice that is live but not durable is the
// disagreement ADR 0102 removes, so a document that cannot be written puts the
// record back and the caller is told.
func TestConsoleModelSelectionRefusedWhenTheDocumentWillNotTakeIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	harness := newDocumentHarness(t, path, documentSeed())
	// A directory where the document goes: the writer cannot rename a file over it.
	if err := os.Mkdir(path, consoleSettingsDirMode); err != nil {
		t.Fatalf("make the path unwritable: %v", err)
	}
	if _, err := harness.selections.SelectModel("session-1", dshapi.ModelSelection{Provider: provider.OpenAI, Model: documentSeed().model}); err == nil {
		t.Fatal("SelectModel succeeded while the settings document could not be written")
	}
	state := harness.selections.States()["session-1"]
	if state.Projection.Next != nil {
		t.Fatalf("selection = %+v, want it rolled back after the document refused it", *state.Projection.Next)
	}
}

// readDocumentFile loads the document a test just wrote through the same reader a
// restart uses, so a test asserts what the host will actually read back.
func readDocumentFile(t *testing.T, path string) consoleSettingsFile {
	t.Helper()
	file, found, err := loadConsoleSettingsFile(path)
	if err != nil {
		t.Fatalf("load the document at %s: %v", path, err)
	}
	if !found {
		t.Fatalf("the document at %s does not exist", path)
	}
	return file
}

// TestConsoleUserLayerBelongsToTheCardItWasWrittenOn pins the other half of the
// user layer: a field belongs to the provider namespace the console wrote it
// through. Without the route, a value saved on one provider's card would be
// reported on whichever card the host happens to be configured with -- the same
// class of lie ADR 0103 removes.
func TestConsoleUserLayerBelongsToTheCardItWasWrittenOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	store, _ := newDocumentStores(t, path, documentSeed())
	if err := store.replaceModel(provider.Anthropic, "claude-x"); err != nil {
		t.Fatalf("replaceModel: %v", err)
	}
	section, found := store.SettingsUserSection(provider.Anthropic)
	if !found {
		t.Fatal("the anthropic card has no user layer after the console wrote its model")
	}
	if encoded := fmt.Sprintf("%+v", section); !strings.Contains(encoded, "claude-x") {
		t.Fatalf("anthropic user layer = %s, want the model written there", encoded)
	}
	if section, found := store.SettingsUserSection(provider.OpenAI); found {
		t.Fatalf("the openai card reports %v, want nothing: the console wrote the anthropic card", section)
	}

	// The card is part of what was saved, so it survives the restart with the
	// value.
	restarted, _ := newDocumentStores(t, path, documentSeed())
	if section, found := restarted.SettingsUserSection(provider.Anthropic); !found {
		t.Fatal("the anthropic card lost its user layer across a restart")
	} else if encoded := fmt.Sprintf("%+v", section); !strings.Contains(encoded, "claude-x") {
		t.Fatalf("anthropic user layer after a restart = %s, want the saved model", encoded)
	}
	if _, found := restarted.SettingsUserSection(provider.OpenAI); found {
		t.Fatal("the openai card reports a user layer after a restart, want it still absent")
	}
}

// TestRegisteredSessionWithoutAChoiceKeepsTheConfiguredModel is the regression the
// console's first prompt hit: a session is registered the moment it is created, so
// the projection has a key for it (ADR 0102), and a registered session with no
// choice must fall back to the configured model. Treating the record's presence as
// a choice installed an adapter for provider "" and failed every fresh session's
// first prompt with "provider \"\" is not one this host can route to".
func TestRegisteredSessionWithoutAChoiceKeepsTheConfiguredModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), consoleSettingsFileName)
	harness := newDocumentHarness(t, path, documentSeed())
	// The chosen session runs on a declared profile, and the host's one stored
	// credential is what an unnamed reference resolves to (ADR 0084, ADR 0095).
	harness.store.mu.Lock()
	harness.store.current.apiKey = "sk-acme-test"
	harness.store.mu.Unlock()
	// The live adapter is the settings page's, not a selection's: whatever it is
	// (here an unbuilt one, because the operator's own key is absent), reading a
	// session's route must leave it exactly as it was.
	before, beforeErr := harness.store.model.current()

	harness.selections.RegisterSession("session-fresh")
	route, ok, err := harness.selections.ModelRoute("session-fresh")
	if err != nil {
		t.Fatalf("ModelRoute for a registered session with no choice: %v", err)
	}
	if ok || route.Adapter != nil {
		t.Fatalf("route = %+v, want no route so the configured model serves the run", route)
	}
	// The record is still there, still with no choice, so the composer keeps its
	// projection key and the session is not silently forgotten.
	state, known := harness.selections.States()["session-fresh"]
	if !known {
		t.Fatal("the registered session is gone after applying its (empty) selection")
	}
	if state.Projection.Next != nil {
		t.Fatalf("projection = %+v, want no selection for a session that chose nothing", *state.Projection.Next)
	}

	// And a session that did choose still installs its own adapter: the flag must
	// not turn every choice into the fallback.
	if _, err := harness.profiles.SetProviderProfile(dshapi.ProviderProfile{
		Provider: "acme",
		API:      dshapi.ProtocolOpenAICompletions,
		BaseURL:  "https://acme.test/v1",
		Models:   []dshapi.ProviderModel{{ID: "acme-1"}},
	}); err != nil {
		t.Fatalf("declare the profile: %v", err)
	}
	if _, err := harness.selections.SelectModel("session-chosen", dshapi.ModelSelection{Provider: "acme", Model: "acme-1"}); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	chosen, ok, err := harness.selections.ModelRoute("session-chosen")
	if err != nil || !ok {
		t.Fatalf("ModelRoute for a chosen session = (%+v, %v, %v), want its own route", chosen, ok, err)
	}
	if chosen.Adapter == nil || chosen.Provider != "acme" || chosen.Model != "acme-1" {
		t.Fatalf("route = %+v, want exactly the chosen session's adapter", chosen)
	}
	// And the host's live adapter is untouched by either read: only the settings
	// page decides what that one is (ADR 0140).
	after, afterErr := harness.store.model.current()
	if after != before || (afterErr == nil) != (beforeErr == nil) {
		t.Fatalf("live adapter changed from (%v, %v) to (%v, %v)", before, beforeErr, after, afterErr)
	}
}

// declaredAfterBuiltins drops the two routes this host is built to serve, which are
// seeded into the pi-ai namespace before any hand-declared one (ADR 0122).
func declaredAfterBuiltins(t *testing.T, listed []dshapi.ProviderProfileStatus) []dshapi.ProviderProfileStatus {
	t.Helper()
	if len(listed) < 2 || listed[0].Profile.Provider != provider.OpenAI || listed[1].Profile.Provider != provider.Anthropic {
		t.Fatalf("profiles = %+v, want the built-in routes first", listed)
	}
	return listed[2:]
}
