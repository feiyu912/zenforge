package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Wire shapes for POST /api/settings/*. The console's configuration pages read
// one view per registered namespace and write through path-addressed ops; the
// types below mirror packages/settings/settings/src/types.ts:20-51 and
// api/settings-controller/src/index.ts:100-200, cited field by field.
//
// schema is a serialized schemastery envelope (`schema.toJSON()`), rehydrated by
// the client with `new Schema(json)` (types.ts:36). The envelope in
// settingsSchemaJSON below was generated with the schemastery version this
// repository pins (vendor/schemastery 3.18.2, the same library upstream builds
// it with), so the client rehydrates a schema rather than a hand-written
// imitation of one. The Models page resolves
// `nodeAtPath(schema, ['providers', '\u0000probe', 'api'])` through it, which is
// what makes the providers dict's inner object the shape that matters.

// settingsSchemaJSON is `Schema.object({providers: Schema.dict(Schema.object({
// api: Schema.object({baseURL, apiKey (role secret), model}) }))}).toJSON()`.
// The extra `api` level is not decoration: the Models page resolves
// `['providers', PROBE_ROUTE, 'api']` and reads the endpoint fields from that
// node, so a profile without it renders no card at all.
const settingsSchemaJSON = `{"uid":11,"refs":{"1":{"type":"string","meta":{"description":"Base URL of the OpenAI-compatible endpoint"}},"4":{"type":"string","meta":{"role":"secret","description":"API key for this endpoint"}},"6":{"type":"string","meta":{"description":"Default model id"}},"7":{"type":"object","meta":{"default":{}},"dict":{"baseURL":1,"apiKey":4,"model":6}},"8":{"type":"object","meta":{"default":{}},"dict":{"api":7}},"9":{"type":"dict","meta":{"default":{}},"inner":8,"sKey":10},"10":{"type":"string","meta":{}},"11":{"type":"object","meta":{"default":{}},"dict":{"providers":9}}}}`

// settingsRevision is the revision this host reports. It has no versioned
// document to number -- the settings live in the running process -- so the value
// is constant and a write returns it again. A client that sends back what it
// read therefore never conflicts, and one that sends something else is refused
// rather than silently overwriting a state it did not read.
const settingsRevision = 1

// SettingsSecretView marks one schema-declared secret slot and whether a value
// is configured. Upstream types.ts:20-23.
type SettingsSecretView struct {
	Path []string `json:"path"`
	Set  bool     `json:"set"`
}

// SettingsNamespaceView is one registered namespace as a configuration page
// renders it. Upstream types.ts:33-48.
type SettingsNamespaceView struct {
	// NS is the namespace key (upstream: llm-deepseek, llm-pi-ai, ...).
	NS string `json:"ns"`
	// Schema is the serialized schemastery envelope the page rehydrates.
	Schema json.RawMessage `json:"schema"`
	// Value is the redacted resolved value: schema defaults, composition base and
	// user layer merged, with every secret slot removed.
	Value any `json:"value"`
	// Base and User are optional upstream; this host stores no document, so it
	// reports neither rather than an empty layer that would claim one exists.
	Base any `json:"base,omitempty"`
	User any `json:"user,omitempty"`
	// Applies reports when the owner applies a change. This host rebuilds its
	// adapter as soon as a field is written, so changes are live.
	Applies string `json:"applies"`
	// Secrets lists every schema-declared secret slot with its configured state.
	Secrets []SettingsSecretView `json:"secrets"`
	// Revision is the version a caller echoes back on a write.
	Revision int64 `json:"revision"`
}

// SettingsDescribeValue is the answer to settings/describe. Upstream
// types.ts:51-55.
type SettingsDescribeValue struct {
	// Writable reports whether this surface may change the settings.
	Writable bool `json:"writable"`
	// HasDocument reports whether a settings document exists on the host. This
	// host holds its configuration in the running process, so false is the
	// honest answer and it is why the page offers no "open the file" action.
	HasDocument bool `json:"hasDocument"`
	// Namespaces holds one view per registered namespace.
	Namespaces []SettingsNamespaceView `json:"namespaces"`
}

// SettingsPathOpView is one path-addressed edit. Upstream types.ts:57-59.
type SettingsPathOpView struct {
	Op    string          `json:"op"`
	Path  []string        `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// SettingsProfile is the configuration this host holds, as the settings
// namespace reports it. It never carries the credential value.
type SettingsProfile struct {
	// Provider is the route key the host serves (openai, anthropic).
	Provider string
	// Model is the model id a run uses by default.
	Model string
	// BaseURL is the endpoint override, empty when the provider default applies.
	BaseURL string
	// HasKey reports whether a credential would be found.
	HasKey bool
}

// SettingsDocumentStore is the injected settings face. It is an interface
// because several operations share one dependency, and it is injected for the
// same reason the credential store is: this package cannot import the serve
// command, which would cycle. A nil store means every settings method answers
// unimplemented rather than pretending a document exists.
type SettingsDocumentStore interface {
	// SettingsProfile reports the host's configuration.
	SettingsProfile() SettingsProfile
	// SetSettingsEndpoint records the endpoint override.
	SetSettingsEndpoint(baseURL string) error
	// SetSettingsModel records the default model id.
	SetSettingsModel(model string) error
	// SetSettingsKey records the credential.
	SetSettingsKey(value string) error
	// ClearSettingsKey forgets the stored credential.
	ClearSettingsKey() error
}

// SetSettingsDocument installs the store the settings methods answer from.
// Separate from New like the other seams; nil restores the unimplemented answer.
func (h *Handler) SetSettingsDocument(store SettingsDocumentStore) {
	h.settingsMu.Lock()
	h.settings = store
	h.settingsMu.Unlock()
}

func (h *Handler) settingsStore() SettingsDocumentStore {
	h.settingsMu.RLock()
	defer h.settingsMu.RUnlock()
	return h.settings
}

// settingsNamespaceRoute maps the namespace keys this host advertises in the
// provider directory to the route each one configures. A namespace this host
// does not serve is refused by name rather than ignored: a write that appears to
// succeed and is dropped is worse than one that reports it was not accepted.
func settingsNamespaceRoute(namespace string) (string, bool) {
	for _, route := range consoleProviderRoutes {
		if namespace == settingsNamespaceFor(route) {
			return route, true
		}
	}
	return "", false
}

// settingsNamespaceFor is the namespace a route's profile lives in, matching the
// directory this host advertises (llm-openai, llm-anthropic).
func settingsNamespaceFor(route string) string { return "llm-" + route }

// settingsViewFor builds one namespace view from the host's profile.
func settingsViewFor(route string, profile SettingsProfile) SettingsNamespaceView {
	providers := map[string]any{}
	if profile.Provider == route {
		api := map[string]any{}
		if profile.BaseURL != "" {
			api["baseURL"] = profile.BaseURL
		}
		if profile.Model != "" {
			api["model"] = profile.Model
		}
		providers[route] = map[string]any{"api": api}
	}
	return SettingsNamespaceView{
		NS:       settingsNamespaceFor(route),
		Schema:   json.RawMessage(settingsSchemaJSON),
		Value:    map[string]any{"providers": providers},
		Applies:  "live",
		Secrets:  []SettingsSecretView{{Path: []string{"providers", route, "api", "apiKey"}, Set: profile.HasKey}},
		Revision: settingsRevision,
	}
}

// settingsDescribe answers POST /api/settings/describe: every namespace's
// redacted view and the schema its page renders from.
func (h *Handler) settingsDescribe(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments(args); failure != nil {
		return nil, failure
	}
	store := h.settingsStore()
	if store == nil {
		return nil, settingsDependencyMissing("settings/describe")
	}
	profile := store.SettingsProfile()
	namespaces := make([]SettingsNamespaceView, 0, len(consoleProviderRoutes))
	for _, route := range consoleProviderRoutes {
		namespaces = append(namespaces, settingsViewFor(route, profile))
	}
	return SettingsDescribeValue{Writable: true, HasDocument: false, Namespaces: namespaces}, nil
}

// settingsMutate answers POST /api/settings/mutate, the write the Models page
// uses (ui-settings-models/src/client/operations.ts:96-100).
func (h *Handler) settingsMutate(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.settingsWrite(ctx, args, "mutate")
}

// settingsUpdate answers POST /api/settings/update, a merge into one namespace's
// user section.
func (h *Handler) settingsUpdate(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.settingsWrite(ctx, args, "update")
}

// settingsReplace answers POST /api/settings/replace, a wholesale replacement of
// one namespace's user section.
func (h *Handler) settingsReplace(ctx context.Context, args map[string]json.RawMessage) (any, *methodError) {
	return h.settingsWrite(ctx, args, "replace")
}

// settingsWrite applies one of the three write shapes. They differ in how the
// caller expressed the change, not in what this host can hold: a single provider
// profile's endpoint, model and credential. The payload is flattened into the
// same field writes, and anything this host cannot hold is refused by name.
func (h *Handler) settingsWrite(_ context.Context, args map[string]json.RawMessage, mode string) (any, *methodError) {
	namespace, _, failure := stringArg(args, "ns")
	if failure != nil {
		return nil, failure
	}
	route, known := settingsNamespaceRoute(namespace)
	if !known {
		return nil, fail(codeBadRequest,
			fmt.Sprintf("settings %s: namespace %q is not served by this host", mode, namespace),
			map[string]any{"argument": "ns"})
	}
	// expectedRevision is optional upstream (undefined writes unconditionally).
	if revision, present, failure := intArg(args, "expectedRevision"); failure != nil {
		return nil, failure
	} else if present && revision != settingsRevision {
		return nil, fail(codeSessionConflict,
			fmt.Sprintf("settings %s: expected revision %d but this host is at %d", mode, revision, settingsRevision),
			map[string]any{"expectedRevision": revision, "revision": settingsRevision})
	}
	store := h.settingsStore()
	if store == nil {
		return nil, settingsDependencyMissing("settings/" + mode)
	}
	var edits []settingsFieldEdit
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
			edit, failure := settingsEditFromOp(route, op)
			if failure != nil {
				return nil, failure
			}
			edits = append(edits, edit)
		}
	default:
		// update and replace carry a section rather than ops. This host holds
		// one profile, so a section that names that profile's api fields is
		// applied and any other field is refused rather than dropped.
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
		section, err := decodeJSONObject(raw)
		if err != nil {
			return nil, fail(codeBadRequest, "settings "+mode+": the section must be a JSON object",
				map[string]any{"argument": "section"})
		}
		sectionEdits, failure := settingsEditsFromSection(route, section)
		if failure != nil {
			return nil, failure
		}
		edits = sectionEdits
	}

	for _, edit := range edits {
		if failure := applySettingsEdit(store, edit); failure != nil {
			return nil, failure
		}
	}
	profile := store.SettingsProfile()
	return settingsViewFor(route, profile), nil
}

// settingsFieldEdit is one field write this host understands.
type settingsFieldEdit struct {
	field string
	// value is the text to store; empty with clear means the field is removed.
	value string
	clear bool
}

// settingsEditFromOp turns one path op into a field write, refusing any path this
// host does not store.
func settingsEditFromOp(route string, op SettingsPathOpView) (settingsFieldEdit, *methodError) {
	if op.Op != "set" && op.Op != "unset" {
		return settingsFieldEdit{}, fail(codeBadRequest,
			fmt.Sprintf("settings mutate: op %q is not one of set or unset", op.Op),
			map[string]any{"argument": "ops", "op": op.Op})
	}
	field, ok := settingsFieldForPath(route, op.Path)
	if !ok {
		return settingsFieldEdit{}, settingsPathRefused(route, op.Path)
	}
	if op.Op == "unset" {
		return settingsFieldEdit{field: field, clear: true}, nil
	}
	var value string
	if err := json.Unmarshal(op.Value, &value); err != nil {
		// A non-string value for one of these fields is refused: the schema
		// declares all three as strings, so anything else is a bug or a
		// different schema than the one this host serves.
		return settingsFieldEdit{}, fail(codeBadRequest,
			fmt.Sprintf("settings mutate: %s must be a string", strings.Join(op.Path, ".")),
			map[string]any{"argument": "ops", "field": field})
	}
	return settingsFieldEdit{field: field, value: strings.TrimSpace(value)}, nil
}

// settingsEditsFromSection flattens an update/replace section into field writes.
func settingsEditsFromSection(route string, section map[string]json.RawMessage) ([]settingsFieldEdit, *methodError) {
	edits := make([]settingsFieldEdit, 0, 3)
	// The accepted shapes are {providers: {<route>: {api: {...}}}} and the api
	// object itself; anything else is refused so a write is never silently lost.
	api := section
	if rawProviders, ok := section["providers"]; ok {
		providers, err := decodeJSONObject(rawProviders)
		if err != nil {
			return nil, fail(codeBadRequest, `settings write: "providers" must be a JSON object`,
				map[string]any{"argument": "providers"})
		}
		for key, raw := range providers {
			if key != route {
				return nil, settingsPathRefused(route, []string{"providers", key})
			}
			profile, err := decodeJSONObject(raw)
			if err != nil {
				return nil, settingsPathRefused(route, []string{"providers", key})
			}
			rawAPI, ok := profile["api"]
			if !ok {
				return nil, settingsPathRefused(route, []string{"providers", key})
			}
			if api, err = decodeJSONObject(rawAPI); err != nil {
				return nil, settingsPathRefused(route, []string{"providers", key, "api"})
			}
		}
	}
	for key, raw := range api {
		field := key
		if field != "baseURL" && field != "apiKey" && field != "model" {
			return nil, settingsPathRefused(route, []string{"providers", route, "api", key})
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fail(codeBadRequest,
				fmt.Sprintf("settings write: %s must be a string", field),
				map[string]any{"argument": "patch", "field": field})
		}
		edits = append(edits, settingsFieldEdit{field: field, value: strings.TrimSpace(value)})
	}
	return edits, nil
}

// settingsFieldForPath maps an accepted op path to the field it addresses.
func settingsFieldForPath(route string, path []string) (string, bool) {
	prefix := []string{"providers", route, "api"}
	switch {
	case len(path) == 0:
		return "", false
	case len(path) == 1 && (path[0] == "baseURL" || path[0] == "apiKey" || path[0] == "model"):
		return path[0], true
	case len(path) == len(prefix)+1:
		for index, segment := range prefix {
			if path[index] != segment {
				return "", false
			}
		}
		field := path[len(prefix)]
		if field == "baseURL" || field == "apiKey" || field == "model" {
			return field, true
		}
	}
	return "", false
}

// settingsPathRefused reports a path this host cannot honour. It names the path
// and deliberately does not carry a value: a refused write must not echo a
// credential back to a caller that guessed its path.
func settingsPathRefused(route string, path []string) *methodError {
	return fail(codeUnimplemented,
		fmt.Sprintf("settings write: path %q is not stored by this host; it holds one provider profile (%s) with baseURL, model and apiKey",
			strings.Join(path, "."), route),
		map[string]any{"path": path})
}

// applySettingsEdit performs one field write against the store.
func applySettingsEdit(store SettingsDocumentStore, edit settingsFieldEdit) *methodError {
	var err error
	switch edit.field {
	case "baseURL":
		if edit.clear {
			err = store.SetSettingsEndpoint("")
		} else {
			err = store.SetSettingsEndpoint(edit.value)
		}
	case "model":
		if edit.clear {
			return fail(codeUnimplemented,
				"settings write: this host always has a model; clearing it is not supported",
				map[string]any{"field": "model"})
		}
		err = store.SetSettingsModel(edit.value)
	case "apiKey":
		if edit.clear || edit.value == "" {
			err = store.ClearSettingsKey()
		} else {
			err = store.SetSettingsKey(edit.value)
		}
	default:
		return fail(codeInternal, "settings write: unknown field reached the store", nil)
	}
	if err != nil {
		return fail(codeInternal, "settings write: the host refused the change: "+redactSecret(err.Error(), edit.value),
			map[string]any{"field": edit.field})
	}
	return nil
}

// settingsDependencyMissing is the honest answer when no store is installed.
func settingsDependencyMissing(method string) *methodError {
	return fail(codeUnimplemented,
		method+" is not configured: the host has no settings document store; the serve command must install one with Handler.SetSettingsDocument",
		map[string]any{"dependency": "SettingsDocumentStore"})
}

// settingsCanOpenAgentPresetDirectory answers the one native-open probe that is
// a question rather than a command: this host has no native opener, so false.
func (h *Handler) settingsCanOpenAgentPresetDirectory(_ context.Context, args map[string]json.RawMessage) (any, *methodError) {
	if failure := rejectUnexpectedArguments(args); failure != nil {
		return nil, failure
	}
	return false, nil
}

// settingsNativeOpenUnsupported answers the two methods that would open a file
// or a directory in an editor on the host.
func (h *Handler) settingsNativeOpenUnsupported(_ context.Context, _ map[string]json.RawMessage) (any, *methodError) {
	return nil, fail(codeUnimplemented,
		"this host has no native editor to open a settings document or a preset directory in; configure the host with --base-url, --model, --api-key or the settings panel instead",
		map[string]any{"capability": "native editor"})
}
