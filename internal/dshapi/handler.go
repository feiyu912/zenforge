package dshapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// Config is the trust seam for the handler. It is configuration rather than a
// hard-coded address check so the process that owns the listener decides the
// policy: zenforge serve can pass its own --allow-remote decision here once it
// mounts this handler, and a test can flip it without touching the package.
type Config struct {
	// AllowRemote admits requests whose RemoteAddr is not a loopback address.
	// The zero value refuses them, which is the safe default for a console
	// that sends no credentials of its own.
	AllowRemote bool
	// Logger, when non-nil, receives one line per request for an endpoint this
	// host does not serve, naming the namespace and method only. That is the
	// host-side replacement for reading a browser's network panel, so the next
	// person can implement the namespaces the console actually calls.
	//
	// The zero value is silent: a nil Logger never falls back to
	// slog.Default, so an unconfigured handler and every existing test print
	// nothing.
	Logger *slog.Logger
}

// Handler answers the console's unary RPCs over the run manager and the
// durable event store. It is a plain http.Handler with no knowledge of where
// /api is mounted, so serve can wire it later and tests can drive it with
// httptest.
type Handler struct {
	manager *harnesshttp.RunManager
	events  eventlog.Store
	cfg     Config

	mu      sync.Mutex
	pending map[string]pendingSession

	// modelCatalog is the injected description of the host's configured model,
	// installed by SetModelCatalog after New. The RWMutex lets a settings
	// change swap it while requests are in flight.
	modelCatalogMu sync.RWMutex
	modelCatalog   ModelCatalogSource

	// credentials is the injected credential face, installed by SetCredentials
	// after New for the same reason: the settings store lives in the serve
	// command, and importing it here would cycle.
	credentialsMu sync.RWMutex
	credentials   CredentialStore

	// llmDirectory is the injected provider directory, installed by
	// SetLlmDirectory after New.
	llmDirectoryMu sync.RWMutex
	llmDirectory   LlmDirectorySource

	// settings is the injected settings document face, installed by
	// SetSettingsDocument after New.
	settingsMu sync.RWMutex
	settings   SettingsDocumentStore

	// settingsRevisionMu guards settingsRevisions, the version of each
	// namespace's user section this host has served. A console editor fences its
	// next write with the revision it was handed and reports the refusal as a
	// conflict when the namespace moved underneath it, so a constant would leave
	// every editor unable to tell an accepted write from a lost one
	// (ui-settings-models/src/client/ProviderEditor.tsx:284 reads back
	// written.view.revision, operations.ts:100 maps "settings/conflict").
	settingsRevisionMu sync.Mutex
	settingsRevisions  map[string]int64

	// presets is the injected preset source, installed by SetPresets after New.
	presetsMu sync.RWMutex
	presets   PresetSource

	// workspaceFiles is the injected read-only file face, installed by
	// SetWorkspaceFiles after New.
	workspaceMu    sync.RWMutex
	workspaceFiles WorkspaceFiles

	// commands is the injected command catalog, installed by SetCommands after
	// New.
	commandsMu sync.RWMutex
	commands   CommandSource

	// plugins is the injected plugin inventory, installed by SetPluginInventory
	// after New.
	pluginsMu sync.RWMutex
	plugins   PluginInventorySource

	// profiles is the injected store for hand-declared provider profiles,
	// installed by SetProviderProfiles after New.
	profilesMu sync.RWMutex
	profiles   ProviderProfileStore

	// modelSelections is the injected store for per-session model selections,
	// installed by SetModelSelections after New.
	modelSelectionsMu sync.RWMutex
	modelSelections   ModelSelectionStore

	// workspaces is the injected console workspace registry, installed by
	// SetWorkspaces after New.
	workspacesMu sync.RWMutex
	workspaces   WorkspaceRegistry
}

// pendingSession is a session id allocated by session/create that has not
// started a run yet. It is process-local: a pending session has no durable
// footprint, which is honest because the run manager has nothing to remember
// it by until the first prompt.
type pendingSession struct {
	id        string
	createdAt time.Time
}

// maxPendingSessions bounds the pending table. Create keeps working by
// refusing past the bound rather than evicting a session the console already
// showed, which would make a later prompt fail as "not found".
const maxPendingSessions = 4096

// New builds the handler from the run manager and durable store the caller
// already owns. Both are required: the session methods cannot be answered
// truthfully without them.
func New(manager *harnesshttp.RunManager, events eventlog.Store, cfg Config) (*Handler, error) {
	if manager == nil {
		return nil, fmt.Errorf("run manager is required")
	}
	if events == nil || nilInterface(events) {
		return nil, fmt.Errorf("event store is required")
	}
	return &Handler{
		manager: manager,
		events:  events,
		cfg:     cfg,
		pending: make(map[string]pendingSession),
	}, nil
}

// ServeHTTP dispatches one POST /api/<namespace>/<method> request. Requests
// are fenced before anything else, then the envelope is validated, then the
// method runs. Only a request that passes every check reaches a method, so
// every method-level failure can be reported as a result envelope.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.fence(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeProtocolError(w, http.StatusMethodNotAllowed, nil, codeBadRequest, "console RPC endpoints require POST")
		return
	}
	endpoint, ok := endpointFromPath(r.URL.Path)
	if !ok {
		writeNotFound(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		writeProtocolError(w, http.StatusBadRequest, nil, codeBadRequest, "request body could not be read")
		return
	}
	if len(body) > maxRequestBodyBytes {
		writeProtocolError(w, http.StatusBadRequest, nil, codeBadRequest, "request body exceeds the size limit")
		return
	}
	request, problem := parseEnvelope(body)
	if problem != "" {
		writeProtocolError(w, http.StatusBadRequest, request.rawRPCID, codeBadRequest, problem)
		return
	}
	// The body and the URL must name the same endpoint. Trusting either alone
	// would let a request logged against one method mutate another.
	if request.method != endpoint {
		writeProtocolError(w, http.StatusBadRequest, request.rawRPCID, codeBadRequest,
			fmt.Sprintf("method %q does not match endpoint %q", request.method, endpoint))
		return
	}
	method, ok := h.method(endpoint)
	if !ok {
		// Diagnose the gap before answering: the console's per-feature
		// degradation is a bare 404, so without this line the only record of
		// what it asked for is a browser's network panel.
		h.logUnservedEndpoint(endpoint)
		writeNotFound(w)
		return
	}
	value, failure := method(r.Context(), unwrapRequestArguments(request.args))
	if failure != nil {
		writeResult(w, request.rawRPCID, rpcResult{Error: &rpcError{
			Code: failure.code, Message: failure.message, Details: failure.details,
		}})
		return
	}
	writeResult(w, request.rawRPCID, rpcResult{OK: true, Value: value})
}

// methodFunc is one endpoint implementation. A nil *methodError is success; a
// non-nil one is a method-level failure carried in a result envelope.
type methodFunc func(context.Context, map[string]json.RawMessage) (any, *methodError)

// method resolves an endpoint to its implementation. An unknown namespace or
// method returns false, which the caller answers with 404 — the console's
// expected shape for a capability a partial host does not provide.
func (h *Handler) method(endpoint string) (methodFunc, bool) {
	namespace, name, found := strings.Cut(endpoint, "/")
	if !found {
		return nil, false
	}
	switch namespace {
	case "pluginInventory":
		if name == "list" {
			return h.pluginInventoryList, true
		}
	case "pluginManager":
		// Every method in this namespace is a write this host cannot perform.
		// The namespace is answered as a whole so a console that skipped the
		// inventory still gets the reason named.
		switch name {
		case "listBundles", "listPlugins", "inspect", "installBundle", "removeBundle",
			"setBundleEnabled", "setPluginEnabled", "cancelInstall":
			return h.pluginManagerUnsupported, true
		}
	case "commands":
		switch name {
		case "list":
			return h.commandsList, true
		case "execute":
			return h.commandsExecute, true
		}
	case "directoryPicker":
		switch name {
		case "list":
			return h.directoryPickerList, true
		case "createDirectory":
			return h.directoryPickerCreateDirectory, true
		case "pick":
			return h.directoryPickerPick, true
		}
	case "workspace":
		switch name {
		case "create":
			return h.workspaceCreate, true
		case "rename":
			return h.workspaceRename, true
		case "delete":
			return h.workspaceDelete, true
		case "archiveSession":
			return h.workspaceArchiveSession, true
		case "unarchiveSession":
			return h.workspaceUnarchiveSession, true
		}
	case "workspaceFiles":
		switch name {
		case "list":
			return h.workspaceFilesList, true
		case "stat":
			return h.workspaceFilesStat, true
		case "read":
			return h.workspaceFilesRead, true
		case "readAll":
			return h.workspaceFilesReadAll, true
		case "readBytes":
			return h.workspaceFilesReadBytes, true
		case "changes":
			return h.workspaceFilesChanges, true
		case "readRelated":
			return h.workspaceFilesReadRelated, true
		}
	case "permissionPresets":
		if name == "catalog" {
			return h.permissionPresetsCatalog, true
		}
	case "agentPresets":
		switch name {
		case "list":
			return h.agentPresetsList, true
		case "read":
			return h.agentPresetsRead, true
		case "copy", "deletePreset":
			return h.agentPresetsReadOnly, true
		case "select":
			return h.agentPresetsSelectIsUnsupported, true
		}
	case "settings":
		switch name {
		case "describe":
			return h.settingsDescribe, true
		case "update":
			return h.settingsUpdate, true
		case "replace":
			return h.settingsReplace, true
		case "mutate":
			return h.settingsMutate, true
		case "canOpenAgentPresetDirectory":
			return h.settingsCanOpenAgentPresetDirectory, true
		case "openSettingsDocument", "openAgentPresetDirectory":
			// These open a file or directory in a native editor on the host.
			// This host has none, so the answer names the gap instead of
			// leaving an operator waiting for a window that will never appear.
			return h.settingsNativeOpenUnsupported, true
		}
	case "llm":
		switch name {
		case "listProviders":
			return h.llmListProviders, true
		case "listConfigurableProviders":
			return h.llmListConfigurableProviders, true
		case "discoverModels":
			return h.llmDiscoverModels, true
		}
	case "credentials":
		switch name {
		case "describe":
			return h.credentialsDescribe, true
		case "set":
			return h.credentialsSet, true
		case "unset":
			return h.credentialsUnset, true
		}
	case "session":
		switch name {
		case "list":
			return h.sessionList, true
		case "create":
			return h.sessionCreate, true
		case "prompt":
			return h.sessionPrompt, true
		case "cancel":
			return h.sessionCancel, true
		case "rename":
			return h.sessionRename, true
		case "page":
			return h.sessionPage, true
		case "modelCatalog":
			return h.sessionModelCatalog, true
		case "selectModel":
			return h.sessionSelectModel, true
		}
	}
	return nil, false
}

// fence applies the Host/Origin/RemoteAddr trust checks the console expects
// from a host. It runs before the method and before body parsing: a request
// that fails trust gets no information about which endpoints exist.
func (h *Handler) fence(w http.ResponseWriter, r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		writeForbidden(w, "cross-site requests are refused")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !originMatchesHost(origin, r.Host) {
		writeForbidden(w, fmt.Sprintf("Origin %q does not match Host %q", origin, r.Host))
		return false
	}
	if !h.cfg.AllowRemote && !remoteIsLoopback(r.RemoteAddr) {
		writeForbidden(w, "non-loopback remote addresses are refused unless remote access is configured")
		return false
	}
	return true
}

// originMatchesHost compares the authority of an Origin header against the
// request Host. A serialized "null" origin parses to an empty host and is a
// mismatch, which is the correct answer: it identifies no same-origin page.
func originMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}

// remoteIsLoopback reports whether a RemoteAddr names this machine. It accepts
// the host:port form net/http produces as well as a bare host, so a test or a
// proxy that omits the port still gets a real answer.
func remoteIsLoopback(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// nilInterface catches a typed-nil event store, which would pass a plain nil
// check and panic on first use.
func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
