package dshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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

// LlmModelDiscoveryRequest is one interrogation of an endpoint configuration has
// not stored yet. Upstream types.d.ts:243-261.
type LlmModelDiscoveryRequest struct {
	// Provider is the route a draft edits, when it edits one. A route whose
	// adapter already knows its models is answered from that knowledge instead of
	// a network call.
	Provider string `json:"provider,omitempty"`
	// BaseURL is the endpoint to interrogate. A route the host already describes
	// needs none; one it does not must supply this.
	BaseURL string `json:"baseURL,omitempty"`
	// API is the wire protocol the endpoint speaks, when the draft names one.
	API string `json:"api,omitempty"`
	// APIKey is the credential for this interrogation alone. This host never
	// stores it and never writes it to a diagnostic.
	APIKey string `json:"apiKey,omitempty"`
}

// LlmDiscoveredModel is one model an endpoint reports about itself. Only the id
// is required because most provider listings disclose an id and nothing else.
// Upstream types.d.ts:269-283.
type LlmDiscoveredModel struct {
	ID string `json:"id"`
	// Name is the human-readable name when the endpoint supplies one.
	Name string `json:"name,omitempty"`
	// ContextWindow and MaxTokens are pointers so an undisclosed capacity is
	// omitted rather than reported as zero.
	ContextWindow   *int     `json:"contextWindow,omitempty"`
	MaxTokens       *int     `json:"maxTokens,omitempty"`
	InputModalities []string `json:"inputModalities,omitempty"`
}

// codeModelDiscoveryRejected is the one error upstream declares for this method,
// carrying the namespace and, when one was named, the endpoint
// (types.d.ts:263-270).
const codeModelDiscoveryRejected = "llm/model-discovery-rejected"

// discoveryTimeout bounds one interrogation. It is a console action an operator
// is waiting on, not a background fetch, so it does not outlive their patience;
// the request context cancels it sooner when the browser goes away.
const discoveryTimeout = 20 * time.Second

// discoveryBodyLimit bounds how much of a response is read. A model listing is
// small and a wrong endpoint (an HTML page, a streaming API) must not be read
// into memory to find that out.
const discoveryBodyLimit = 1 << 20

// discoveryRejected is the typed refusal for this method: the namespace, the
// endpoint when one was named, and a message naming what is wrong. It never
// carries the credential the draft sent.
func discoveryRejected(settingsNS, baseURL, message string) *methodError {
	details := map[string]any{"settingsNs": settingsNS}
	if trimmed := strings.TrimSpace(baseURL); trimmed != "" {
		details["baseURL"] = trimmed
	}
	return fail(codeModelDiscoveryRejected, message, details)
}

// llmDiscoverModels answers POST /api/llm/discoverModels: the model list of an
// endpoint a card is still editing, or of a route this host already describes.
//
// Upstream uses it twice (ui-settings-models/src/client/ModelListEditor.tsx:153
// and :230): when a card for a known, non-declared route mounts it inherits the
// adapter's own catalog with no network call, and the model list editor's fetch
// button sends the draft endpoint and credential directly, because a provider
// being added has no route to name yet.
func (h *Handler) llmDiscoverModels(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	// request is the client's own parameter name and the dispatch layer already
	// flattens it; both the flattened fields and the wrapper are accepted so the
	// method keeps working whichever shape reaches it.
	if failure := rejectUnknownArguments(args, "settingsNs", "request", "provider", "baseURL", "api", "apiKey"); failure != nil {
		return nil, failure
	}
	settingsNS, present, failure := stringArg(args, "settingsNs")
	if failure != nil {
		return nil, failure
	}
	if !present || strings.TrimSpace(settingsNS) == "" {
		return nil, fail(codeArgumentsInvalid,
			"argument \"settingsNs\" is required: it names the settings namespace whose draft is being interrogated",
			map[string]any{"argument": "settingsNs"})
	}
	rawRequest, ok := args["request"]
	if !ok {
		// The dispatch layer flattened the request object into the args, so the
		// fields it carried are the request. Re-encoding names them back for the
		// decoder that reads them, which keeps one validation path.
		flat := make(map[string]json.RawMessage, len(args))
		for name, value := range args {
			if name == "settingsNs" {
				continue
			}
			flat[name] = value
		}
		encoded, err := json.Marshal(flat)
		if err != nil {
			return nil, fail(codeInternal, "encode discovery request: "+err.Error(), nil)
		}
		rawRequest = encoded
	}
	request, failure := decodeDiscoveryRequest(settingsNS, rawRequest)
	if failure != nil {
		return nil, failure
	}
	protocol, failure := discoveryProtocol(settingsNS, request.API)
	if failure != nil {
		return nil, failure
	}
	if strings.TrimSpace(request.BaseURL) == "" {
		return h.discoverDeclaredModels(settingsNS, request)
	}
	return h.discoverEndpointModels(ctx, settingsNS, request, protocol)
}

// decodeDiscoveryRequest reads the request object, refusing any field this host
// does not read rather than ignoring a typo: a draft that named "baseUrl" would
// otherwise be interrogated with no endpoint and refuse for the wrong reason.
func decodeDiscoveryRequest(settingsNS string, raw json.RawMessage) (LlmModelDiscoveryRequest, *methodError) {
	var request LlmModelDiscoveryRequest
	object, err := decodeJSONObject(raw)
	if err != nil {
		return request, fail(codeArgumentsInvalid, "argument \"request\" must be an object",
			map[string]any{"argument": "request"})
	}
	for name := range object {
		switch name {
		case "provider", "baseURL", "api", "apiKey":
		default:
			return request, fail(codeArgumentsInvalid,
				fmt.Sprintf("unexpected argument \"request.%s\"", name), map[string]any{"argument": "request." + name})
		}
	}
	for name, target := range map[string]*string{
		"provider": &request.Provider, "baseURL": &request.BaseURL,
		"api": &request.API, "apiKey": &request.APIKey,
	} {
		rawValue, ok := object[name]
		if !ok {
			continue
		}
		if err := json.Unmarshal(rawValue, target); err != nil {
			return request, fail(codeArgumentsInvalid,
				fmt.Sprintf("argument \"request.%s\" must be a string", name),
				map[string]any{"argument": "request." + name})
		}
	}
	return request, nil
}

// discoveryProtocol is the protocol an interrogation speaks: the draft's own
// when it names one, otherwise the one its settings namespace configures.
func discoveryProtocol(settingsNS, api string) (string, *methodError) {
	if trimmed := strings.TrimSpace(api); trimmed != "" {
		switch trimmed {
		case ProtocolOpenAICompletions, ProtocolAnthropicMessages:
			return trimmed, nil
		}
		return "", discoveryRejected(settingsNS, "",
			fmt.Sprintf("protocol %q is not one this host speaks (it speaks %s and %s)",
				trimmed, ProtocolOpenAICompletions, ProtocolAnthropicMessages))
	}
	switch settingsNS {
	case PiAiNamespace, settingsNamespaceFor("openai"):
		return ProtocolOpenAICompletions, nil
	case settingsNamespaceFor("anthropic"):
		return ProtocolAnthropicMessages, nil
	}
	return "", discoveryRejected(settingsNS, "",
		fmt.Sprintf("namespace %q names no protocol this host knows: pass request.api (%s or %s)",
			settingsNS, ProtocolOpenAICompletions, ProtocolAnthropicMessages))
}

// discoverDeclaredModels answers without a network call. The host's model
// catalog is its registry: a route that appears in it is answered with the
// models the console already shows for it, a route the host can build an adapter
// for but publishes no model list has an empty catalog, and a route the host has
// never heard of refuses by name.
func (h *Handler) discoverDeclaredModels(settingsNS string, request LlmModelDiscoveryRequest) (any, *methodError) {
	provider := strings.TrimSpace(request.Provider)
	source := h.modelCatalogSource()
	if source != nil {
		for _, group := range source().Groups {
			if group.ID != provider {
				continue
			}
			models := make([]LlmDiscoveredModel, 0, len(group.Models))
			for _, model := range group.Models {
				discovered := LlmDiscoveredModel{ID: model.ID, Name: model.Name}
				if discovered.ID == "" {
					continue
				}
				if discovered.Name == model.ID {
					// The catalog falls back to the id for a name; the wire type
					// omits a name it does not have rather than repeating the id.
					discovered.Name = ""
				}
				models = append(models, discovered)
			}
			return models, nil
		}
	}
	if provider == "" {
		return nil, discoveryRejected(settingsNS, "",
			"no endpoint and no provider were supplied: pass request.baseURL to interrogate an endpoint, or request.provider for a route this host already describes")
	}
	if h.routeKnown(provider) {
		// A registered route with no published model list: an empty catalog is the
		// truthful answer, and the card lets the operator add models by hand.
		return []LlmDiscoveredModel{}, nil
	}
	return nil, discoveryRejected(settingsNS, "",
		fmt.Sprintf("this host knows no route %q: pass request.baseURL to interrogate an endpoint, or declare the provider first", provider))
}

// routeKnown reports whether this host has an adapter for a route, either built
// by its factory or listed in the provider directory.
func (h *Handler) routeKnown(provider string) bool {
	for _, route := range consoleProviderRoutes {
		if route == provider {
			return true
		}
	}
	source := h.llmDirectorySource()
	if source == nil {
		return false
	}
	directory := source()
	for _, live := range directory.Live {
		if live.ID == provider {
			return true
		}
	}
	for _, configurable := range directory.Configurable {
		if configurable.Provider == provider {
			return true
		}
	}
	return false
}

// discoverEndpointModels interrogates an endpoint's model list. The credential
// travels in the header the protocol uses and is never logged.
func (h *Handler) discoverEndpointModels(ctx context.Context, settingsNS string, request LlmModelDiscoveryRequest, protocol string) (any, *methodError) {
	baseURL := strings.TrimSpace(request.BaseURL)
	endpoint, err := discoveryEndpointURL(baseURL)
	if err != nil {
		return nil, discoveryRejected(settingsNS, baseURL, err.Error())
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, discoveryRejected(settingsNS, baseURL, "the endpoint is not a usable URL: "+err.Error())
	}
	httpRequest.Header.Set("accept", "application/json")
	if key := strings.TrimSpace(request.APIKey); key != "" {
		if protocol == ProtocolAnthropicMessages {
			httpRequest.Header.Set("x-api-key", key)
		} else {
			httpRequest.Header.Set("authorization", "Bearer "+key)
		}
	}
	if protocol == ProtocolAnthropicMessages {
		httpRequest.Header.Set("anthropic-version", "2023-06-01")
	}
	client := &http.Client{Timeout: discoveryTimeout}
	response, err := client.Do(httpRequest)
	if err != nil {
		// The credential is in a header, so a transport error cannot echo it, and
		// redactSecret guards the message anyway.
		return nil, discoveryRejected(settingsNS, baseURL,
			"the endpoint could not be reached: "+redactSecret(err.Error(), request.APIKey))
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, discoveryBodyLimit))
	if err != nil {
		return nil, discoveryRejected(settingsNS, baseURL,
			"the endpoint's answer could not be read: "+redactSecret(err.Error(), request.APIKey))
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, discoveryRejected(settingsNS, baseURL,
			fmt.Sprintf("the endpoint answered HTTP %d (the model list is expected at %s)", response.StatusCode, endpoint))
	}
	models, err := decodeDiscoveredModels(body)
	if err != nil {
		return nil, discoveryRejected(settingsNS, baseURL, err.Error())
	}
	return models, nil
}

// discoveryEndpointURL is where a model listing lives: both protocols this host
// speaks publish it at /models under the base URL the operator configured, which
// is the API root each adapter appends its own request path to.
func discoveryEndpointURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("the endpoint is not a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("the endpoint must be an http or https URL (got scheme %q)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("the endpoint has no host")
	}
	if parsed.User != nil {
		return "", errors.New("the endpoint must not carry credentials in the URL; pass request.apiKey instead")
	}
	return strings.TrimRight(parsed.String(), "/") + "/models", nil
}

// decodedModel is one entry of a model listing. The fields are the spellings
// OpenAI-compatible gateways, Anthropic and Ollama actually use; anything else
// is not guessed.
type decodedModel struct {
	// The name fields are pointers so "absent" and "present but empty" are told
	// apart: an OpenAI-shaped entry with an empty id is malformed and skipped,
	// while an Ollama-shaped one legitimately names the model with "name".
	ID              *string  `json:"id"`
	Name            *string  `json:"name"`
	DisplayName     *string  `json:"display_name"`
	Model           *string  `json:"model"`
	ContextWindow   *int     `json:"context_window"`
	ContextLength   *int     `json:"context_length"`
	MaxTokens       *int     `json:"max_tokens"`
	MaxOutputTokens *int     `json:"max_output_tokens"`
	InputModalities []string `json:"input_modalities"`
}

// identified is the model id an entry names, in the order the shapes that carry
// one use: OpenAI's "id", then the tag a compatible gateway or Ollama repeats as
// "model", then Ollama's "name".
func (entry decodedModel) identified() string {
	for _, candidate := range []*string{entry.ID, entry.Model, entry.Name} {
		if candidate == nil {
			continue
		}
		return strings.TrimSpace(*candidate)
	}
	return ""
}

func (entry decodedModel) labelled() string {
	for _, candidate := range []*string{entry.Name, entry.DisplayName} {
		if candidate == nil {
			continue
		}
		if value := strings.TrimSpace(*candidate); value != "" {
			return value
		}
	}
	return ""
}

// decodeDiscoveredModels reads a model listing. Both shapes a compatible
// endpoint publishes are accepted -- OpenAI's {"data": [...]} and Ollama's
// {"models": [...]} -- and a response with neither refuses by name rather than
// reporting an empty catalog for an endpoint that answered something else.
func decodeDiscoveredModels(body []byte) ([]LlmDiscoveredModel, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, errors.New("the endpoint's answer was not a JSON object with a model list")
	}
	raw, ok := object["data"]
	if !ok {
		raw, ok = object["models"]
	}
	if !ok {
		return nil, errors.New("the endpoint's answer carried neither a \"data\" nor a \"models\" list")
	}
	var entries []decodedModel
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.New("the endpoint's model list was not an array of models")
	}
	models := make([]LlmDiscoveredModel, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		id := entry.identified()
		if id == "" {
			// An entry with no usable id cannot be adopted by a card.
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		model := LlmDiscoveredModel{
			ID:              id,
			ContextWindow:   firstNonNil(entry.ContextWindow, entry.ContextLength),
			MaxTokens:       firstNonNil(entry.MaxOutputTokens, entry.MaxTokens),
			InputModalities: entry.InputModalities,
		}
		if name := entry.labelled(); name != "" && name != id {
			model.Name = name
		}
		models = append(models, model)
	}
	return models, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonNil(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// llmDependencyMissing is the honest answer when no directory is installed.
func llmDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no provider directory; the serve command must install one with Handler.SetLlmDirectory",
		map[string]any{"dependency": "LlmDirectorySource"})
}
