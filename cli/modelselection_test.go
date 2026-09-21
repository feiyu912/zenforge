package cli

import (
	"strings"
	"testing"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
)

// catalogFixture is a host configured with one route and one declared provider,
// plus one declared provider whose credential is missing.
func catalogFixture(t *testing.T) (*consoleModels, *settingsStore, *consoleProviderProfiles) {
	t.Helper()
	settings := profileSettingsStore(t, "sk-host")
	profiles := newConsoleProviderProfiles(settings)
	t.Setenv("ACME_API_KEY", "sk-acme")
	t.Setenv("GATEWAY_TOKEN", "")
	for _, profile := range []dshapi.ProviderProfile{
		{Provider: "acme", DisplayName: "Acme Gateway", API: dshapi.ProtocolOpenAICompletions,
			BaseURL: "https://gateway.acme.example/v1", APIKeyEnv: "ACME_API_KEY",
			Models: []dshapi.ProviderModel{{ID: "acme-large", Name: "Acme Large"}, {ID: "acme-think"}}},
		{Provider: "broken", DisplayName: "Broken Gate", API: dshapi.ProtocolOpenAICompletions,
			BaseURL: "https://broken.example/v1", APIKeyEnv: "GATEWAY_TOKEN",
			Models: []dshapi.ProviderModel{{ID: "broken-1"}}},
	} {
		if _, err := profiles.SetProviderProfile(profile); err != nil {
			t.Fatalf("SetProviderProfile(%s): %v", profile.Provider, err)
		}
	}
	// A settings store starts unbuilt; the serve command rebuilds it at startup.
	settings.rebuild()
	models := consoleModels{settings: settings, profiles: profiles}
	return &models, settings, profiles
}

// The catalog is what the picker renders: the configured route and every declared
// profile appear as groups, a route this host cannot serve appears in failures
// with its reason, and only routes it can serve are routable.
func TestCatalogListsTheConfiguredRouteAndDeclaredProviders(t *testing.T) {
	models, _, _ := catalogFixture(t)
	catalog := models.Catalog()
	groups := make([]string, 0, len(catalog.Groups))
	for _, group := range catalog.Groups {
		groups = append(groups, group.ID)
	}
	if strings.Join(groups, ",") != "openai,acme,broken" {
		t.Fatalf("groups = %v, want the configured route and both declared providers", groups)
	}
	// A route with a missing credential is still registered, the way upstream
	// registers adapters and reports authentication at request time; the reason it
	// cannot serve is carried in failures so the operator can repair it, and the
	// composer stays usable in the meantime.
	if strings.Join(catalog.RoutableProviders, ",") != "openai,acme,broken" {
		t.Fatalf("routable = %v, want every registered route", catalog.RoutableProviders)
	}
	if len(catalog.Failures) != 1 || catalog.Failures[0].ID != "broken" {
		t.Fatalf("failures = %+v, want the route whose credential is missing", catalog.Failures)
	}
	if !strings.Contains(catalog.Failures[0].Message, "GATEWAY_TOKEN") {
		t.Fatalf("failure message = %q, want the missing credential named", catalog.Failures[0].Message)
	}
	if catalog.Default.Provider != provider.OpenAI || catalog.Default.Model != "qwen-plus" {
		t.Fatalf("default = %+v, want the operator's configured model", catalog.Default)
	}
	acme := catalog.Groups[1]
	if len(acme.Models) != 2 || acme.Models[0].ID != "acme-large" || acme.Models[0].Name != "Acme Large" {
		t.Fatalf("acme models = %+v, want the declared models with their names", acme.Models)
	}
	if acme.Models[1].Name != "acme-think" {
		t.Fatalf("a model with no declared name must fall back to its id: %+v", acme.Models[1])
	}
}

// A host with nothing configured still offers what it can serve, so an operator
// who declared a provider has something for the picker to show.
func TestCatalogFallsBackToADeclaredDefault(t *testing.T) {
	settings := &settingsStore{
		current: serverSettings{provider: provider.OpenAI, apiKey: "sk-host"},
		model:   newSwappableModel(),
	}
	profiles := newConsoleProviderProfiles(settings)
	t.Setenv("ACME_API_KEY", "sk-acme")
	if _, err := profiles.SetProviderProfile(dshapi.ProviderProfile{
		Provider: "acme", API: dshapi.ProtocolOpenAICompletions, BaseURL: "https://acme.example/v1",
		APIKeyEnv: "ACME_API_KEY", Models: []dshapi.ProviderModel{{ID: "acme-large"}},
	}); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	catalog := (consoleModels{settings: settings, profiles: profiles}).Catalog()
	if catalog.Default.Provider != "acme" || catalog.Default.Model != "acme-large" {
		t.Fatalf("default = %+v, want the first routable declared model", catalog.Default)
	}
	if len(catalog.Groups) != 1 || len(catalog.RoutableProviders) != 1 {
		t.Fatalf("catalog = %+v, want only the declared provider", catalog)
	}
}

func TestOffersAndAdapterForAnswerForEveryRouteItLists(t *testing.T) {
	models, _, _ := catalogFixture(t)
	// Membership refusals: a route this host does not know, and a model the route
	// does not offer. Both are what the picker would have prevented.
	for _, testCase := range []struct {
		name      string
		selection dshapi.ModelSelection
		wantError string
	}{
		{"a route this host does not know", dshapi.ModelSelection{Provider: "nope", Model: "m"}, "not one this host can route to"},
		{"a model the route does not offer", dshapi.ModelSelection{Provider: "acme", Model: "nope"}, "is not one provider"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := models.Offers(testCase.selection)
			if err == nil {
				t.Fatalf("Offers(%+v) = nil, want a refusal", testCase.selection)
			}
			if !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("error = %q, want it to mention %q", err, testCase.wantError)
			}
			if _, err := models.AdapterFor(testCase.selection); err == nil {
				t.Fatal("AdapterFor accepted what Offers refused")
			}
		})
	}
	// A route whose credential is missing is offered -- it is registered, and the
	// console keeps the selection -- but it cannot be built into an adapter, which
	// is the honest answer a run needs.
	broken := dshapi.ModelSelection{Provider: "broken", Model: "broken-1"}
	if err := models.Offers(broken); err != nil {
		t.Fatalf("Offers(%+v) = %v, want a registered route to be selectable", broken, err)
	}
	adapter, err := models.AdapterFor(broken)
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_TOKEN") {
		t.Fatalf("AdapterFor(%+v) = (%v, %v), want the missing credential named", broken, adapter, err)
	}
	// Both serveable routes build, including the declared one the console added.
	for _, selection := range []dshapi.ModelSelection{
		{Provider: provider.OpenAI, Model: "qwen-plus"},
		{Provider: "acme", Model: "acme-think"},
	} {
		adapter, err := models.AdapterFor(selection)
		if err != nil || adapter == nil {
			t.Fatalf("AdapterFor(%+v) = (%v, %v), want an adapter", selection, adapter, err)
		}
	}
	// With nothing configured and nothing declared, the host says so instead of
	// listing nothing silently.
	empty := consoleModels{settings: &settingsStore{current: serverSettings{provider: provider.OpenAI}, model: newSwappableModel()}}
	err = empty.Offers(dshapi.ModelSelection{Provider: provider.OpenAI, Model: "gpt-4.1"})
	if err == nil || !strings.Contains(err.Error(), "no provider is configured or declared yet") {
		t.Fatalf("error = %q, want the empty host explained", err)
	}
}

// The credential rule: a profile naming the reference the console's card derives
// gets the key the operator typed for it, and a profile naming anything else is
// resolved from that environment variable alone. Borrowing the host's own key for
// an arbitrary endpoint would send the operator's credential somewhere they never
// entered it.
func TestDeclaredProfileCredentialsFollowTheConsoleReference(t *testing.T) {
	settings := profileSettingsStore(t, "sk-host")
	settings.rebuild()
	profiles := newConsoleProviderProfiles(settings)
	t.Setenv("GATEWAY_TOKEN", "")
	t.Setenv("ACME_API_KEY", "")
	// acme-gw derives ACME_GW_API_KEY, which is not what this profile names.
	if _, err := profiles.SetProviderProfile(dshapi.ProviderProfile{
		Provider: "acme-gw", API: dshapi.ProtocolOpenAICompletions, BaseURL: "https://gw.example/v1",
		APIKeyEnv: "GATEWAY_TOKEN", Models: []dshapi.ProviderModel{{ID: "m"}},
	}); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	foreign := profileByRoute(t, profiles.ProviderProfiles(), "acme-gw")
	if foreign.Error == "" {
		t.Fatal("a profile naming a foreign reference borrowed the host's credential")
	}
	if !strings.Contains(foreign.Error, "GATEWAY_TOKEN") {
		t.Fatalf("error = %q, want the named reference", foreign.Error)
	}
	// The derived reference is where the card's credentials/set put the key, so it
	// resolves from the host's stored credential.
	if _, err := profiles.SetProviderProfile(dshapi.ProviderProfile{
		Provider: "acme", API: dshapi.ProtocolOpenAICompletions, BaseURL: "https://acme.example/v1",
		APIKeyEnv: consoleDerivedKeyRef("acme"), Models: []dshapi.ProviderModel{{ID: "m"}},
	}); err != nil {
		t.Fatalf("SetProviderProfile: %v", err)
	}
	derived := profileByRoute(t, profiles.ProviderProfiles(), "acme")
	if derived.Error != "" {
		t.Fatalf("error = %q, want the derived reference to resolve from the host's key", derived.Error)
	}
	if got := consoleDerivedKeyRef("acme-gw"); got != "ACME_GW_API_KEY" {
		t.Fatalf("consoleDerivedKeyRef(acme-gw) = %q, want ACME_GW_API_KEY", got)
	}
}

func TestSelectModelRecordsAndPublishesTheSelection(t *testing.T) {
	models, settings, _ := catalogFixture(t)
	selections := newConsoleModelSelection(settings, *models)
	var observed []dshstream.ModelSelectionUpdate
	unsubscribe := selections.Updates(func(update dshstream.ModelSelectionUpdate) {
		observed = append(observed, update)
	})
	selected, err := selections.SelectModel("run-1", dshapi.ModelSelection{Provider: "acme", Model: "acme-large"})
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if selected.Provider != "acme" || selected.Model != "acme-large" {
		t.Fatalf("selected = %+v, want what the picker submitted", selected)
	}
	states := selections.States()
	state, ok := states["run-1"]
	if !ok || state.Projection.Next == nil || state.Projection.Next.Model != "acme-large" {
		t.Fatalf("states = %+v, want the recorded next selection", states)
	}
	if state.Projection.LastUsed != nil {
		t.Fatalf("lastUsed = %+v, want null until a run consumed it", state.Projection.LastUsed)
	}
	if state.Seq == 0 {
		t.Fatal("seq = 0, want a monotone sequence so a client can order updates")
	}
	if len(observed) != 1 || observed[0].SessionID != "run-1" {
		t.Fatalf("observed = %+v, want one live update", observed)
	}
	unsubscribe()
	if _, err := selections.SelectModel("run-2", dshapi.ModelSelection{Provider: "acme", Model: "acme-think"}); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if len(observed) != 1 {
		t.Fatalf("observed = %+v, want no update after unsubscribing", observed)
	}
}

func TestSelectModelRefusesWhatTheHostCannotServe(t *testing.T) {
	models, settings, _ := catalogFixture(t)
	selections := newConsoleModelSelection(settings, *models)
	if _, err := selections.SelectModel("run-1", dshapi.ModelSelection{Provider: "acme", Model: "acme-large", ReasoningEffort: "high"}); err == nil {
		t.Fatal("an effort was accepted by a host that exposes none")
	}
	// A registered route whose credential is missing is recorded: the console
	// keeps the selection and the run reports the credential (see the apply test).
	if _, err := selections.SelectModel("run-1", dshapi.ModelSelection{Provider: "broken", Model: "broken-1"}); err != nil {
		t.Fatalf("SelectModel on a registered route: %v", err)
	}
	if _, err := selections.SelectModel("run-1", dshapi.ModelSelection{Provider: "acme", Model: "nope"}); err == nil {
		t.Fatal("a model the route does not offer was recorded")
	}
	if len(selections.States()) != 1 {
		t.Fatalf("states = %+v, want only the registered route recorded", selections.States())
	}
}

// Applying is what makes a selection real: the session's own adapter goes in, and
// a session that chose nothing gets the operator's configured adapter back rather
// than inheriting the previous session's choice.
func TestApplyInstallsTheSelectedAdapterAndRestoresTheConfiguredOne(t *testing.T) {
	models, settings, _ := catalogFixture(t)
	selections := newConsoleModelSelection(settings, *models)
	var installed []model.Model
	selections.install = func(adapter model.Model) { installed = append(installed, adapter) }

	if _, err := selections.SelectModel("run-1", dshapi.ModelSelection{Provider: "acme", Model: "acme-large"}); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if err := selections.ApplyModelSelection("run-1"); err != nil {
		t.Fatalf("ApplyModelSelection: %v", err)
	}
	if len(installed) != 1 || installed[0] == nil {
		t.Fatalf("installed = %v, want the selected provider's adapter", installed)
	}
	state := selections.States()["run-1"]
	if state.Projection.LastUsed == nil || state.Projection.LastUsed.Model != "acme-large" {
		t.Fatalf("state = %+v, want the run to have consumed the selection", state)
	}
	// A fresh session has no selection: the configured adapter is rebuilt and
	// nothing else is installed.
	if err := selections.ApplyModelSelection("run-2"); err != nil {
		t.Fatalf("ApplyModelSelection: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("installed = %v, want no session adapter for a session that chose none", installed)
	}
	adapter, err := settings.model.current()
	if err != nil || adapter == nil {
		t.Fatalf("configured adapter = (%v, %v), want the operator's own model in place", adapter, err)
	}
}

func TestApplyRefusesWhenTheCredentialIsGone(t *testing.T) {
	models, settings, _ := catalogFixture(t)
	selections := newConsoleModelSelection(settings, *models)
	if _, err := selections.SelectModel("run-1", dshapi.ModelSelection{Provider: "acme", Model: "acme-large"}); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	// The credential the profile names is the only thing that can serve it.
	if err := settings.clearAPIKey(); err != nil {
		t.Fatalf("clearAPIKey: %v", err)
	}
	t.Setenv("ACME_API_KEY", "")
	err := selections.ApplyModelSelection("run-1")
	if err == nil {
		t.Fatal("the run was allowed to start with no credential for its selected provider")
	}
	if !strings.Contains(err.Error(), "ACME_API_KEY") {
		t.Fatalf("error = %q, want the missing credential named", err)
	}
}
