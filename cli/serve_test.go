package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/internal/dshmount"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// The run and settings APIs have no authentication, so an address that reaches
// the network has to be an explicit decision. This asserts the refusal happens
// before anything is bound or built, and that the operator is told how to opt
// in rather than being left with a bare "permission denied".
func TestServeRefusesANonLoopbackAddressWithoutTheOptIn(t *testing.T) {
	var stderr bytes.Buffer
	err := serveCommand(context.Background(), []string{"--addr", "0.0.0.0:8787"}, IO{Stderr: &stderr})
	if err == nil {
		t.Fatal("serve accepted a non-loopback address without --allow-remote")
	}
	if !strings.Contains(err.Error(), "--allow-remote") {
		t.Errorf("refusal does not name the opt-in flag: %v", err)
	}
}

func TestServeLoopbackDetection(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8787", "[::1]:8787", "localhost:8787"} {
		if !isLoopbackListenAddr(addr) {
			t.Errorf("%q is loopback but was not treated as one", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:8787", "192.168.1.10:8787"} {
		if isLoopbackListenAddr(addr) {
			t.Errorf("%q reaches the network but was treated as loopback", addr)
		}
	}
}

// The settings endpoint is the one place a credential passes through, so the
// key must never come back out: hasApiKey is the only thing a page or an
// operator learns, and a client that can read the key could exfiltrate it.
func TestSettingsViewNeverRevealsTheKey(t *testing.T) {
	store := newTestSettingsStore(t, false)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	request.RemoteAddr = "127.0.0.1:51234"
	store.serveHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, testSettingsKey) {
		t.Fatalf("the settings response revealed the API key: %s", body)
	}
	var view settingsView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("decode settings view: %v", err)
	}
	if !view.HasAPIKey {
		t.Error("hasApiKey is false although a key is configured")
	}
	if view.Model != "qwen-plus" || view.BaseURL != testSettingsBaseURL {
		t.Errorf("view = %+v, want the configured endpoint", view)
	}
}

// An empty key means "keep the one you have": the page is never shown the key,
// so it cannot send it back, and a model change must not silently drop it.
func TestSettingsApplyKeepsTheStoredKeyWhenTheFieldIsEmpty(t *testing.T) {
	store := newTestSettingsStore(t, false)
	view, err := store.apply(settingsRequest{BaseURL: "https://api.example.invalid/v1", Model: "qwen-max"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if view.BaseURL != "https://api.example.invalid/v1" || view.Model != "qwen-max" {
		t.Errorf("view = %+v, want the new endpoint and model", view)
	}
	if !view.HasAPIKey {
		t.Error("an empty apiKey dropped the stored key")
	}
	store.mu.RLock()
	got := store.current.apiKey
	store.mu.RUnlock()
	if got != testSettingsKey {
		t.Errorf("stored key = %q, want it unchanged", got)
	}
}

// A typo in the endpoint or provider must fail at the settings call rather than
// at the next run, where the failure would look like a model outage.
func TestSettingsApplyRefusesWhatItCannotUse(t *testing.T) {
	store := newTestSettingsStore(t, false)
	for _, request := range []settingsRequest{
		{Provider: "gemini"},
		{BaseURL: "not-a-url"},
		{BaseURL: "ftp://example.invalid/v1"},
	} {
		if _, err := store.apply(request); err == nil {
			t.Errorf("%+v was accepted", request)
		}
	}
}

// --allow-remote is what makes remote settings changes possible, and without
// it the gate is what stops a stray page from pointing runs at an endpoint the
// operator did not choose.
func TestSettingsRefusesRemoteChangesWithoutTheOptIn(t *testing.T) {
	store := newTestSettingsStore(t, false)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"model":"qwen-max"}`))
	request.RemoteAddr = "203.0.113.5:1234"
	store.serveHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("remote settings change = %d, want 403", recorder.Code)
	}
	store.mu.RLock()
	got := store.current.model
	store.mu.RUnlock()
	if got != "qwen-plus" {
		t.Errorf("a refused request changed the model to %q", got)
	}
}

func TestSettingsAllowsRemoteChangesWhenTheFlagWasGiven(t *testing.T) {
	store := newTestSettingsStore(t, true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(`{"model":"qwen-max"}`))
	request.RemoteAddr = "203.0.113.5:1234"
	store.serveHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("remote settings change with --allow-remote = %d (%s), want 200", recorder.Code, recorder.Body.String())
	}
	store.mu.RLock()
	got := store.current.model
	store.mu.RUnlock()
	if got != "qwen-max" {
		t.Errorf("model = %q, want the posted one", got)
	}
}

const (
	testSettingsBaseURL = "https://example.invalid/v1"
	testSettingsKey     = "secret-key-value"
)

// newTestSettingsStore builds the store the endpoint serves, without the run
// manager around it: the settings contract is about what the store accepts and
// reveals, and testing it through a fully assembled server would drag in a
// workspace, stores, and a provider key for no added coverage.
func newTestSettingsStore(t *testing.T, allowRemote bool) *settingsStore {
	t.Helper()
	return &settingsStore{
		current: serverSettings{
			baseURL:  testSettingsBaseURL,
			model:    "qwen-plus",
			provider: provider.OpenAI,
			apiKey:   testSettingsKey,
		},
		model:       newSwappableModel(),
		allowRemote: allowRemote,
	}
}

// The DSH console is the only product surface serve offers, so "/" must answer
// the boot-injected upstream shell and nothing else may answer a console. This
// pins the route table: the removed first-party console left no /classic/ or
// root asset aliases behind, while the settings and server endpoints keep their
// exact routes beside the console's /api mount.
func TestServeMountsTheDSHConsoleAtTheRootAndNothingElse(t *testing.T) {
	store := memory.New()
	manager := harnesshttp.NewRunManager(nil, store, eventlog.NewBus(), harnesshttp.RunManagerOptions{})
	console, err := dshmount.New(manager, store, dshmount.Config{})
	if err != nil {
		t.Fatalf("build the DSH console mount: %v", err)
	}
	mux := newServeMux(serveMuxConfig{
		registerHarness: func(*http.ServeMux) {},
		settings:        newTestSettingsStore(t, false),
		workspace:       "/tmp/zenforge-serve-workspace",
		allowRemote:     false,
		dsh:             console,
	})

	// "/" is the injected DSH shell, not the raw shell and not a first-party one.
	root := httptest.NewRecorder()
	mux.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", root.Code)
	}
	shell := root.Body.String()
	if !strings.Contains(shell, "<title>ZenForge</title>") {
		t.Error("GET / is not the ZenForge DSH shell")
	}
	if !strings.Contains(shell, "__DSH_BOOT__") || !strings.Contains(shell, "__DSH_BOOT_READY__") {
		t.Error("GET / served a shell without the injected boot graph and readiness tail")
	}
	if strings.Contains(shell, "zenforge console") {
		t.Error("GET / served the first-party console, not the DSH shell")
	}

	// The interim first-party console is gone, so its route and its two root
	// asset aliases fall through to the console mount's own 404. Asserting the
	// status (not just "not 200") proves the route was removed rather than
	// redirected to some other console.
	for _, target := range []string{"/classic/", "/classic/assets/app.js", "/assets/app.js", "/assets/style.css"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 after the first-party console was removed", target, recorder.Code)
		}
	}

	// A fingerprinted DSH asset the shell names must resolve through the root
	// catch-all, which is what proves /assets/ still serves the DSH console.
	refs := regexp.MustCompile(`\./assets/[^"]+`).FindAllString(shell, -1)
	if len(refs) == 0 {
		t.Fatal("the DSH shell names no assets")
	}
	for _, ref := range refs {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(ref, "."), nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", ref, recorder.Code)
		}
	}

	// The settings and server endpoints keep their exact routes beside the
	// console's /api mount.
	server := httptest.NewRecorder()
	mux.ServeHTTP(server, httptest.NewRequest(http.MethodGet, "/api/server", nil))
	if server.Code != http.StatusOK {
		t.Fatalf("GET /api/server = %d, want 200", server.Code)
	}
	if !strings.Contains(server.Body.String(), "zenforge-serve-workspace") {
		t.Errorf("GET /api/server did not report the workspace: %s", server.Body.String())
	}
	settings := httptest.NewRecorder()
	mux.ServeHTTP(settings, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if settings.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200", settings.Code)
	}
}

// The console's Models page writes a key through the credentials namespace, so
// the adapter must land on the settings store without disturbing anything else
// the operator configured. The regression this pins is specific: apply assigns
// the base URL unconditionally, so routing a credential write through it would
// silently clear --base-url and send later runs to the provider default.
func TestConsoleCredentialsStoreAKeyWithoutClearingTheEndpoint(t *testing.T) {
	store := newTestSettingsStore(t, false)
	credentials := consoleCredentials{settings: store}
	if !credentials.CredentialConfigured() {
		t.Fatal("CredentialConfigured = false, want the fixture's key reported")
	}
	if err := credentials.StoreCredential("sk-second-value"); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}
	view := store.view()
	if view.BaseURL != testSettingsBaseURL {
		t.Fatalf("baseUrl = %q, want %q: storing a key must not clear the endpoint", view.BaseURL, testSettingsBaseURL)
	}
	if view.Model != "qwen-plus" {
		t.Fatalf("model = %q, want the configured model kept", view.Model)
	}
	if !view.HasAPIKey {
		t.Fatal("hasApiKey = false, want the stored key reported")
	}
	if encoded := fmt.Sprintf("%+v", view); strings.Contains(encoded, "sk-second-value") {
		t.Fatalf("the view carries the key: %s", encoded)
	}
	if err := credentials.RemoveCredential(); err != nil {
		t.Fatalf("RemoveCredential: %v", err)
	}
	if store.view().HasAPIKey {
		t.Fatal("hasApiKey = true after removal, want the inline key forgotten")
	}
}

// Removing the stored key must not claim the host has no credential when the
// process was started with an environment-supplied one: the console's badge
// would then contradict what the run path actually uses.
func TestConsoleCredentialsRespectAnEnvironmentSuppliedKey(t *testing.T) {
	t.Setenv("ZENFORGE_TEST_CONSOLE_KEY", "env-key-value")
	store := newTestSettingsStore(t, false)
	store.current.apiKey = ""
	store.current.apiKeyEnv = "ZENFORGE_TEST_CONSOLE_KEY"
	credentials := consoleCredentials{settings: store}
	if !credentials.CredentialConfigured() {
		t.Fatal("CredentialConfigured = false, want the environment-supplied key reported")
	}
	if err := credentials.RemoveCredential(); err != nil {
		t.Fatalf("RemoveCredential: %v", err)
	}
	if !store.view().HasAPIKey {
		t.Fatal("hasApiKey = false, want the environment-supplied key still reported")
	}
}
