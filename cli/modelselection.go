package cli

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/feiyu912/zenforge"
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
	// The built-in routes are seeded into the pi-ai namespace, so the live route
	// arrives below as a declared profile too. It is one endpoint: its declared
	// models replace the configured one's, and it is not added a second time
	// (ADR 0122).
	declared := map[string]dshapi.ProviderProfile{}
	for _, status := range m.declared() {
		declared[status.Profile.Provider] = status.Profile
	}
	if modelName := strings.TrimSpace(view.Model); modelName != "" {
		models := []dshapi.ModelCatalogModel{{ID: modelName, Name: modelName}}
		if profile, ok := declared[configuredRoute]; ok {
			if declaredModels := consoleProfileModels(profile); len(declaredModels) > 0 {
				models = declaredModels
			}
		}
		catalog.Groups = append(catalog.Groups, dshapi.ModelProviderGroup{
			ID:     configuredRoute,
			Name:   consoleProviderName(configuredRoute),
			Models: models,
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
		if status.Profile.Provider == configuredRoute {
			continue
		}
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
// built: the catalog does not offer the model, or the route's credential is
// missing.
func (m consoleModels) AdapterFor(selection dshapi.ModelSelection) (model.Model, error) {
	if err := m.Offers(selection); err != nil {
		return nil, err
	}
	return m.Resolve(selection.Provider, selection.Model)
}

// Resolve builds the adapter a provider and model name, with the route's own
// endpoint and credential: the configured route from the settings as they stand
// (never the live adapter the settings page swaps, so a settings change made later
// cannot reach a run already answering), and a declared profile from its own
// fields. It is the zenforge.ModelResolver seam, and it is what makes a resumed
// run rebuild the adapter its original run used.
//
// It deliberately does not apply the catalog's membership rule. That rule is the
// picker's -- what the model list may offer and what a selection may name -- and a
// caller that names its route and model explicitly, a workflow script's agent()
// option or the route a checkpoint froze, is not the picker. The route still has
// to be one this host knows, and its credential still has to resolve, which is
// where the honest refusals come from.
func (m consoleModels) Resolve(providerName, modelName string) (model.Model, error) {
	providerName = strings.TrimSpace(providerName)
	modelName = strings.TrimSpace(modelName)
	if providerName == consoleConfiguredRoute(m.settings.view()) {
		return m.settings.adapterForModel(modelName)
	}
	for _, status := range m.declared() {
		if status.Profile.Provider != providerName {
			continue
		}
		return consoleBuildAdapter(m.settings, status.Profile, modelName)
	}
	return nil, fmt.Errorf("provider %q has no profile this host can build an adapter from", providerName)
}

// declares reports whether this host lists a provider as one it can route to:
// the configured route, or a declared profile. It is what tells a route this
// console owns from a provider name that reached the resolver some other way. An
// empty provider is not a route name -- it is a caller saying only which model it
// wants, and the CLI's resolver is what decides what that means.
func (m consoleModels) declares(providerName string) bool {
	trimmed := strings.TrimSpace(providerName)
	if trimmed == "" {
		return false
	}
	if trimmed == consoleConfiguredRoute(m.settings.view()) {
		return true
	}
	for _, status := range m.declared() {
		if status.Profile.Provider == trimmed {
			return true
		}
	}
	return false
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

// consoleRouteResolver is this host's zenforge.ModelResolver in serve mode: a
// route the console knows resolves to the endpoint and credential that route
// holds, which is the same resolution the selection itself goes through, so the
// adapter a resumed run rebuilds is the adapter its original run used.
//
// A provider the console does not declare at all falls through to the CLI's
// resolver, which is how this seam behaved before per-run models existed and how
// a workflow script's agent() still names a provider the operator never added to
// the console (and how a caller that names only a model gets the host's own
// route). A provider the console does declare never falls through: its own
// refusal -- a credential that is gone -- is the answer, because resolving it
// somewhere else would run the caller on a different endpoint than the one it
// named.
type consoleRouteResolver struct {
	models   consoleModels
	fallback zenforge.ModelResolver
}

func (r consoleRouteResolver) Resolve(providerName, modelName string) (model.Model, error) {
	if r.models.declares(providerName) {
		return r.models.Resolve(providerName, modelName)
	}
	if r.fallback == nil {
		return r.models.Resolve(providerName, modelName)
	}
	return r.fallback.Resolve(providerName, modelName)
}

// consoleSelectionLimit bounds how many chosen models the settings document
// carries. A selection is a per-session convenience, not a transcript -- the run
// log holds the conversation -- and an unbounded map keyed by session would grow
// the settings file with every session the host has ever served. The most recent
// choices are the ones a restart can still restore.
const consoleSelectionLimit = 64

// consoleModelSelection records each session's chosen model and makes it real: it
// resolves the selection against the catalog and builds the adapter that
// session's own next run is started on.
//
// The selection is per-session in effect as well as in name. A session's route is
// read once, at the prompt that starts its turn, and travels on the run itself
// (ADR 0140), so two sessions running concurrently under different selections
// keep their own adapters, and a selection made while a run is answering cannot
// reach that run. The host's configured adapter is only what a session with no
// choice runs on, and only the settings page swaps it.
type consoleModelSelection struct {
	settings *settingsStore
	models   consoleModels

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
	previous, existed := s.records[sessionID]
	record := previous
	record.selection = recorded
	record.selected = true
	record.seq = s.bumpLocked()
	s.records[sessionID] = record
	s.mu.Unlock()
	s.notify(sessionID, record)
	// The choice is console-written state, so it goes into the settings document
	// with the rest of it: an operator who picked a model and restarted should find
	// it still there (ADR 0103). A document that will not take it puts the record
	// back, because a choice that is live but not durable is the disagreement the
	// document exists to remove.
	if err := s.persistSelection(); err != nil {
		s.mu.Lock()
		if existed {
			s.records[sessionID] = previous
		} else {
			delete(s.records, sessionID)
		}
		s.mu.Unlock()
		s.notify(sessionID, previous)
		return dshapi.ModelSelection{}, err
	}
	return selection, nil
}

// persistSelection writes the settings document after a recorded choice. The
// caller must have released this store's lock: the document's snapshot reads this
// store's records, and taking that read while holding the lock would deadlock.
func (s *consoleModelSelection) persistSelection() error {
	if s == nil || s.settings == nil {
		return nil
	}
	return s.settings.persist()
}

// SelectionRecords renders the sessions whose model an operator chose, newest
// first. This is the part of this store the settings document carries, and it is
// bounded: the document is one small file, and a map keyed by every session the
// host has ever served would grow it without limit. The runtime last-used hint is
// not carried, because it changes on every run and would rewrite the file for a
// label.
func (s *consoleModelSelection) SelectionRecords() map[string]consoleSelectionRecordFile {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type chosenSession struct {
		sessionID string
		seq       int64
	}
	chosen := make([]chosenSession, 0, len(s.records))
	for sessionID, record := range s.records {
		if !record.selected || strings.TrimSpace(record.selection.Model) == "" {
			continue
		}
		chosen = append(chosen, chosenSession{sessionID: sessionID, seq: record.seq})
	}
	slices.SortFunc(chosen, func(a, b chosenSession) int {
		switch {
		case a.seq > b.seq:
			return -1
		case a.seq < b.seq:
			return 1
		default:
			return 0
		}
	})
	if len(chosen) > consoleSelectionLimit {
		chosen = chosen[:consoleSelectionLimit]
	}
	records := make(map[string]consoleSelectionRecordFile, len(chosen))
	for _, item := range chosen {
		record := s.records[item.sessionID]
		records[item.sessionID] = consoleSelectionRecordFile{
			Provider: record.selection.Provider,
			Model:    record.selection.Model,
		}
	}
	return records
}

// AdoptSelectionRecords restores the choices a loaded document carried. Sessions
// are adopted in a stable order so the sequence numbers, and with them which
// choices survive the bound, do not depend on map iteration.
func (s *consoleModelSelection) AdoptSelectionRecords(records map[string]consoleSelectionRecordFile) {
	if s == nil || len(records) == 0 {
		return
	}
	sessionIDs := make([]string, 0, len(records))
	for sessionID := range records {
		sessionIDs = append(sessionIDs, sessionID)
	}
	slices.Sort(sessionIDs)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sessionID := range sessionIDs {
		stored := records[sessionID]
		provider := strings.TrimSpace(stored.Provider)
		modelName := strings.TrimSpace(stored.Model)
		if modelName == "" {
			continue
		}
		record := s.records[sessionID]
		if record.selected && record.selection.Provider == provider && record.selection.Model == modelName {
			continue
		}
		record.selection = dshstream.ModelSelection{Provider: provider, Model: modelName}
		record.selected = true
		record.seq = s.bumpLocked()
		s.records[sessionID] = record
	}
}

// ModelRoute builds the route this session's next run must start on: the
// provider and model its operator chose, with the adapter this host built for the
// pair. A session that never chose a model reports no route, and dshapi starts
// its run on the host's configured adapter instead.
//
// The test is the record's own `selected` flag, not its presence in the map: a
// session is registered the moment it is created so the console's projection has
// a key for it (RegisterSession, ADR 0102), and a registered session with no
// choice is exactly the session that must keep the configured adapter. Reading
// the presence as a choice made every fresh session's first prompt fail with
// "provider \"\" is not one this host can route to".
//
// A recorded choice this host can no longer build is returned as an error, which
// dshapi answers by refusing the prompt: the adapter is built here, once, so a
// credential that went missing fails where the operator can still see it rather
// than becoming a failed run.
//
// The read has no side effect. Marking the route used is dshapi's separate call
// once the run has actually started, because this one is also what a projected
// transcript asks to name the model a session runs on.
func (s *consoleModelSelection) ModelRoute(sessionID string) (dshapi.ModelRoute, bool, error) {
	s.mu.Lock()
	record, known := s.records[sessionID]
	s.mu.Unlock()
	if !known || !record.selected {
		return dshapi.ModelRoute{}, false, nil
	}
	selection := dshapi.ModelSelection{Provider: record.selection.Provider, Model: record.selection.Model}
	adapter, err := s.models.AdapterFor(selection)
	if err != nil {
		return dshapi.ModelRoute{}, false, err
	}
	return dshapi.ModelRoute{Provider: selection.Provider, Model: selection.Model, Adapter: adapter}, true, nil
}

// MarkModelUsed records that a run actually started on this session's route,
// which is what the console's "last used" hint reports. A session that chose
// nothing has no route to consume, and marking one would invent a model no
// operator picked.
func (s *consoleModelSelection) MarkModelUsed(sessionID string) {
	s.mu.Lock()
	record, known := s.records[sessionID]
	if !known || !record.selected {
		s.mu.Unlock()
		return
	}
	lastUsed := record.selection
	record.lastUsed = &lastUsed
	record.seq = s.bumpLocked()
	s.records[sessionID] = record
	s.mu.Unlock()
	s.notify(sessionID, record)
}

// SessionModelIdentity reports what a session will run on. It is the optional
// half of the selection store dshapi asks for when it labels a projected
// transcript: a session that chose a model reports it, and a session that chose
// nothing reports nothing so the caller falls back to the host's configured
// default rather than inventing a route. It stays a map read -- provenance must
// not build an adapter, and must not consume the route it reports.
func (s *consoleModelSelection) SessionModelIdentity(sessionID string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, known := s.records[sessionID]
	if !known || !record.selected {
		return "", "", false
	}
	return record.selection.Provider, record.selection.Model, true
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
