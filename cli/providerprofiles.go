package cli

import (
	"strings"
	"sync"

	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/model/provider"
)

// consoleProviderProfiles holds the provider profiles the console declares in the
// llm-pi-ai namespace, and answers the question only this layer can answer: can an
// adapter actually be built for this profile? (ADR 0095)
//
// A profile that cannot be served yet is still stored, with the reason. The console
// writes the profile first and the credential it names second
// (client/CustomProviderCard.tsx:144-190), so refusing an unserved profile on the
// way in would make that order impossible -- and the reason is what the Models page
// shows for repair, exactly as upstream's directory `error` field does.
type consoleProviderProfiles struct {
	settings *settingsStore
	// document is the settings document these profiles are stored in. A nil
	// writer leaves the declarations process-local, which is what a host with no
	// durable home for them does (ADR 0102).
	document *consoleSettingsWriter

	mu       sync.RWMutex
	order    []string
	profiles map[string]dshapi.ProviderProfile
}

// newConsoleProviderProfiles starts from the routes this host is built to serve.
//
// The console picks a provider's editor by the *name* of the namespace the
// directory points at (ui-settings-models client.js: it knows llm-deepseek and
// llm-pi-ai and reports anything else as "unknown", which renders a card with no
// fields and a disabled save). Advertising the built-in routes under this host's
// own namespace names therefore produced cards the operator could see but not
// edit. They are seeded into the pi-ai namespace instead, carrying what the host is
// actually configured with, so the card opens on the real endpoint and model and an
// edit is stored through the same path as any hand-declared route (ADR 0122).
//
// No apiKeyEnv is seeded: this host resolves its own credential (the CLI's
// --api-key-env, or the page's stored key), and naming a variable it does not read
// would be a lie the operator could act on.
func newConsoleProviderProfiles(settings *settingsStore) *consoleProviderProfiles {
	store := &consoleProviderProfiles{settings: settings, profiles: map[string]dshapi.ProviderProfile{}}
	view := settings.view()
	for _, route := range []string{provider.OpenAI, provider.Anthropic} {
		profile := dshapi.ProviderProfile{
			Provider:    route,
			DisplayName: consoleProviderName(route),
			API:         providerProfileProtocol(route),
			BaseURL:     providerProfileBaseURL(view, route),
			Models:      []dshapi.ProviderModel{},
		}
		// The route the host runs on carries the model it runs: the card then shows
		// the operator's own configuration rather than an empty form. It also keeps
		// an empty apiKeyEnv, which is how a profile says "the host's own
		// credential"; a built-in route the host is *not* running on names its
		// conventional variable instead, because an empty reference there would let
		// it borrow a key for a different service.
		if strings.TrimSpace(view.Provider) == route {
			if model := strings.TrimSpace(view.Model); model != "" {
				profile.Models = []dshapi.ProviderModel{{ID: model}}
			}
		} else {
			profile.APIKeyEnv = providerProfileCredentialEnv(route)
		}
		store.order = append(store.order, route)
		store.profiles[route] = profile
	}
	return store
}

// providerProfileCredentialEnv is the conventional variable a built-in route's key
// is read from when it is not the route the host is already running on. The host
// reads this name from the profile, so the seed is a reference it honours.
func providerProfileCredentialEnv(route string) string {
	if route == provider.Anthropic {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

// providerProfileProtocol is the wire protocol a built-in route speaks, from the
// same union the pi-ai schema offers.
func providerProfileProtocol(route string) string {
	if route == provider.Anthropic {
		return dshapi.ProtocolAnthropicMessages
	}
	return dshapi.ProtocolOpenAICompletions
}

// providerProfileBaseURL is the endpoint a built-in route starts at: the one this
// host was configured with when it is the live route, and the provider's own
// default otherwise.
func providerProfileBaseURL(view settingsView, route string) string {
	if strings.TrimSpace(view.Provider) == route {
		if base := strings.TrimSpace(view.BaseURL); base != "" {
			return base
		}
	}
	if route == provider.Anthropic {
		return "https://api.anthropic.com/v1"
	}
	return "https://api.openai.com/v1"
}

// applyDocument loads the profiles the document carries. Declaration order is
// kept, because the Models page lists routes in the order they were declared.
func (s *consoleProviderProfiles) applyDocument(records []consoleProfileRecord) {
	declared := declaredProfiles(records)
	if len(declared) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profiles == nil {
		s.profiles = map[string]dshapi.ProviderProfile{}
	}
	for _, profile := range declared {
		if _, exists := s.profiles[profile.Provider]; exists {
			continue
		}
		s.order = append(s.order, profile.Provider)
		s.profiles[profile.Provider] = profile
	}
}

// persist writes the settings document after a committed change. It is called
// with no lock held: the document's snapshot reads this store back through
// ProviderProfiles, which takes the lock itself.
func (s *consoleProviderProfiles) persist() error { return s.document.save() }

// ProviderProfiles lists the declared routes in declaration order, each with a
// freshly computed diagnostic. Serviceability is deliberately not cached: the card
// writes the profile and then the credential it names, so a cached answer would
// keep reporting a route as unconfigured after the key arrived.
func (s *consoleProviderProfiles) ProviderProfiles() []dshapi.ProviderProfileStatus {
	s.mu.RLock()
	declared := make([]dshapi.ProviderProfile, 0, len(s.order))
	for _, providerID := range s.order {
		if profile, ok := s.profiles[providerID]; ok {
			declared = append(declared, profile)
		}
	}
	s.mu.RUnlock()
	listed := make([]dshapi.ProviderProfileStatus, 0, len(declared))
	for _, profile := range declared {
		listed = append(listed, dshapi.ProviderProfileStatus{Profile: profile, Error: s.serviceability(profile)})
	}
	return listed
}

// SetProviderProfile stores one profile and reports whether this host can serve
// it. The serviceability check builds the real adapter, so "serviceable" here means
// the same thing it means at run time rather than a shape that merely looks right.
//
// A profile this host cannot serve is still stored, with the reason: the console's
// card writes the profile first and the credential it names second (ADR 0095).
// The document is then written, and a document that will not take it puts the
// declaration back, so the page never reads a profile a restart will not have.
func (s *consoleProviderProfiles) SetProviderProfile(profile dshapi.ProviderProfile) (dshapi.ProviderProfileStatus, error) {
	status := dshapi.ProviderProfileStatus{Profile: profile, Error: s.serviceability(profile)}
	s.mu.Lock()
	if s.profiles == nil {
		s.profiles = map[string]dshapi.ProviderProfile{}
	}
	previous, existed := s.profiles[profile.Provider]
	if !existed {
		s.order = append(s.order, profile.Provider)
	}
	s.profiles[profile.Provider] = profile
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		if existed {
			s.profiles[profile.Provider] = previous
		} else {
			delete(s.profiles, profile.Provider)
			s.order = s.order[:len(s.order)-1]
		}
		s.mu.Unlock()
		return dshapi.ProviderProfileStatus{}, err
	}
	return status, nil
}

// RemoveProviderProfile forgets one route, and forgets it in the document too.
func (s *consoleProviderProfiles) RemoveProviderProfile(providerID string) error {
	s.mu.Lock()
	previous, existed := s.profiles[providerID]
	kept := make([]string, 0, len(s.order))
	for _, existing := range s.order {
		if existing != providerID {
			kept = append(kept, existing)
		}
	}
	order := s.order
	s.order = kept
	delete(s.profiles, providerID)
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		if existed {
			s.profiles[providerID] = previous
		}
		s.order = order
		s.mu.Unlock()
		return err
	}
	return nil
}

// serviceability returns the reason this host cannot serve the profile, or an
// empty string when it can.
func (s *consoleProviderProfiles) serviceability(profile dshapi.ProviderProfile) string {
	if len(profile.Models) == 0 {
		return "the profile declares no model"
	}
	// The build is the same one a run performs, so "serviceable" means the same
	// thing here and at run time.
	adapter, err := consoleBuildAdapter(s.settings, profile, profile.Models[0].ID)
	if err != nil {
		return err.Error()
	}
	if adapter == nil {
		return "the adapter factory returned no adapter"
	}
	return ""
}

// consoleProtocol maps the wire protocol a profile names to the adapter family
// this host builds for it.
func consoleProtocol(api string) (string, bool) {
	switch api {
	case dshapi.ProtocolOpenAICompletions:
		return provider.OpenAI, true
	case dshapi.ProtocolAnthropicMessages:
		return provider.Anthropic, true
	default:
		return "", false
	}
}

// consoleDeclaredProvider renders one declared profile as the directory entry the
// Models page shows: its settings address in the pi-ai namespace, and the reason
// when this host cannot serve it.
func consoleDeclaredProvider(status dshapi.ProviderProfileStatus) dshapi.LlmConfigurableProvider {
	displayName := strings.TrimSpace(status.Profile.DisplayName)
	if displayName == "" {
		displayName = status.Profile.Provider
	}
	declared := true
	entry := dshapi.LlmConfigurableProvider{
		Provider:     status.Profile.Provider,
		DisplayName:  displayName,
		SettingsNS:   dshapi.PiAiNamespace,
		SettingsPath: []string{"providers", status.Profile.Provider},
		Declared:     &declared,
	}
	if status.Error != "" {
		entry.Error = status.Error
	}
	return entry
}
