package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshmount"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

const (
	// defaultServeAddr is loopback because the run and settings APIs have no
	// authentication: the operator is expected to be sitting at the machine
	// that runs the server, and --allow-remote is the explicit decision to
	// expose it further.
	defaultServeAddr = "127.0.0.1:8787"
	// defaultServeRunTimeout bounds one served run, matching the detached
	// example harness: a browser tab that is closed must not leave a run
	// running for the lifetime of the server.
	defaultServeRunTimeout = 10 * time.Minute
	// maxSettingsBodyBytes caps the settings body. The fields are short, and
	// an endpoint that accepts a key should not accept an unbounded upload.
	maxSettingsBodyBytes = 1 << 16
)

// serveCommand hosts the HTTP harness and the built-in console. It is the
// closest relative of mcpServerCommand: a long-running subcommand that opens
// resources, serves until its context ends, and drains what it opened.
func serveCommand(ctx context.Context, args []string, ioStreams IO) error {
	opts, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	bindOptions(fs, &opts)
	addr := fs.String("addr", defaultServeAddr, "HTTP listen address; a non-loopback address requires --allow-remote")
	allowRemote := fs.Bool("allow-remote", false, "allow binding a non-loopback address and remote settings changes")
	runTimeout := fs.Duration("run-timeout", defaultServeRunTimeout, "bound on one served run")
	// The secret falls back to the environment so it is not visible in argv,
	// and an empty secret disables the signed webhook route rather than
	// allowing an unauthenticated run trigger.
	webhookSecret := fs.String("webhook-secret", strings.TrimSpace(os.Getenv("ZENFORGE_WEBHOOK_SECRET")), "shared secret for the signed POST /webhook/run trigger; empty disables the endpoint")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	if *runTimeout <= 0 {
		return invalidUsage(errors.New("--run-timeout must be positive"))
	}
	// The check is deliberately about the address that will be bound, not
	// about the string: a bind to "0.0.0.0" or "[::]" reaches the network
	// even though neither string looks like a name, so anything not provably
	// loopback has to be opted into.
	if !*allowRemote && !isLoopbackListenAddr(*addr) {
		return invalidUsage(fmt.Errorf("--addr %q is not a loopback address; the run and settings APIs are unauthenticated, so pass --allow-remote to bind it deliberately", *addr))
	}

	// Registered before anything is built so every path out releases the
	// stores and MCP processes buildAgentConfig opens.
	defer drainClosers(&opts, ioStreams)

	app, err := newServeApp(ctx, &opts, ioStreams, serveConfig{
		runTimeout:    *runTimeout,
		webhookSecret: *webhookSecret,
		allowRemote:   *allowRemote,
	})
	if err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Close(stopCtx); err != nil {
			_, _ = fmt.Fprintf(ioStreams.Stderr, "warning: closing run manager: %v\n", err)
		}
	}()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()
	_, _ = fmt.Fprintf(ioStreams.Stdout, "zenforge serve listening on http://%s\n", listener.Addr())
	_, _ = fmt.Fprintln(ioStreams.Stdout, "open it in a browser; set the model endpoint and API key under the gear icon")

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// serveConfig is the small set of serve-only knobs, kept separate from the
// shared options so newServeApp can be called by a test without a flag set.
type serveConfig struct {
	runTimeout    time.Duration
	webhookSecret string
	allowRemote   bool
}

// serveApp is the assembled server a command or a test can drive. It exists
// as a value so the tests exercise the real handler through httptest instead
// of binding a port and racing a goroutine to learn it.
type serveApp struct {
	runtime   *harnesshttp.Runtime
	handler   http.Handler
	settings  *settingsStore
	workspace string
}

// Handler returns the fully routed handler: harness routes, settings API, and
// the embedded console, in the same precedence a browser sees.
func (a *serveApp) Handler() http.Handler {
	return a.handler
}

// Close stops detached work; the deferred option drain closes the stores.
func (a *serveApp) Close(ctx context.Context) error {
	if a == nil || a.runtime == nil {
		return nil
	}
	return a.runtime.Close(ctx)
}

// newServeApp builds the model, stores, approval broker, run manager, and
// route table exactly once. It is the seam the tests use: everything about
// serving except the listener lives here.
func newServeApp(ctx context.Context, opts *options, ioStreams IO, config serveConfig) (*serveApp, error) {
	// Approvals are answered from the browser through /approvals and
	// /approval, so the run's broker is the same in-memory pending broker the
	// HTTP handler exposes. The CLI's interactive broker would read the
	// server's own stdin, where no operator is sitting.
	inbox := approval.NewPendingBroker(0)
	opts.approvalOverride = inbox

	// The model is a delegating adapter rather than a concrete one: the agent
	// is assembled once, and a later settings POST has to affect runs that
	// start afterwards without rebuilding the agent (which would drop the
	// tools, stores, and MCP processes already wired). Every model call reads
	// the adapter current at that moment.
	swappable := newSwappableModel()
	opts.modelOverride = swappable

	settings := &settingsStore{
		current: serverSettings{
			baseURL:   opts.baseURL,
			model:     opts.model,
			provider:  opts.provider,
			apiKey:    opts.apiKey,
			apiKeyEnv: opts.apiKeyEnv,
		},
		model:       swappable,
		allowRemote: config.allowRemote,
	}
	// A missing key at startup is not fatal: the console exists so an
	// operator can paste one. The build error is kept and reported when a run
	// actually needs the model, instead of refusing to start the server.
	settings.rebuild()

	agentConfig, events, err := buildAgentConfig(ctx, opts, ioStreams)
	if err != nil {
		return nil, err
	}
	runtime, err := harnesshttp.NewRuntime(agentConfig, events, harnesshttp.RuntimeOptions{
		ApprovalInbox: inbox,
		Webhook:       harnesshttp.WebhookOptions{Secret: config.webhookSecret},
		Manager: harnesshttp.RunManagerOptions{
			MaxActive:         16,
			RunTimeout:        config.runTimeout,
			TerminalRetention: 10 * time.Minute,
			OwnerID:           "zenforge-serve",
		},
	})
	if err != nil {
		return nil, err
	}

	workspace := opts.workspace
	if absolute, absErr := filepath.Abs(opts.workspace); absErr == nil {
		workspace = absolute
	}

	// The console groups sessions by workspace, and this host runs every
	// session in one directory: the registry it hands the console is built
	// around that directory (ADR 0101). A workspace that cannot be opened
	// leaves the face nil, so the namespace answers unimplemented with the
	// dependency named instead of showing an empty registry -- and the reason
	// is logged once, here, instead of on every request.
	workspaces, workspacesErr := newConsoleWorkspaces(workspace)
	if workspacesErr != nil {
		slog.Warn("console workspace grouping is disabled: the host workspace could not be opened",
			"workspace", workspace, "error", workspacesErr)
	}
	var workspaceRegistry dshapi.WorkspaceRegistry
	var workspaceBaseline func() dshstream.WorkspaceBaseline
	var workspaceUpdates func(observe func(dshstream.WorkspaceUpdate)) (unsubscribe func())
	if workspaces != nil {
		workspaceRegistry = workspaces
		workspaceBaseline = workspaces.Baseline
		workspaceUpdates = workspaces.Subscribe
	}

	// The DSH console is the product surface: its boot-injected shell, staged
	// assets, module bundles, and unary RPCs share one origin, which is what the
	// console requires (its RPC base is hard-wired to the page origin). Building
	// it is where a roster that cannot boot fails, before a listener is opened.
	// The console's model selector asks the host what it is configured with.
	// The settings store is the answer, so the picker reports the operator's
	// own endpoint and model instead of an invented one; an unconfigured host
	// returns an empty catalog and the console says so.
	profiles := newConsoleProviderProfiles(settings)
	models := consoleModels{settings: settings, profiles: profiles}
	selections := newConsoleModelSelection(settings, models)
	modelCatalog := models.Catalog
	// The Models page loads its provider directory before it renders any card,
	// so a missing answer is not a missing nicety: the page reports that loading
	// the directory failed and shows nothing. The live half is the route this
	// host is configured to serve; the configurable half is every route the
	// host's own adapter factory accepts.
	llmDirectory := func() dshapi.LlmDirectory {
		liveProvider := strings.TrimSpace(settings.view().Provider)
		if liveProvider == "" {
			liveProvider = provider.OpenAI
		}
		configurable := []dshapi.LlmConfigurableProvider{
			{Provider: provider.OpenAI, DisplayName: "OpenAI", SettingsNS: "llm-openai", SettingsPath: []string{}},
			{Provider: provider.Anthropic, DisplayName: "Anthropic", SettingsNS: "llm-anthropic", SettingsPath: []string{}},
		}
		// Hand-declared routes come last, as upstream orders them, and carry the
		// settings address in the pi-ai namespace that they were written to.
		for _, status := range profiles.ProviderProfiles() {
			configurable = append(configurable, consoleDeclaredProvider(status))
		}
		return dshapi.LlmDirectory{
			Live: []dshapi.LlmProviderInfo{{
				ID:   liveProvider,
				Name: consoleProviderName(liveProvider),
			}},
			Configurable: configurable,
		}
	}
	console, err := dshmount.New(runtime.Manager, runtime.Events, dshmount.Config{
		AllowRemote:      config.allowRemote,
		ModelCatalog:     modelCatalog,
		Logger:           slog.Default(),
		Credentials:      consoleCredentials{settings: settings},
		LlmDirectory:     llmDirectory,
		Settings:         consoleSettings{settings: settings},
		ProviderProfiles: profiles,
		ModelSelections:  selections,
		Presets:          consolePresets(opts),
		WorkspaceFiles:   consoleFileFace(opts),
		Commands:         consoleCommandCatalog(opts),
		Workspaces:       workspaceRegistry,
	})
	if err != nil {
		return nil, err
	}

	// The console cannot follow a session over unary RPC: sessions, live events
	// and approvals travel on the WebSocket mux, and an approval is answered on
	// its own route. Both are the same handler, mounted at the paths that
	// package exports, so the two halves cannot drift apart.
	stream, err := dshstream.New(runtime.Manager, runtime.Events, inbox, dshstream.Config{
		AllowRemote:           config.allowRemote,
		ModelSelections:       selections.States,
		ModelSelectionUpdates: selections.Updates,
		Workspaces:            workspaceBaseline,
		WorkspaceUpdates:      workspaceUpdates,
	})
	if err != nil {
		return nil, err
	}

	mux := newServeMux(serveMuxConfig{
		registerHarness: func(mux *http.ServeMux) {
			handler := runtime.Handler
			mux.HandleFunc("/runs/start", handler.ServeDetachedStart)
			mux.HandleFunc("/runs/resume", handler.ServeDetachedResume)
			mux.HandleFunc("/runs/status", handler.ServeDetachedStatus)
			mux.HandleFunc("/runs", handler.ServeDetachedRuns)
			mux.HandleFunc("/runs/attach", handler.ServeDetachedAttach)
			mux.HandleFunc("/runs/cancel", handler.ServeDetachedCancel)
			mux.HandleFunc("/approvals", handler.ServeApprovals)
			mux.HandleFunc("/approval", handler.ServeApproval)
			// The signed-webhook trigger is registered only when a secret is set, so
			// a deployment without one 404s instead of starting runs unauthenticated.
			handler.RegisterWebhookRun(mux)
		},
		settings:    settings,
		workspace:   workspace,
		allowRemote: config.allowRemote,
		dsh:         console,
		stream:      stream,
	})

	return &serveApp{runtime: runtime, handler: mux, settings: settings, workspace: workspace}, nil
}

// serveMuxConfig is the route table's inputs. They are handlers and a
// registration callback rather than the runtime itself so the table can be
// built and asserted in a test without a model, an event store, or a run
// manager; newServeApp is the only production caller.
type serveMuxConfig struct {
	registerHarness func(*http.ServeMux)
	settings        *settingsStore
	workspace       string
	allowRemote     bool
	dsh             http.Handler
	stream          http.Handler
}

// newServeMux assembles every served route. Precedence is the point:
//
//   - the harness routes, the settings API, and the server-info route register
//     their exact patterns and win over the console catch-all;
//   - the DSH console is mounted at "/", where its own handler claims the shell,
//     the staged assets, /plugins/..., and /api/... — the root path is required
//     because the shell's asset URLs are relative.
//
// The console is the only HTML surface serve offers: the interim first-party
// console is deliberately not mounted, so a browser that asks for /classic/
// falls through to the console's own 404 rather than a second interface.
func newServeMux(cfg serveMuxConfig) *http.ServeMux {
	mux := http.NewServeMux()
	if cfg.registerHarness != nil {
		cfg.registerHarness(mux)
	}
	mux.HandleFunc("/api/settings", cfg.settings.serveHTTP)
	mux.HandleFunc("/api/server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeServeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "server info requires GET")
			return
		}
		writeServeJSON(w, http.StatusOK, map[string]any{
			"workspace":   cfg.workspace,
			"allowRemote": cfg.allowRemote,
		})
	})
	if cfg.stream != nil {
		mux.Handle(dshstream.MuxPath, cfg.stream)
		mux.Handle(dshstream.EventsResultPath, cfg.stream)
	}
	mux.Handle("/", cfg.dsh)
	return mux
}

// isLoopbackListenAddr reports whether addr proves it binds only the loopback
// interface. A hostname is not proof — it may resolve anywhere — so only a
// loopback IP literal or "localhost" passes; an unparsable or wildcard
// address is treated as remote and needs --allow-remote.
func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackRemoteAddr reports whether an HTTP peer is on this machine. The
// peer address is the only signal available without adding authentication, so
// a parse failure is treated as not-loopback: refusing a settings change is
// recoverable, handing a remote page the server's key is not.
func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// swappableModel is the model adapter the console's settings act on. The
// agent holds this value for its whole life, so a settings POST replaces the
// adapter behind it and the next model call — the next step of a running
// agent or the first call of a newly started one — uses the new endpoint.
type swappableModel struct {
	mu    sync.RWMutex
	inner model.Model
	err   error
}

func newSwappableModel() *swappableModel {
	return &swappableModel{}
}

func (m *swappableModel) set(inner model.Model, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inner = inner
	m.err = err
}

func (m *swappableModel) current() (model.Model, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.inner == nil {
		if m.err != nil {
			return nil, fmt.Errorf("model is not configured: %w", m.err)
		}
		return nil, errors.New("model is not configured")
	}
	return m.inner, nil
}

func (m *swappableModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	inner, err := m.current()
	if err != nil {
		return nil, err
	}
	return inner.Generate(ctx, req)
}

func (m *swappableModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	inner, err := m.current()
	if err != nil {
		return nil, err
	}
	return inner.Stream(ctx, req)
}

// serverSettings is the in-memory model configuration. It is seeded from the
// CLI, environment, and config layers at startup; a value set from the
// console overrides the seed until the process restarts. The API key lives
// only in this struct: it is never returned by the API, never logged, and
// never written to disk.
type serverSettings struct {
	baseURL  string
	model    string
	provider string
	apiKey   string
	// apiKeyEnv names the environment fallback the CLI was configured with,
	// so hasApiKey can report a key the server would actually use without
	// copying that key into memory.
	apiKeyEnv string
}

// settingsStore owns the current settings and the adapter built from them.
type settingsStore struct {
	mu      sync.RWMutex
	current serverSettings
	model   *swappableModel
	// allowRemote is the operator's --allow-remote decision, carried here so
	// the write gate travels with the store the endpoint serves.
	allowRemote bool
	// consoleSections holds the settings namespaces the console owns: facts
	// about the GUI rather than this host's configuration (ADR 0094). They live
	// in this process with the rest of the console-written settings, which is
	// what the document-less profile this host reports means.
	consoleSections map[string]map[string]any
}

// ConsoleSection reports a console-owned namespace's stored section. The map is
// copied, so a caller cannot reach into the store's state through it.
func (s *settingsStore) ConsoleSection(namespace string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored := s.consoleSections[namespace]
	section := make(map[string]any, len(stored))
	for key, value := range stored {
		section[key] = value
	}
	return section
}

// SetConsoleSection records one. An empty section is stored as empty rather than
// deleting the namespace, so a cleared field reads back the same way as one that
// was never written.
func (s *settingsStore) SetConsoleSection(namespace string, section map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consoleSections == nil {
		s.consoleSections = make(map[string]map[string]any)
	}
	stored := make(map[string]any, len(section))
	for key, value := range section {
		stored[key] = value
	}
	s.consoleSections[namespace] = stored
	return nil
}

// settingsView is the GET/POST response shape. It deliberately has no field
// for the key: hasApiKey is the only thing an operator or a page learns.
type settingsView struct {
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	HasAPIKey bool   `json:"hasApiKey"`
}

// settingsRequest is the POST body. An absent field is not the same as an
// empty one for the key and the model, which keep their current value when
// empty, while an empty baseUrl clears the override back to the provider
// default.
type settingsRequest struct {
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
}

// setAPIKey records an inline key and rebuilds the adapter from it. It does not
// reuse apply: apply assigns the base URL unconditionally, so a request that
// named only a key would clear the operator's --base-url override.
func (s *settingsStore) setAPIKey(value string) error {
	return s.replaceAPIKey(strings.TrimSpace(value))
}

// clearAPIKey forgets the inline key. An environment-supplied key is left
// alone, so the next hasAPIKey call still reports the credential honestly.
//
// It does not go through replaceAPIKey: that path refuses a configuration the
// provider cannot build, and a host with no key is a configuration this server
// deliberately starts in, so the run path reports the provider's own failure.
// The clearing is committed first and the adapter rebuild records the error,
// exactly as startup does for an operator who has not configured a key yet.
func (s *settingsStore) clearAPIKey() error {
	s.mu.Lock()
	next := s.current
	next.apiKey = ""
	s.current = next
	s.mu.Unlock()
	s.rebuild()
	return nil
}

// replaceAPIKey commits a new inline key after proving the adapter still
// builds. The candidate adapter is constructed before anything is stored, so a
// key the provider rejects leaves the running server untouched -- the same
// ordering apply uses.
func (s *settingsStore) replaceAPIKey(key string) error {
	s.mu.RLock()
	next := s.current
	s.mu.RUnlock()
	next.apiKey = key
	adapter, err := provider.FromEnv(provider.Config{
		Protocol:  next.provider,
		Model:     next.model,
		BaseURL:   next.baseURL,
		APIKey:    next.apiKey,
		APIKeyEnv: next.apiKeyEnv,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	s.model.set(adapter, nil)
	return nil
}

// consoleProviderName is the human-readable name the console's selector shows
// for a route key. A route this host does not recognise keeps its own key
// rather than an invented label.
func consoleProviderName(route string) string {
	switch route {
	case provider.OpenAI:
		return "OpenAI"
	case provider.Anthropic:
		return "Anthropic"
	default:
		return route
	}
}

// replaceEndpoint commits a new endpoint and model while keeping the provider and
// the credential.
//
// It validates what can be checked without a credential and then commits, rather
// than routing through apply: apply refuses a configuration the provider cannot
// build, and a host with no credential is a configuration this server
// deliberately runs in (the run path reports the provider's own failure). The
// console's Models page writes the endpoint before it writes the key, so
// refusing the endpoint for the absence of the key would make the panel's own
// order impossible. A malformed endpoint is still refused before anything is
// stored.
func (s *settingsStore) replaceEndpoint(baseURL, model string) error {
	normalized, err := normalizeSettingsRequest(settingsRequest{BaseURL: baseURL, Model: model})
	if err != nil {
		return err
	}
	s.mu.Lock()
	next := s.current
	next.baseURL = normalized.BaseURL
	if normalized.Model != "" {
		next.model = normalized.Model
	}
	s.current = next
	s.mu.Unlock()
	// A rebuild that cannot build an adapter records the failure instead of
	// returning it, exactly as startup does for an operator who has not
	// configured a key yet.
	s.rebuild()
	return nil
}

// consoleExecutionModes lists the execution presets this host implements. The ids
// are the agent's own mode constants, so a renamed mode cannot drift away from
// the roster the console reads.
func consoleExecutionModes() []dshapi.ConsolePresetMode {
	return []dshapi.ConsolePresetMode{
		{
			ID:   string(zenforge.ModeReact),
			Name: string(zenforge.ModeReact),
			Description: "The normal model/tool loop: the agent reasons, calls tools, " +
				"and repeats until it answers.",
		},
		{
			ID:   string(zenforge.ModeOneshot),
			Name: string(zenforge.ModeOneshot),
			Description: "Caps the model/tool loop at two rounds and then forces a " +
				"no-tool final answer when needed.",
		},
		{
			ID:          string(zenforge.ModePlanExecute),
			Name:        string(zenforge.ModePlanExecute),
			Description: "Plans first, then executes the plan.",
		},
	}
}

// consoleExecutionMode is the preset this host runs: an explicit --mode when one
// was given, otherwise the planning preset when planning is on, otherwise the
// plain model/tool loop. The console reports it as the roster's default because
// this host uses it for every session it starts.
func consoleExecutionMode(opts *options) string {
	if mode := strings.TrimSpace(opts.mode); mode != "" {
		return mode
	}
	if planningMode(opts.planning) == zenforge.PlanningPlanExecute {
		return string(zenforge.ModePlanExecute)
	}
	return string(zenforge.ModeReact)
}

// consolePresets reports the host's execution presets and the sandbox and
// approval settings it runs with, for the console's preset and permission
// selectors (ADR 0088). It is a snapshot of startup flags: the console cannot
// change either here, and the surfaces say so rather than offering a choice
// that would be dropped.
func consolePresets(opts *options) dshapi.PresetSource {
	return func() dshapi.ConsolePresets {
		return dshapi.ConsolePresets{
			Sandbox:     opts.sandboxBackend,
			Approval:    opts.approve,
			DefaultMode: consoleExecutionMode(opts),
			Modes:       consoleExecutionModes(),
		}
	}
}

// consoleCommandCatalog builds the composer's slash-command catalog from the same
// directories `zenforge run` reads (ADR 0090). A catalog that cannot be read
// leaves the source nil, so the namespace answers unimplemented with the
// dependency named rather than the menu reporting a failure it cannot explain.
func consoleCommandCatalog(opts *options) dshapi.CommandSource {
	catalog, err := buildCatalog(*opts)
	if err != nil {
		slog.Warn("console slash commands are disabled: the command catalog could not be read", "error", err)
		return nil
	}
	return consoleCommands{catalog: catalog, opts: *opts}
}

// consoleFileFace builds the console's read-only workspace file face over the
// directory this server serves (ADR 0089). A workspace that cannot be opened
// leaves the face nil, so the namespace answers unimplemented with the dependency
// named rather than the console showing a file tree that does not exist -- and
// the reason is logged once, here, instead of on every request.
func consoleFileFace(opts *options) dshapi.WorkspaceFiles {
	face, err := newConsoleWorkspaceFiles(opts.workspace)
	if err != nil {
		slog.Warn("console file browsing is disabled: the workspace could not be opened", "workspace", opts.workspace, "error", err)
		return nil
	}
	return face
}

// consoleSettings adapts the settings store to the console's settings namespace
// (ADR 0087): one provider profile whose endpoint, model and credential the
// Models page can write. The credential is never part of a reported value, only
// of the secret slot that says whether one is configured.
type consoleSettings struct {
	settings *settingsStore
}

func (c consoleSettings) SettingsProfile() dshapi.SettingsProfile {
	view := c.settings.view()
	return dshapi.SettingsProfile{
		Provider: view.Provider,
		Model:    view.Model,
		BaseURL:  view.BaseURL,
		HasKey:   view.HasAPIKey,
	}
}

func (c consoleSettings) SetSettingsEndpoint(baseURL string) error {
	return c.settings.replaceEndpoint(baseURL, "")
}

func (c consoleSettings) SetSettingsModel(model string) error {
	return c.settings.replaceEndpoint(c.settings.view().BaseURL, model)
}

func (c consoleSettings) SetSettingsKey(value string) error { return c.settings.setAPIKey(value) }

func (c consoleSettings) ClearSettingsKey() error { return c.settings.clearAPIKey() }

// ConsoleSection and SetConsoleSection pass the console-owned namespaces
// straight through: the store holds them, this host's configuration does not
// have to understand them.
func (c consoleSettings) ConsoleSection(namespace string) map[string]any {
	return c.settings.ConsoleSection(namespace)
}

func (c consoleSettings) SetConsoleSection(namespace string, section map[string]any) error {
	return c.settings.SetConsoleSection(namespace, section)
}

// consoleCredentials adapts the settings store to the console's credentials
// namespace (ADR 0084). The host has one model credential, so every reference
// the panel names is answered from it and a stored value replaces it; the value
// is never returned, only whether one would be found.
type consoleCredentials struct {
	settings *settingsStore
}

func (c consoleCredentials) CredentialConfigured() bool { return c.settings.view().HasAPIKey }

func (c consoleCredentials) StoreCredential(value string) error { return c.settings.setAPIKey(value) }

func (c consoleCredentials) RemoveCredential() error { return c.settings.clearAPIKey() }

func (s *settingsStore) view() settingsView {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	return settingsView{
		BaseURL:   current.baseURL,
		Model:     current.model,
		Provider:  current.provider,
		HasAPIKey: s.hasAPIKey(current),
	}
}

// hasAPIKey reports whether a key would be found: an inline key the operator
// or page supplied, or the environment variable the CLI named. It never
// reveals which.
// setModelAdapter installs the adapter a session's selected model names. It
// deliberately does not touch the stored settings: the operator's configuration
// did not change, only which of the models this host can serve the run uses.
func (s *settingsStore) setModelAdapter(adapter model.Model) {
	s.model.set(adapter, nil)
}

// configuredAPIKey reports the operator's own key. It exists for the provider
// profile serviceability check, which must resolve a credential exactly the way a
// run would -- and that check cannot go through settingsView, which deliberately
// carries no key.
func (s *settingsStore) configuredAPIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.apiKey
}

func (s *settingsStore) hasAPIKey(current serverSettings) bool {
	if strings.TrimSpace(current.apiKey) != "" {
		return true
	}
	if current.apiKeyEnv == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv(current.apiKeyEnv)) != ""
}

// rebuild builds the adapter from the current settings. A failure is recorded
// rather than returned: at startup the server must still come up so the
// operator can fix the settings in the browser, and the error surfaces from
// the model call a run makes.
func (s *settingsStore) rebuild() {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	adapter, err := provider.FromEnv(provider.Config{
		Protocol:  current.provider,
		Model:     current.model,
		BaseURL:   current.baseURL,
		APIKey:    current.apiKey,
		APIKeyEnv: current.apiKeyEnv,
	})
	s.model.set(adapter, err)
}

// apply validates and commits a settings change. The candidate adapter is
// built before anything is stored, so a request that names an unreachable or
// incomplete configuration leaves the running server untouched.
func (s *settingsStore) apply(req settingsRequest) (settingsView, error) {
	normalized, err := normalizeSettingsRequest(req)
	if err != nil {
		return settingsView{}, err
	}
	s.mu.Lock()
	next := s.current
	next.baseURL = normalized.BaseURL
	if normalized.Model != "" {
		next.model = normalized.Model
	}
	if normalized.Provider != "" {
		next.provider = normalized.Provider
	}
	if normalized.APIKey != "" {
		next.apiKey = normalized.APIKey
	}
	s.mu.Unlock()

	adapter, err := provider.FromEnv(provider.Config{
		Protocol:  next.provider,
		Model:     next.model,
		BaseURL:   next.baseURL,
		APIKey:    next.apiKey,
		APIKeyEnv: next.apiKeyEnv,
	})
	if err != nil {
		return settingsView{}, err
	}

	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	s.model.set(adapter, nil)
	return s.view(), nil
}

// normalizeSettingsRequest trims the fields and validates what can be checked
// without building an adapter. Unknown fields are rejected by the decoder, so
// a typo'd field is an error instead of a silent no-op.
func normalizeSettingsRequest(req settingsRequest) (settingsRequest, error) {
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.Model = strings.TrimSpace(req.Model)
	req.Provider = strings.TrimSpace(req.Provider)
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.Provider != "" {
		normalized := strings.ToLower(req.Provider)
		if normalized != provider.OpenAI && normalized != provider.Anthropic {
			return settingsRequest{}, fmt.Errorf("provider must be %s or %s", provider.OpenAI, provider.Anthropic)
		}
		req.Provider = normalized
	}
	if req.BaseURL != "" {
		parsed, err := url.Parse(req.BaseURL)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" {
			return settingsRequest{}, errors.New("baseUrl must be an absolute URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return settingsRequest{}, errors.New("baseUrl must use http or https")
		}
	}
	return req, nil
}

// serveHTTP is the settings endpoint. Reads are open on the bound address;
// writes are restricted to the machine running the server unless the operator
// already chose to expose it with --allow-remote. Without that gate, a page
// from anywhere could point this server at an attacker's endpoint or hand it
// a key the operator never intended to use.
func (s *settingsStore) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeServeJSON(w, http.StatusOK, s.view())
	case http.MethodPost:
		if !s.allowRemote && !isLoopbackRemoteAddr(r.RemoteAddr) {
			writeServeError(w, http.StatusForbidden, "settings_forbidden", "settings changes are restricted to the machine running the server; restart with --allow-remote to permit remote changes")
			return
		}
		var req settingsRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeServeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeServeError(w, http.StatusBadRequest, "invalid_json", "settings body must contain exactly one JSON object")
			return
		}
		view, err := s.apply(req)
		if err != nil {
			writeServeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
			return
		}
		writeServeJSON(w, http.StatusOK, view)
	default:
		writeServeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "settings requires GET or POST")
	}
}

func writeServeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeServeError mirrors the harness error shape so one client-side parser
// reads every endpoint's failure.
func writeServeError(w http.ResponseWriter, status int, code, message string) {
	writeServeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}
