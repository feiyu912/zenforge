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

	// The DSH console is the product surface: its boot-injected shell, staged
	// assets, module bundles, and unary RPCs share one origin, which is what the
	// console requires (its RPC base is hard-wired to the page origin). Building
	// it is where a roster that cannot boot fails, before a listener is opened.
	// The console's model selector asks the host what it is configured with.
	// The settings store is the answer, so the picker reports the operator's
	// own endpoint and model instead of an invented one; an unconfigured host
	// returns an empty catalog and the console says so.
	modelCatalog := func() dshapi.ModelCatalog {
		view := settings.view()
		if view.Model == "" {
			return dshapi.ModelCatalog{}
		}
		provider := view.Provider
		if provider == "" {
			provider = "openai"
		}
		return dshapi.ModelCatalog{
			Default:           dshapi.ModelSelection{Provider: provider, Model: view.Model},
			RoutableProviders: []string{provider},
			Groups: []dshapi.ModelProviderGroup{{
				ID:     provider,
				Name:   provider,
				Models: []dshapi.ModelCatalogModel{{ID: view.Model, Name: view.Model}},
			}},
			Failures: []dshapi.ModelCatalogFailure{},
		}
	}
	console, err := dshmount.New(runtime.Manager, runtime.Events, dshmount.Config{
		AllowRemote:  config.allowRemote,
		ModelCatalog: modelCatalog,
		Logger:       slog.Default(),
		Credentials:  consoleCredentials{settings: settings},
	})
	if err != nil {
		return nil, err
	}

	// The console cannot follow a session over unary RPC: sessions, live events
	// and approvals travel on the WebSocket mux, and an approval is answered on
	// its own route. Both are the same handler, mounted at the paths that
	// package exports, so the two halves cannot drift apart.
	stream, err := dshstream.New(runtime.Manager, runtime.Events, inbox, dshstream.Config{AllowRemote: config.allowRemote})
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
