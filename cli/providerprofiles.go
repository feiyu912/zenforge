package cli

import (
	"fmt"
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

	mu       sync.RWMutex
	order    []string
	profiles map[string]dshapi.ProviderProfile
}

func newConsoleProviderProfiles(settings *settingsStore) *consoleProviderProfiles {
	return &consoleProviderProfiles{settings: settings, profiles: map[string]dshapi.ProviderProfile{}}
}

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
func (s *consoleProviderProfiles) SetProviderProfile(profile dshapi.ProviderProfile) (dshapi.ProviderProfileStatus, error) {
	status := dshapi.ProviderProfileStatus{Profile: profile, Error: s.serviceability(profile)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profiles == nil {
		s.profiles = map[string]dshapi.ProviderProfile{}
	}
	if _, exists := s.profiles[profile.Provider]; !exists {
		s.order = append(s.order, profile.Provider)
	}
	s.profiles[profile.Provider] = profile
	return status, nil
}

// RemoveProviderProfile forgets one route.
func (s *consoleProviderProfiles) RemoveProviderProfile(providerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.profiles, providerID)
	kept := s.order[:0]
	for _, existing := range s.order {
		if existing != providerID {
			kept = append(kept, existing)
		}
	}
	s.order = kept
	return nil
}

// serviceability returns the reason this host cannot serve the profile, or an
// empty string when it can.
func (s *consoleProviderProfiles) serviceability(profile dshapi.ProviderProfile) string {
	protocol, ok := consoleProtocol(profile.API)
	if !ok {
		// Unreachable through the console: the schema offers only these, and the
		// API layer refuses anything else. Answered anyway rather than assumed.
		return fmt.Sprintf("protocol %q is not one this host speaks", profile.API)
	}
	if len(profile.Models) == 0 {
		return "the profile declares no model"
	}
	// The credential is resolved exactly the way the run resolves it, so
	// "serviceable" means the same thing here and at run time.
	key, ref := consoleProfileCredential(s.settings, profile)
	adapter, err := provider.FromEnv(provider.Config{
		Protocol:  protocol,
		Model:     strings.TrimSpace(profile.Models[0].ID),
		BaseURL:   strings.TrimSpace(profile.BaseURL),
		APIKey:    key,
		APIKeyEnv: ref,
	})
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
