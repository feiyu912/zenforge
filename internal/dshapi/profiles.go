package dshapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Hand-declared provider profiles, and the settings namespace they live in.
//
// The console's "Add a custom provider" button exists only when settings/describe
// carries a namespace called llm-pi-ai, and it is enabled only when that
// namespace's schema declares the wire protocols one may speak: the page reads the
// choices out of the schema rather than from a wire field, so the two cannot drift
// (client/ui-settings-models/src/client/ModelsSection.tsx:535-547, store.ts:112-135).
// The card then writes one profile at providers.<route> in that namespace
// (client/CustomProviderCard.tsx:144-170). Upstream's family is the same shape:
// packages/llm/llm-pi-ai/src/index.ts:93-131.
//
// This host answers with the protocols its own adapter factory can build, so a
// profile it accepts is a profile it can serve -- and one it cannot serve yet is
// reported with the reason instead of being rejected on the way in, because the
// card writes the profile before the credential it names (ADR 0095).

// PiAiNamespace is the settings namespace hand-declared providers are
// written to. It is exported because the serve command builds the directory
// entries that point at it.
const PiAiNamespace = "llm-pi-ai"

// The wire protocol identifiers this host's adapter factory speaks. They are the
// union the namespace schema declares and the only values a profile's api may
// take, so the choices the page offers are exactly the ones this host accepts.
const (
	ProtocolOpenAICompletions = "openai-completions"
	ProtocolAnthropicMessages = "anthropic-messages"
)

// hostProtocols is that set in declaration order.
var hostProtocols = []string{ProtocolOpenAICompletions, ProtocolAnthropicMessages}

// piAiSchemaJSON is the schemastery envelope for the namespace, generated with
// the pinned schemastery the console rehydrates with. providers is a dict, so the
// client's probe path providers."\u0000probe".api resolves to the protocols union.
var piAiSchemaJSON = []byte(`{"uid":26,"refs":{"1":{"type":"string","meta":{"description":"Model id the endpoint accepts"}},"3":{"type":"string","meta":{"description":"Human-readable model name"}},"5":{"type":"number","meta":{"description":"Maximum combined context"}},"7":{"type":"number","meta":{"description":"Maximum output tokens"}},"8":{"type":"object","meta":{"default":{}},"dict":{"id":1,"name":3,"contextWindow":5,"maxTokens":7}},"10":{"type":"string","meta":{"description":"Human-readable provider name"}},"12":{"type":"string","meta":{"description":"Credential reference holding the API key"}},"15":{"type":"const","meta":{"required":true},"value":"openai-completions"},"17":{"type":"const","meta":{"required":true},"value":"anthropic-messages"},"18":{"type":"union","meta":{"description":"Wire protocol the endpoint speaks"},"list":[15,17]},"20":{"type":"string","meta":{"description":"Base URL of the endpoint"}},"22":{"type":"array","meta":{"default":[],"description":"Models this endpoint serves"},"inner":8},"23":{"type":"object","meta":{"default":{}},"dict":{"displayName":10,"apiKeyEnv":12,"api":18,"baseURL":20,"models":22}},"24":{"type":"dict","meta":{"default":{},"description":"Hand-declared provider routes, keyed by route id"},"inner":23,"sKey":25},"25":{"type":"string","meta":{}},"26":{"type":"object","meta":{"default":{}},"dict":{"providers":24}}}}`)

// ProviderModel is one model a declared route serves. Upstream
// packages/llm/llm/lib/types/types.d.ts:263-283 keeps the same four fields this
// host reads; the optional ones are absent when the declaration omits them.
type ProviderModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow *int64 `json:"contextWindow,omitempty"`
	MaxTokens     *int64 `json:"maxTokens,omitempty"`
}

// ProviderProfile is one hand-declared route. Provider is the dict key rather
// than a field, matching how the card writes it.
type ProviderProfile struct {
	Provider    string          `json:"-"`
	DisplayName string          `json:"displayName,omitempty"`
	APIKeyEnv   string          `json:"apiKeyEnv,omitempty"`
	API         string          `json:"api"`
	BaseURL     string          `json:"baseURL"`
	Models      []ProviderModel `json:"models"`
}

// section renders the profile as the settings value the console reads back.
func (p ProviderProfile) section() map[string]any {
	models := make([]map[string]any, 0, len(p.Models))
	for _, item := range p.Models {
		model := map[string]any{"id": item.ID}
		if item.Name != "" {
			model["name"] = item.Name
		}
		if item.ContextWindow != nil {
			model["contextWindow"] = *item.ContextWindow
		}
		if item.MaxTokens != nil {
			model["maxTokens"] = *item.MaxTokens
		}
		models = append(models, model)
	}
	section := map[string]any{"api": p.API, "baseURL": p.BaseURL, "models": models}
	if p.DisplayName != "" {
		section["displayName"] = p.DisplayName
	}
	if p.APIKeyEnv != "" {
		section["apiKeyEnv"] = p.APIKeyEnv
	}
	return section
}

// ProviderProfileStatus is a declared route as the directory reports it: the
// profile, and the reason this host cannot serve it when there is one.
type ProviderProfileStatus struct {
	Profile ProviderProfile
	// Error is the diagnostic the directory carries for repair. Empty means the
	// host built an adapter for this route, which is the only state that makes it
	// usable.
	Error string
}

// ProviderProfileStore owns the llm-pi-ai namespace: the profiles the console
// declares, and whether this host can actually serve each one. It is injected for
// the same reason the other seams are -- only the serve command knows how to build
// an adapter, and this package cannot import it.
type ProviderProfileStore interface {
	// ProviderProfiles lists the declared routes in declaration order.
	ProviderProfiles() []ProviderProfileStatus
	// SetProviderProfile stores one profile and reports whether an adapter could
	// be built for it. A profile that cannot be served yet is stored with the
	// reason rather than refused: the panel writes the profile before the
	// credential it names, so refusing here would make that order impossible.
	SetProviderProfile(profile ProviderProfile) (ProviderProfileStatus, error)
	// RemoveProviderProfile forgets one route.
	RemoveProviderProfile(provider string) error
}

// SetProviderProfiles installs the store the llm-pi-ai namespace answers from.
// Separate from New like the other seams; nil hides the namespace, which is what
// keeps the console's "Add a custom provider" entry point from appearing on a host
// that could not hold the profile it writes.
func (h *Handler) SetProviderProfiles(store ProviderProfileStore) {
	h.profilesMu.Lock()
	h.profiles = store
	h.profilesMu.Unlock()
}

func (h *Handler) providerProfileStore() ProviderProfileStore {
	h.profilesMu.RLock()
	defer h.profilesMu.RUnlock()
	return h.profiles
}

// settingsPiAiView builds the namespace view. The value is the profile map the
// console reads back: route ids it must not offer again, and each profile's
// apiKeyEnv, which is the credential reference its card asks about.
func settingsPiAiView(profiles []ProviderProfileStatus) SettingsNamespaceView {
	providers := make(map[string]any, len(profiles))
	for _, status := range profiles {
		providers[status.Profile.Provider] = status.Profile.section()
	}
	return SettingsNamespaceView{
		NS:       PiAiNamespace,
		Schema:   piAiSchemaJSON,
		Value:    map[string]any{"providers": providers},
		Applies:  "live",
		Secrets:  []SettingsSecretView{},
		Revision: settingsRevision,
	}
}

// providerProfileFields is the field set one declared profile holds, in the order
// its schema declares it (the embedded envelope's profile node:
// displayName, apiKeyEnv, api, baseURL, models).
// TestProviderProfileFieldsMatchTheSchema holds this list to that envelope.
var providerProfileFields = []string{"displayName", "apiKeyEnv", "api", "baseURL", "models"}

// providerModelFields is the field set one model row holds. A row carrying
// anything else is refused rather than silently dropped when the array is decoded
// into the typed field, because a dropped field is a write that looks saved.
var providerModelFields = []string{"id", "name", "contextWindow", "maxTokens"}

// settingsPiAiWrite applies one write to the profile namespace. Two paths are
// held: providers.<route> carries a whole profile, which is how a new provider is
// declared, and providers.<route>.<field> carries one field of an existing
// profile, which is how the provider editor saves an edit (it diffs the keys of
// the profile's subtree and addresses each one: ProviderEditor.tsx:123-126).
// update and replace carry sections, and unset removes a route or a field.
// Anything else is refused by name so a write is never lost.
func settingsPiAiWrite(store ProviderProfileStore, mode string, args map[string]json.RawMessage) (any, *methodError) {
	switch mode {
	case "mutate":
		raw, ok := args["ops"]
		if !ok {
			return nil, argumentRequired("ops")
		}
		var ops []SettingsPathOpView
		if err := json.Unmarshal(raw, &ops); err != nil {
			return nil, fail(codeBadRequest, `argument "ops" must be an array of path operations`,
				map[string]any{"argument": "ops"})
		}
		for _, op := range ops {
			if failure := settingsPiAiOp(store, op); failure != nil {
				return nil, failure
			}
		}
	default:
		raw, ok := args["patch"]
		if mode == "replace" {
			raw, ok = args["section"]
		}
		if !ok {
			key := "patch"
			if mode == "replace" {
				key = "section"
			}
			return nil, argumentRequired(key)
		}
		if mode == "replace" {
			// A replace swaps the whole section, so routes it does not name are
			// removed -- including every route when it carries no providers.
			for _, status := range store.ProviderProfiles() {
				if err := store.RemoveProviderProfile(status.Profile.Provider); err != nil {
					return nil, settingsPiAiRefused(err)
				}
			}
		}
		providers, failure := settingsPiAiProviders(raw)
		if failure != nil {
			return nil, failure
		}
		for _, profile := range providers {
			if _, err := store.SetProviderProfile(profile); err != nil {
				return nil, settingsPiAiRefused(err)
			}
		}
	}
	return settingsPiAiView(store.ProviderProfiles()), nil
}

// settingsPiAiOp turns one path op into a store call.
func settingsPiAiOp(store ProviderProfileStore, op SettingsPathOpView) *methodError {
	if op.Op != "set" && op.Op != "unset" {
		return fail(codeBadRequest,
			fmt.Sprintf("settings mutate: op %q is not one of set or unset", op.Op),
			map[string]any{"argument": "ops", "op": op.Op})
	}
	if len(op.Path) < 2 || len(op.Path) > 3 || op.Path[0] != "providers" {
		return settingsPiAiPathRefused(op.Path)
	}
	provider := strings.TrimSpace(op.Path[1])
	if provider == "" {
		return settingsPiAiPathRefused(op.Path)
	}
	if len(op.Path) == 3 {
		return settingsPiAiFieldOp(store, provider, strings.TrimSpace(op.Path[2]), op)
	}
	if op.Op == "unset" {
		if err := store.RemoveProviderProfile(provider); err != nil {
			return settingsPiAiRefused(err)
		}
		return nil
	}
	profile, failure := settingsPiAiProfile(provider, op.Value)
	if failure != nil {
		return failure
	}
	if _, err := store.SetProviderProfile(profile); err != nil {
		return settingsPiAiRefused(err)
	}
	return nil
}

// settingsPiAiFieldOp applies one field write to an existing profile. The stored
// profile is the merge of the fields the editor did not touch, and the merge is
// validated as a whole before anything is stored, so a field write cannot leave a
// profile this host would have refused to create: a refused write changes nothing.
func settingsPiAiFieldOp(store ProviderProfileStore, provider, field string, op SettingsPathOpView) *methodError {
	if !slices.Contains(providerProfileFields, field) {
		return fail(codeUnimplemented,
			fmt.Sprintf("settings write: field %q is not one this host stores for provider %q (it stores %s)",
				field, provider, strings.Join(providerProfileFields, ", ")),
			map[string]any{"argument": "providers", "provider": provider, "field": field})
	}
	current, stored := storedProviderProfile(store, provider)
	if !stored {
		return fail(codeBadRequest,
			fmt.Sprintf("settings write: there is no stored profile for provider %q to edit; write providers.%s first",
				provider, provider),
			map[string]any{"argument": "providers", "provider": provider})
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return fail(codeInternal, "settings write: the stored profile could not be re-encoded: "+err.Error(),
			map[string]any{"provider": provider})
	}
	fields, err := decodeJSONObject(encoded)
	if err != nil {
		return fail(codeInternal, "settings write: the stored profile could not be read back: "+err.Error(),
			map[string]any{"provider": provider})
	}
	if op.Op == "unset" {
		delete(fields, field)
	} else {
		fields[field] = op.Value
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return fail(codeInternal, "settings write: the merged profile could not be encoded: "+err.Error(),
			map[string]any{"provider": provider})
	}
	profile, failure := settingsPiAiProfile(provider, merged)
	if failure != nil {
		return failure
	}
	if _, err := store.SetProviderProfile(profile); err != nil {
		return settingsPiAiRefused(err)
	}
	return nil
}

// storedProviderProfile finds one stored profile. The store holds a handful, so a
// scan is the lookup; finding it is what keeps a field write from landing on a
// route that is not there.
func storedProviderProfile(store ProviderProfileStore, provider string) (ProviderProfile, bool) {
	for _, status := range store.ProviderProfiles() {
		if status.Profile.Provider == provider {
			return status.Profile, true
		}
	}
	return ProviderProfile{}, false
}

// settingsPiAiProviders decodes an update/replace section.
func settingsPiAiProviders(raw json.RawMessage) ([]ProviderProfile, *methodError) {
	section, err := decodeJSONObject(raw)
	if err != nil {
		return nil, fail(codeBadRequest, "settings write: the section must be a JSON object",
			map[string]any{"argument": "section"})
	}
	providers := make([]ProviderProfile, 0, len(section))
	if rawProviders, ok := section["providers"]; ok {
		entries, err := decodeJSONObject(rawProviders)
		if err != nil {
			return nil, fail(codeBadRequest, `settings write: "providers" must be a JSON object`,
				map[string]any{"argument": "providers"})
		}
		section = entries
	}
	for key, value := range section {
		profile, failure := settingsPiAiProfile(strings.TrimSpace(key), value)
		if failure != nil {
			return nil, failure
		}
		providers = append(providers, profile)
	}
	return providers, nil
}

// settingsPiAiProfile decodes and validates one profile.
func settingsPiAiProfile(provider string, raw json.RawMessage) (ProviderProfile, *methodError) {
	var profile ProviderProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return ProviderProfile{}, fail(codeBadRequest,
			fmt.Sprintf("settings write: the profile for %q must be a JSON object", provider),
			map[string]any{"argument": "providers", "provider": provider})
	}
	profile.Provider = provider
	if failure := settingsPiAiModelFields(provider, raw); failure != nil {
		return ProviderProfile{}, failure
	}
	if failure := validateProviderProfile(profile); failure != nil {
		return ProviderProfile{}, failure
	}
	return profile, nil
}

// settingsPiAiModelFields refuses a model row carrying a field this host does not
// store. The typed decode would drop it, and a dropped field is a write that
// looks saved: an adopted row whose endpoint disclosed something this host does
// not hold is refused by name instead.
func settingsPiAiModelFields(provider string, raw json.RawMessage) *methodError {
	object, err := decodeJSONObject(raw)
	if err != nil {
		return nil
	}
	models, ok := object["models"]
	if !ok {
		return nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(models, &rows); err != nil {
		return nil
	}
	for _, row := range rows {
		fields, err := decodeJSONObject(row)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if slices.Contains(providerModelFields, name) {
				continue
			}
			return fail(codeBadRequest,
				fmt.Sprintf("settings write: model field %q is not one this host stores (it stores %s)",
					name, strings.Join(providerModelFields, ", ")),
				map[string]any{"argument": "models", "provider": provider, "field": name})
		}
	}
	return nil
}

// providerRoutePattern is the route grammar: a settings dict key that is also the
// stem of a credential reference, which cannot start with a digit and cannot hold
// a character that would have to be rewritten (client/store.ts:112-125).
var providerRoutePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// validateProviderProfile refuses a profile this host could not serve, naming what
// it accepts. Everything here is a shape check; whether an adapter actually builds
// is the store's answer, because only it can build one.
func validateProviderProfile(profile ProviderProfile) *methodError {
	if !providerRoutePattern.MatchString(profile.Provider) {
		return fail(codeBadRequest,
			fmt.Sprintf("settings write: %q is not a usable provider route id: it must start with a letter and hold only letters, digits, underscore or dash", profile.Provider),
			map[string]any{"argument": "providers", "provider": profile.Provider})
	}
	if !slices.Contains(hostProtocols, profile.API) {
		return fail(codeBadRequest,
			fmt.Sprintf("settings write: protocol %q is not one this host speaks (%s)", profile.API, strings.Join(hostProtocols, ", ")),
			map[string]any{"argument": "api", "value": profile.API, "protocols": hostProtocols})
	}
	profile.BaseURL = strings.TrimSpace(profile.BaseURL)
	if failure := validateProviderBaseURL(profile.BaseURL); failure != nil {
		return failure
	}
	if profile.APIKeyEnv != "" && !credentialRefPattern.MatchString(profile.APIKeyEnv) {
		return fail(codeBadRequest,
			fmt.Sprintf("settings write: %q is not a usable credential reference", profile.APIKeyEnv),
			map[string]any{"argument": "apiKeyEnv", "value": profile.APIKeyEnv})
	}
	if len(profile.Models) == 0 {
		return fail(codeBadRequest,
			"settings write: a provider profile needs at least one model, because a route with no model cannot serve a request",
			map[string]any{"argument": "models", "provider": profile.Provider})
	}
	return nil
}

// validateProviderBaseURL accepts the absolute http(s) endpoint this host can dial.
func validateProviderBaseURL(baseURL string) *methodError {
	if baseURL == "" {
		return fail(codeBadRequest, "settings write: a provider profile needs a baseURL",
			map[string]any{"argument": "baseURL"})
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fail(codeBadRequest,
			fmt.Sprintf("settings write: baseURL %q must be an absolute http or https URL", baseURL),
			map[string]any{"argument": "baseURL", "value": baseURL})
	}
	return nil
}

// settingsPiAiPathRefused names the one path this namespace holds.
func settingsPiAiPathRefused(path []string) *methodError {
	return fail(codeUnimplemented,
		fmt.Sprintf("settings write: path %q is not stored by this host; namespace %q holds provider profiles at providers.<route> and one field at providers.<route>.<field>",
			strings.Join(path, "."), PiAiNamespace),
		map[string]any{"path": path, "namespace": PiAiNamespace})
}

// settingsPiAiRefused reports a store refusal.
func settingsPiAiRefused(err error) *methodError {
	return fail(codeInternal, "settings write: the host refused the change: "+err.Error(),
		map[string]any{"namespace": PiAiNamespace})
}
