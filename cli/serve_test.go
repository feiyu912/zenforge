package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(shell, "<title>zenforge</title>") {
		t.Error("GET / is not the zenforge DSH shell")
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

// The console's Models page writes the endpoint before it writes the key, so a
// host with no credential must accept the endpoint. Refusing it for the absence
// of a key -- which is what routing through apply would do -- makes the panel's
// own write order impossible.
func TestConsoleSettingsWriteTheEndpointBeforeTheKey(t *testing.T) {
	store := newTestSettingsStore(t, false)
	store.current.apiKey = ""
	settings := consoleSettings{settings: store}
	if err := settings.SetSettingsEndpoint(provider.OpenAI, "https://example.test/v1"); err != nil {
		t.Fatalf("SetSettingsEndpoint without a key: %v", err)
	}
	if got := store.view().BaseURL; got != "https://example.test/v1" {
		t.Fatalf("baseUrl = %q, want the endpoint committed", got)
	}
	if store.view().HasAPIKey {
		t.Fatal("hasApiKey = true, want no credential reported")
	}
	// The model write keeps the endpoint that was just written.
	if err := settings.SetSettingsModel(provider.OpenAI, "qwen-max"); err != nil {
		t.Fatalf("SetSettingsModel: %v", err)
	}
	view := store.view()
	if view.Model != "qwen-max" || view.BaseURL != "https://example.test/v1" {
		t.Fatalf("view = %+v, want the model changed and the endpoint kept", view)
	}
	// A malformed endpoint is still refused before anything is stored.
	if err := settings.SetSettingsEndpoint(provider.OpenAI, "not-a-url"); err == nil {
		t.Fatal("SetSettingsEndpoint accepted a malformed endpoint")
	}
	if got := store.view().BaseURL; got != "https://example.test/v1" {
		t.Fatalf("baseUrl = %q, want the refused write to have changed nothing", got)
	}
	// And a key written afterwards completes the configuration.
	if err := settings.SetSettingsKey("sk-later"); err != nil {
		t.Fatalf("SetSettingsKey: %v", err)
	}
	if !store.view().HasAPIKey {
		t.Fatal("hasApiKey = false after writing a key")
	}
}

// A console-owned settings namespace is held by the same store as the rest of the
// console-written settings, and the section it hands back is a copy: a caller
// cannot reach into the store through it.
func TestSettingsStoreHoldsConsoleSections(t *testing.T) {
	store := newTestSettingsStore(t, false)
	if got := store.ConsoleSection("ui-onboarding"); len(got) != 0 {
		t.Fatalf("ConsoleSection before any write = %v, want an empty section", got)
	}
	if err := store.SetConsoleSection("ui-onboarding", map[string]any{"welcomeNoticeVersion": "2026-08-13.1"}); err != nil {
		t.Fatalf("SetConsoleSection: %v", err)
	}
	section := store.ConsoleSection("ui-onboarding")
	if section["welcomeNoticeVersion"] != "2026-08-13.1" {
		t.Fatalf("section = %v, want the stored acknowledgement", section)
	}
	// The returned map is a copy, or a caller could edit the store's state.
	section["welcomeNoticeVersion"] = "tampered"
	if store.ConsoleSection("ui-onboarding")["welcomeNoticeVersion"] != "2026-08-13.1" {
		t.Fatal("the section handed back aliases the store's state")
	}
	// An empty section reads back the same way as one that was never written,
	// which is what a cleared acknowledgement is.
	if err := store.SetConsoleSection("ui-onboarding", map[string]any{}); err != nil {
		t.Fatalf("SetConsoleSection(empty): %v", err)
	}
	if got := store.ConsoleSection("ui-onboarding"); len(got) != 0 {
		t.Fatalf("section after clearing = %v, want empty", got)
	}
	if got := store.ConsoleSection("ui-theme"); len(got) != 0 {
		t.Fatalf("an unrelated namespace = %v, want empty", got)
	}
}

// The console's adapter passes the console-owned namespace straight through to
// the store, so the notice's write and the notice's read see the same state.
func TestConsoleSettingsPassTheOnboardingNamespaceThrough(t *testing.T) {
	settings := consoleSettings{settings: newTestSettingsStore(t, false)}
	if err := settings.SetConsoleSection("ui-onboarding", map[string]any{"welcomeNoticeVersion": "2026-08-13.1"}); err != nil {
		t.Fatalf("SetConsoleSection: %v", err)
	}
	if got := settings.ConsoleSection("ui-onboarding")["welcomeNoticeVersion"]; got != "2026-08-13.1" {
		t.Fatalf("value = %v, want the acknowledgement the console wrote", got)
	}
}

// TestServeKeepsItsRunRegistryOnDisk pins the wiring a durable sidebar needs:
// the console lists sessions from the run registry, so the host configures one
// in its checkpoint directory instead of the run manager's in-process records
// (ADR 0109). Without it the sidebar forgets every conversation at the ten
// minute terminal retention and every restart, while the transcripts stay on
// disk.
func TestServeKeepsItsRunRegistryOnDisk(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()
	state := t.TempDir()
	opts := defaultOptions()
	opts.workspace = workspace
	opts.checkpointDir = filepath.Join(state, "runs")
	opts.apiKey = "sk-test-key"
	ioStreams := IO{Stdout: io.Discard, Stderr: io.Discard}

	app, err := newServeApp(context.Background(), &opts, ioStreams, serveConfig{
		settingsFile: filepath.Join(state, "console-settings.json"),
	})
	if err != nil {
		t.Fatalf("newServeApp: %v", err)
	}
	t.Cleanup(func() {
		_ = app.Close(context.Background())
		drainClosers(&opts, ioStreams)
	})

	registryPath := filepath.Join(opts.checkpointDir, "run-registry.sqlite")
	if _, err := os.Stat(registryPath); err != nil {
		t.Fatalf("the host kept no durable run registry at %s: %v", registryPath, err)
	}
	// A file is not a registry: reopening it must answer, which is what the next
	// process does when it lists the sessions this one served.
	registry, err := harnesshttp.OpenSQLiteRunRegistry(context.Background(), registryPath)
	if err != nil {
		t.Fatalf("reopen the host's registry: %v", err)
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.List(context.Background()); err != nil {
		t.Fatalf("list through the host's registry: %v", err)
	}
}

// TestServeAdoptsTheRunsAlreadyInItsStateDirectory covers the operator whose
// conversations predate the durable registry: their transcripts are on disk and
// a fresh registry lists nothing, so the host records the stored runs at startup.
// The run here is real -- a prompt through the console's own handler, answered by
// a stub provider -- so the test also proves a conversation survives a restart
// through the real serve seam, not only through the adapter (ADR 0109).
func TestServeAdoptsTheRunsAlreadyInItsStateDirectory(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	model := newOpenAISSEStub(t, textChunk("recorded answer"))
	opts := servedRunOptions(t, model.url)
	settingsFile := filepath.Join(t.TempDir(), "console-settings.json")
	ioStreams := servedRunStreams()
	build := func() *serveApp {
		t.Helper()
		app, err := newServeApp(context.Background(), &opts, ioStreams, serveConfig{settingsFile: settingsFile})
		if err != nil {
			t.Fatalf("newServeApp: %v", err)
		}
		t.Cleanup(func() { _ = app.Close(context.Background()) })
		return app
	}

	first := build()
	sessionID := consoleSessionID(t, first)
	consolePrompt(t, first, sessionID, "first question")
	waitForConsoleSession(t, first, sessionID)

	// The host stops, the way an install that predates the registry did: the
	// transcripts stay and no registry row survives.
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("close the first host: %v", err)
	}
	registryPath, err := runRegistryPath(&opts)
	if err != nil {
		t.Fatalf("runRegistryPath: %v", err)
	}
	if err := os.Remove(registryPath); err != nil {
		t.Fatalf("remove the registry: %v", err)
	}

	second := build()
	item := findConsoleSession(t, second, sessionID)
	if item == nil {
		t.Fatal("the restarted host does not list the conversation it already served")
	}
	if item.Running {
		t.Fatalf("an adopted conversation is listed as running: %+v", item)
	}
}

// TestServeCarriesThePromptsRequestIdentity walks the whole chain the console
// depends on: it prompts through the console's own route, the real agent records
// the run with the identity the prompt RPC carried, and the page the console
// reads names that identity on the durable user message, which is what retires
// the local echo of the submission (ADR 0111).
func TestServeCarriesThePromptsRequestIdentity(t *testing.T) {
	t.Setenv("ZENFORGE_CONFIG_DIR", t.TempDir())
	model := newOpenAISSEStub(t, textChunk("an answer"))
	opts := servedRunOptions(t, model.url)
	app, err := newServeApp(context.Background(), &opts, servedRunStreams(), serveConfig{settingsFile: filepath.Join(t.TempDir(), "console-settings.json")})
	if err != nil {
		t.Fatalf("newServeApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })

	sessionID := consoleSessionID(t, app)
	consolePrompt(t, app, sessionID, "a recorded question")
	waitForConsoleSession(t, app, sessionID)

	recorder := consolePost(t, app, "session/page",
		fmt.Sprintf(`{"address":{"kind":"session","sessionId":%q},"throughSeq":-1,"maxMessages":50}`, sessionID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("session/page = %d: %s", recorder.Code, recorder.Body.String())
	}
	type pageRecord struct {
		Event struct {
			Type string `json:"type"`
			Data struct {
				// A record's source is whatever its own type declares: a map
				// for a message, a string for the title's own provenance.
				Source any `json:"source"`
			} `json:"data"`
		} `json:"event"`
	}
	var envelope struct {
		Result struct {
			Value struct {
				Records []pageRecord `json:"records"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	found := false
	for _, record := range envelope.Result.Value.Records {
		if record.Event.Type != "user/message" {
			continue
		}
		found = true
		source, _ := record.Event.Data.Source.(map[string]any)
		if identity, _ := source["rpcId"].(string); identity != "req-1" {
			t.Fatalf("prompt source = %v, want the requestId the console prompted with", source)
		}
	}
	if !found {
		t.Fatalf("the page carries no user message: %s", recorder.Body.String())
	}
}

// consoleSessionID creates a session through the console's own route.
func consoleSessionID(t *testing.T, app *serveApp) string {
	t.Helper()
	recorder := consolePost(t, app, "session/create", "{}")
	var value struct {
		SessionID string `json:"sessionId"`
	}
	decodeConsoleValue(t, recorder, &value)
	if value.SessionID == "" {
		t.Fatalf("session/create returned no id: %s", recorder.Body.String())
	}
	return value.SessionID
}

// consolePrompt sends one turn through the console's own route.
func consolePrompt(t *testing.T, app *serveApp, sessionID, text string) {
	t.Helper()
	body := fmt.Sprintf(`{"requestId":"req-1","sessionId":%q,"mode":"queue","content":[{"type":"text","text":%q}]}`, sessionID, text)
	recorder := consolePost(t, app, "session/prompt", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("session/prompt = %d: %s", recorder.Code, recorder.Body.String())
	}
}

// waitForConsoleSession blocks until the session is listed and no longer running,
// which is what the console shows as a finished turn.
func waitForConsoleSession(t *testing.T, app *serveApp, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		item := findConsoleSession(t, app, sessionID)
		if item != nil && !item.Running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s never finished its turn", sessionID)
}

// findConsoleSession lists sessions through the console's own route.
func findConsoleSession(t *testing.T, app *serveApp, sessionID string) *consoleListItem {
	t.Helper()
	recorder := consolePost(t, app, "session/list", "{}")
	var value struct {
		Items []consoleListItem `json:"items"`
	}
	decodeConsoleValue(t, recorder, &value)
	for index := range value.Items {
		if value.Items[index].SessionID == sessionID {
			return &value.Items[index]
		}
	}
	return nil
}

type consoleListItem struct {
	SessionID string `json:"sessionId"`
	Running   bool   `json:"running"`
	Title     string `json:"title"`
}

func consolePost(t *testing.T, app *serveApp, method, args string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"type":"client-request","rpcId":"rpc-%s","method":%q,"payload":{"args":%s}}`, method, method, args)
	request := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	request.Host = "127.0.0.1:8787"
	request.RemoteAddr = "127.0.0.1:54321"
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func decodeConsoleValue(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Result struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the console envelope: %v: %s", err, recorder.Body.String())
	}
	if !envelope.Result.OK {
		message := "no error reported"
		if envelope.Result.Error != nil {
			message = envelope.Result.Error.Message
		}
		t.Fatalf("the console request failed: %s", message)
	}
	if err := json.Unmarshal(envelope.Result.Value, target); err != nil {
		t.Fatalf("decode the console value: %v: %s", err, envelope.Result.Value)
	}
}
