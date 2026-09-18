package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/model/provider"
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
