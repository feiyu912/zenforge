package cli

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
)

// consoleModels answers what this host can serve and with which adapter: the
// route the operator configured, plus every provider profile the console
// declared (ADR 0095). The catalog the picker renders and the resolution a
// selection goes through are the same list, so a model the catalog offers is a
// model a run can use -- and a route the host cannot serve appears in `failures`
// with its reason instead of being quietly absent.
type consoleModels struct {
	settings *settingsStore
	profiles *consoleProviderProfiles
}

// Catalog is the answer to POST /api/session/modelCatalog.
func (m consoleModels) Catalog() dshapi.ModelCatalog {
	catalog := dshapi.ModelCatalog{
		RoutableProviders: []string{},
		Groups:            []dshapi.ModelProviderGroup{},
		Failures:          []dshapi.ModelCatalogFailure{},
	}
	view := m.settings.view()
	configuredRoute := consoleConfiguredRoute(view)
	if modelName := strings.TrimSpace(view.Model); modelName != "" {
		catalog.Groups = append(catalog.Groups, dshapi.ModelProviderGroup{
			ID:     configuredRoute,
			Name:   consoleProviderName(configuredRoute),
			Models: []dshapi.ModelCatalogModel{{ID: modelName, Name: modelName}},
		})
		catalog.Default = dshapi.ModelSelection{Provider: configuredRoute, Model: modelName}
		catalog.RoutableProviders = append(catalog.RoutableProviders, configuredRoute)
		if _, err := m.settings.model.current(); err != nil {
			// A registered route whose adapter will not build is still a route:
			// upstream registers adapters and reports an authentication failure
			// when a request is made, and the console keeps the composer usable.
			// The reason is carried for repair rather than hiding the route.
			catalog.Failures = append(catalog.Failures, dshapi.ModelCatalogFailure{
				ID:      configuredRoute,
				Name:    consoleProviderName(configuredRoute),
				Message: err.Error(),
			})
		}
	}
	for _, status := range m.declared() {
		models := consoleProfileModels(status.Profile)
		if len(models) == 0 {
			continue
		}
		name := strings.TrimSpace(status.Profile.DisplayName)
		if name == "" {
			name = status.Profile.Provider
		}
		catalog.Groups = append(catalog.Groups, dshapi.ModelProviderGroup{
			ID:     status.Profile.Provider,
			Name:   name,
			Models: models,
		})
		if status.Error != "" {
			catalog.Failures = append(catalog.Failures, dshapi.ModelCatalogFailure{
				ID:      status.Profile.Provider,
				Name:    name,
				Message: status.Error,
			})
		}
		catalog.RoutableProviders = append(catalog.RoutableProviders, status.Profile.Provider)
		if catalog.Default.Model == "" {
			catalog.Default = dshapi.ModelSelection{Provider: status.Profile.Provider, Model: models[0].ID}
		}
	}
	return catalog
}

// declared is the declared profiles this host holds. A host with no profile store
// -- a serve command without one, and a test -- declares none rather than panicking.
func (m consoleModels) declared() []dshapi.ProviderProfileStatus {
	if m.profiles == nil {
		return nil
	}
	return m.profiles.ProviderProfiles()
}

// Offers reports whether this host has a route that lists this model. It is the
// membership rule the picker and the selection both use, and it deliberately does
// not require the credential a run needs: a route with no credential is registered
// and selectable, and the credential failure is what the catalog's failures and the
// prompt-time apply report.
func (m consoleModels) Offers(selection dshapi.ModelSelection) error {
	catalog := m.Catalog()
	if !slices.Contains(catalog.RoutableProviders, selection.Provider) {
		if len(catalog.RoutableProviders) == 0 {
			return fmt.Errorf("provider %q is not one this host can route to: no provider is configured or declared yet", selection.Provider)
		}
		known := append([]string{}, catalog.RoutableProviders...)
		slices.Sort(known)
		return fmt.Errorf("provider %q is not one this host can route to (it serves %s)",
			selection.Provider, strings.Join(known, ", "))
	}
	if !consoleGroupOffers(catalog.Groups, selection.Provider, selection.Model) {
		return fmt.Errorf("model %q is not one provider %q offers", selection.Model, selection.Provider)
	}
	return nil
}

// AdapterFor builds the adapter a selection names, or explains why it cannot be
// built: the model is not offered, or the route's credential is missing.
func (m consoleModels) AdapterFor(selection dshapi.ModelSelection) (model.Model, error) {
	if err := m.Offers(selection); err != nil {
		return nil, err
	}
	view := m.settings.view()
	if selection.Provider == consoleConfiguredRoute(view) && selection.Model == strings.TrimSpace(view.Model) {
		// The configured route is already built and live.
		return m.settings.model.current()
	}
	for _, status := range m.declared() {
		if status.Profile.Provider != selection.Provider {
			continue
		}
		return consoleBuildAdapter(m.settings, status.Profile, selection.Model)
	}
	return nil, fmt.Errorf("provider %q has no profile this host can build an adapter from", selection.Provider)
}

// consoleBuildAdapter builds the adapter a declared profile serves one of its
// models with. Serviceability, the catalog's failures and a run's apply all go
// through it, so "can this host serve it" has one answer.
func consoleBuildAdapter(settings *settingsStore, profile dshapi.ProviderProfile, modelID string) (model.Model, error) {
	protocol, ok := consoleProtocol(profile.API)
	if !ok {
		return nil, fmt.Errorf("protocol %q is not one this host speaks", profile.API)
	}
	key, ref := consoleProfileCredential(settings, profile)
	return provider.FromEnv(provider.Config{
		Protocol:  protocol,
		Model:     strings.TrimSpace(modelID),
		BaseURL:   strings.TrimSpace(profile.BaseURL),
		APIKey:    key,
		APIKeyEnv: ref,
	})
}

// consoleDerivedKeyRef mirrors the credential reference the console's card derives
// from a route: the picker uppercases the provider and replaces every run of
// characters that is not A-Z or a digit with an underscore
// (client/ui-settings-models/src/client/store.ts:112-125), so route "acme-gw"
// names ACME_GW_API_KEY.
func consoleDerivedKeyRef(route string) string {
	var b strings.Builder
	previousUnderscore := false
	for _, r := range strings.ToUpper(route) {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			previousUnderscore = false
		default:
			if !previousUnderscore {
				b.WriteByte('_')
				previousUnderscore = true
			}
		}
	}
	return b.String() + "_API_KEY"
}

// consoleProfileCredential resolves the credential a declared profile names.
//
// A profile naming the reference the console's card derives from its route gets
// the host's own stored credential, because that is where credentials/set put the
// key the operator typed for this provider (ADR 0084: this host has one model
// credential). A profile naming anything else is resolved from that environment
// variable alone: borrowing the host's key for an endpoint it was never entered
// for would send the operator's credential somewhere they did not choose.
func consoleProfileCredential(settings *settingsStore, profile dshapi.ProviderProfile) (key, keyEnv string) {
	ref := strings.TrimSpace(profile.APIKeyEnv)
	if ref == "" || ref == consoleDerivedKeyRef(profile.Provider) {
		return settings.configuredAPIKey(), ref
	}
	return "", ref
}

// consoleConfiguredRoute is the route the host's own settings name.
func consoleConfiguredRoute(view settingsView) string {
	route := strings.TrimSpace(view.Provider)
	if route == "" {
		route = provider.OpenAI
	}
	return route
}

// consoleProfileModels renders a declared profile's models as catalog entries.
func consoleProfileModels(profile dshapi.ProviderProfile) []dshapi.ModelCatalogModel {
	models := make([]dshapi.ModelCatalogModel, 0, len(profile.Models))
	for _, declared := range profile.Models {
		id := strings.TrimSpace(declared.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(declared.Name)
		if name == "" {
			name = id
		}
		models = append(models, dshapi.ModelCatalogModel{ID: id, Name: name})
	}
	return models
}

// consoleGroupOffers reports whether a provider group lists a model.
func consoleGroupOffers(groups []dshapi.ModelProviderGroup, route, modelName string) bool {
	for _, group := range groups {
		if group.ID != route {
			continue
		}
		for _, offered := range group.Models {
			if offered.ID == modelName {
				return true
			}
		}
	}
	return false
}

// consoleModelSelection records each session's chosen model and makes it real: it
// resolves the selection against the catalog and installs that adapter before the
// session's next run.
//
// This host has one model adapter, so the selection is per-session while its
// effect is host-wide for the duration of a run. Every prompt re-applies the
// adapter for the session being prompted -- its own selection, or the operator's
// configured one when it has none -- so a later session never inherits a model a
// previous session chose. Two sessions running concurrently under different
// selections share whichever was applied last; the ADR and docs/limitations.md
// state that plainly rather than implying the harness holds several adapters.
type consoleModelSelection struct {
	settings *settingsStore
	models   consoleModels

	// install is where an applied adapter goes. It is a field so a test can see
	// which adapter a run would use without reaching into the settings store;
	// production wires it to settingsStore.setModelAdapter.
	install func(model.Model)

	mu        sync.Mutex
	seq       int64
	records   map[string]consoleSelectionRecord
	observers map[int]func(dshstream.ModelSelectionUpdate)
	nextID    int
}

// consoleSelectionRecord is one session's durable selection. A record exists for
// every session the host has been told about, selected or not: the console's
// selector needs the projection key present from the session's first moment, and
// `selected` is what keeps "no choice yet" from being reported as an empty
// provider and model.
type consoleSelectionRecord struct {
	selection dshstream.ModelSelection
	selected  bool
	lastUsed  *dshstream.ModelSelection
	seq       int64
}

func newConsoleModelSelection(settings *settingsStore, models consoleModels) *consoleModelSelection {
	return &consoleModelSelection{
		settings:  settings,
		models:    models,
		install:   settings.setModelAdapter,
		records:   map[string]consoleSelectionRecord{},
		observers: map[int]func(dshstream.ModelSelectionUpdate){},
	}
}

// SelectModel validates the selection and records it as the session's next one.
func (s *consoleModelSelection) SelectModel(sessionID string, selection dshapi.ModelSelection) (dshapi.ModelSelection, error) {
	if strings.TrimSpace(selection.ReasoningEffort) != "" {
		return dshapi.ModelSelection{}, fmt.Errorf("this host exposes no reasoning-effort choices, so %q cannot be selected", selection.ReasoningEffort)
	}
	if err := s.models.Offers(selection); err != nil {
		return dshapi.ModelSelection{}, err
	}
	recorded := dshstream.ModelSelection{Provider: selection.Provider, Model: selection.Model}
	s.mu.Lock()
	record := s.records[sessionID]
	record.selection = recorded
	record.selected = true
	record.seq = s.bumpLocked()
	s.records[sessionID] = record
	s.mu.Unlock()
	s.notify(sessionID, record)
	return selection, nil
}

// ApplyModelSelection installs the adapter this session's run should use. A
// session that never chose a model gets the operator's configured adapter, so a
// fresh session cannot inherit the previous one's choice.
func (s *consoleModelSelection) ApplyModelSelection(sessionID string) error {
	s.mu.Lock()
	record, chosen := s.records[sessionID]
	s.mu.Unlock()
	if !chosen {
		s.settings.rebuild()
		return nil
	}
	selection := dshapi.ModelSelection{Provider: record.selection.Provider, Model: record.selection.Model}
	adapter, err := s.models.AdapterFor(selection)
	if err != nil {
		return err
	}
	s.install(adapter)
	lastUsed := record.selection
	s.mu.Lock()
	record = s.records[sessionID]
	record.lastUsed = &lastUsed
	record.seq = s.bumpLocked()
	s.records[sessionID] = record
	s.mu.Unlock()
	s.notify(sessionID, record)
	return nil
}

// States reports every recorded selection for the stream's projection baselines.
// RegisterSession records that a session exists, so the control baseline and the
// live projection frames carry the modelSelection key for it from the moment it
// is created. A session created after the control stream opened is otherwise
// never seeded, and the console's selector then shows nothing to choose however
// well the catalog answered (ADR 0102).
func (s *consoleModelSelection) RegisterSession(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	s.mu.Lock()
	if _, known := s.records[sessionID]; known {
		s.mu.Unlock()
		return
	}
	record := consoleSelectionRecord{seq: s.bumpLocked()}
	s.records[sessionID] = record
	s.mu.Unlock()
	s.notify(sessionID, record)
}

func (s *consoleModelSelection) States() map[string]dshstream.ModelSelectionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make(map[string]dshstream.ModelSelectionState, len(s.records))
	for sessionID, record := range s.records {
		states[sessionID] = dshstream.ModelSelectionState{
			Projection: consoleProjection(record),
			Seq:        record.seq,
		}
	}
	return states
}

// Updates delivers later selection changes until the returned function is called.
func (s *consoleModelSelection) Updates(observe func(dshstream.ModelSelectionUpdate)) (unsubscribe func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.observers[id] = observe
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.observers, id)
		s.mu.Unlock()
	}
}

// bumpLocked advances the local sequence. It is not a session-log watermark: the
// harness records no model-selection event, so the counter exists only to order
// the frames a client receives. Callers hold the lock.
func (s *consoleModelSelection) bumpLocked() int64 {
	s.seq++
	return s.seq
}

// notify tells every subscribed stream about one session's new projection.
func (s *consoleModelSelection) notify(sessionID string, record consoleSelectionRecord) {
	update := dshstream.ModelSelectionUpdate{
		SessionID:  sessionID,
		Projection: consoleProjection(record),
		Seq:        record.seq,
	}
	s.mu.Lock()
	observers := make([]func(dshstream.ModelSelectionUpdate), 0, len(s.observers))
	for _, observe := range s.observers {
		observers = append(observers, observe)
	}
	s.mu.Unlock()
	for _, observe := range observers {
		observe(update)
	}
}

// consoleProjection is the client's view of one session's fold.
func consoleProjection(record consoleSelectionRecord) dshstream.ModelSelectionProjection {
	if !record.selected {
		return dshstream.ModelSelectionProjection{LastUsed: record.lastUsed}
	}
	next := record.selection
	return dshstream.ModelSelectionProjection{LastUsed: record.lastUsed, Next: &next}
}
