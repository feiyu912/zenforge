package dshapi

import (
	"context"
	"encoding/json"
)

// Wire shapes for POST /api/llm/{listProviders,listConfigurableProviders,discoverModels}.
// The console's Models page loads its provider directory from here before it
// renders any card (packages/client/ui-settings-models/src/client/store.ts:215
// onward), so an unserved namespace is not a missing nicety: the page reports
// "Loading the provider directory failed" and shows nothing at all.
//
// Upstream declares exactly three methods on this namespace
// (packages/llm/llm/lib/typert.remote-client.d.ts:10-12) and describes the two
// directory halves as complementary: configuration surfaces merge
// `listProviders()` -- the routes actually registered -- with
// `listConfigurableProviders()` -- every route an adapter can activate through
// configuration -- so a provider appears alongside its live or dormant state
// (packages/llm/llm/lib/types/types.d.ts:194-217).

// consoleProviderRoutes is every provider route this host's adapter factory can
// build. The provider directory, the settings namespaces and the model catalog
// all answer from this one list, so a route cannot be advertised by one surface
// and refused by another.
var consoleProviderRoutes = []string{"openai", "anthropic"}

// LlmProviderInfo is display metadata for one registered provider route.
// Upstream types.d.ts:194-200.
type LlmProviderInfo struct {
	// ID is the provider route key a request names.
	ID string `json:"id"`
	// Name is the human-readable name a selector shows.
	Name string `json:"name"`
}

// LlmConfigurableProvider is one route an adapter can activate through
// configuration, whether or not it is currently registered. Upstream
// types.d.ts:213-235.
type LlmConfigurableProvider struct {
	// Provider is the route key this entry activates when configured.
	Provider string `json:"provider"`
	// DisplayName is the human-readable name for configuration surfaces.
	DisplayName string `json:"displayName"`
	// SettingsNS is the user-settings namespace whose section configures this
	// provider. This host has no settings document (ADR 0084), so the namespace
	// it names is the one a card would need; the write path answers that the
	// feature is unsupported rather than pretending to store it.
	SettingsNS string `json:"settingsNs"`
	// SettingsPath is the path from that namespace's root to this provider's
	// profile object; empty when the whole section is the profile.
	SettingsPath []string `json:"settingsPath"`
	// Declared is upstream-optional and means "the adapter knows this route only
	// because configuration declared it". This host cannot answer for an adapter
	// it did not write, so it is omitted rather than guessed.
	Declared *bool `json:"declared,omitempty"`
	// Error carries a configuration diagnostic for repair. Omitted when the
	// provider is in no trouble, which is the only state this host reports.
	Error string `json:"error,omitempty"`
}

// LlmDirectory is the host's provider directory: the routes currently able to
// serve a request, and the routes an operator could configure.
type LlmDirectory struct {
	Live         []LlmProviderInfo
	Configurable []LlmConfigurableProvider
}

// LlmDirectorySource reports the directory. It is a function type so nil
// unambiguously means "no directory installed", and it is injected for the same
// reason the model catalog is: this package cannot import the serve command,
// which would cycle.
type LlmDirectorySource func() LlmDirectory

// SetLlmDirectory installs the source the llm methods answer from. Separate from
// New, like SetModelCatalog and SetCredentials; nil restores the unimplemented
// answer. Safe to call while requests are in flight.
func (h *Handler) SetLlmDirectory(source LlmDirectorySource) {
	h.llmDirectoryMu.Lock()
	h.llmDirectory = source
	h.llmDirectoryMu.Unlock()
}

func (h *Handler) llmDirectorySource() LlmDirectorySource {
	h.llmDirectoryMu.RLock()
	defer h.llmDirectoryMu.RUnlock()
	return h.llmDirectory
}

// llmListProviders answers POST /api/llm/listProviders: the routes that can
// serve a request right now.
func (h *Handler) llmListProviders(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments(args); failure != nil {
		return nil, failure
	}
	source := h.llmDirectorySource()
	if source == nil {
		return nil, llmDependencyMissing("llm/listProviders")
	}
	directory := source()
	live := directory.Live
	if live == nil {
		// The wire type is a readonly array; null would fail the client's
		// decode where an empty array renders an empty directory.
		live = []LlmProviderInfo{}
	}
	return live, nil
}

// llmListConfigurableProviders answers POST /api/llm/listConfigurableProviders:
// every route an operator could configure through a settings namespace.
func (h *Handler) llmListConfigurableProviders(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments(args); failure != nil {
		return nil, failure
	}
	source := h.llmDirectorySource()
	if source == nil {
		return nil, llmDependencyMissing("llm/listConfigurableProviders")
	}
	directory := source()
	configurable := directory.Configurable
	if configurable == nil {
		configurable = []LlmConfigurableProvider{}
	}
	for index := range configurable {
		if configurable[index].SettingsPath == nil {
			// settingsPath is required on the wire and read as an array; an
			// empty path means "the whole section is the profile".
			configurable[index].SettingsPath = []string{}
		}
	}
	return configurable, nil
}

// llmDiscoverModels answers POST /api/llm/discoverModels. Upstream uses it to
// interrogate a provider endpoint a card is still editing, sending the draft
// endpoint and credential directly (types.d.ts:243-261). This host can reach an
// OpenAI-compatible endpoint and does not yet: the method is registered so the
// console reports a named unsupported capability (and this host's log records
// it) instead of a transport failure that reads like a broken install.
func (h *Handler) llmDiscoverModels(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented,
		"llm/discoverModels is not implemented: this host does not yet interrogate a draft endpoint's model list; configure the model on the host with --model or /api/settings instead",
		map[string]any{"capability": "model discovery"})
}

// llmDependencyMissing is the honest answer when no directory is installed.
func llmDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no provider directory; the serve command must install one with Handler.SetLlmDirectory",
		map[string]any{"dependency": "LlmDirectorySource"})
}
