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
	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/goals"
	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshmount"
	"github.com/feiyu912/zenforge/internal/dshstream"
	"github.com/feiyu912/zenforge/internal/dshwire"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/provider"
	"github.com/feiyu912/zenforge/server/auth"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

const (
	// defaultServeAddr is loopback because the run and settings APIs are
	// unauthenticated unless the operator asks for tokens: the operator is
	// expected to be sitting at the machine that runs the server, and
	// --allow-remote is the explicit decision to expose it further -- which now
	// also requires tokens unless --allow-anonymous-remote is given (ADR 0141).
	defaultServeAddr = "127.0.0.1:8787"
	// defaultServeRunTimeout bounds one served run, matching the detached
	// example harness: a browser tab that is closed must not leave a run
	// running for the lifetime of the server.
	defaultServeRunTimeout = 10 * time.Minute
	// maxSettingsBodyBytes caps the settings body. The fields are short, and
	// an endpoint that accepts a key should not accept an unbounded upload.
	maxSettingsBodyBytes = 1 << 16
	// serveConsoleOn and serveConsoleOff are the only two values --console
	// accepts. The console is opt-out rather than opt-in because serving it is
	// what `zenforge serve` has always done and a default that changed would
	// silently take a surface away from every existing deployment; off is the
	// headless mode a caller who wants the harness API alone asks for, and it is
	// the runtime half of the layering rule in ADR 0099.
	serveConsoleOn  = "on"
	serveConsoleOff = "off"
	// consoleDisabledCode is the error code a console-off host answers every
	// path it does not otherwise serve with. It names the missing capability
	// rather than the absent file, which is what lets a client tell "this host
	// runs headless" from "this route never existed".
	consoleDisabledCode = "console_disabled"
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
	// The settings document has a default the operator rarely needs to name: this
	// host's own configuration directory. The flag is for the case where that is
	// not where they want their credential kept, and -- like every other secret
	// flag here -- it is taken verbatim.
	settingsFile := fs.String("settings-file", "", "file the console's settings and credential persist to; defaults to console-settings.json in the host configuration directory")
	// The caller-identity flags. --allow-remote used to be the only gate on a
	// network-bound host, and it opened every route at once: the tokens below are
	// what a deployment exposes instead, so a remote peer has to name itself.
	authTokenFile := fs.String("auth-token-file", "", "file the tokens this host accepts are kept in; defaults to tokens.json in the host configuration directory")
	requireAuth := fs.Bool("require-auth", false, "serve only requests that present a valid token; implied by --allow-remote unless --allow-anonymous-remote is given")
	allowAnonymousRemote := fs.Bool("allow-anonymous-remote", false, "let --allow-remote expose this host without tokens (only for a network you already trust)")
	auditLogFile := fs.String("audit-log", "", "file every admission decision is appended to; defaults to audit.jsonl in the host configuration directory whenever authentication is required")
	// The secret falls back to the environment so it is not visible in argv,
	// and an empty secret disables the signed webhook route rather than
	// allowing an unauthenticated run trigger.
	webhookSecret := fs.String("webhook-secret", strings.TrimSpace(os.Getenv("ZENFORGE_WEBHOOK_SECRET")), "shared secret for the signed POST /webhook/run trigger; empty disables the endpoint")
	// The console is the one HTML surface this command offers, so it is opt-out:
	// --console=off builds a host that serves the harness routes, /api/server and
	// -- when authentication is configured -- the sign-in page, with no console
	// path, no WebSocket mux and no settings document at all. The sign-in page is
	// the one exception to "no HTML", because a caller without a token has to be
	// able to reach the form that gives it one. It is serve-only -- no other
	// subcommand mounts the console -- so it stays out of the shared option set.
	consoleMode := fs.String("console", serveConsoleOn, "serve the built-in DSH console at / (on), or serve the harness API alone with no console surface (off)")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	if err := resolveExecutionFlags(fs, &opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateOptionEnums(opts); err != nil {
		return invalidUsage(err)
	}
	if err := validateConsoleMode(*consoleMode); err != nil {
		return invalidUsage(err)
	}
	// --settings-file names the console's settings document, and a headless host
	// neither reads nor writes one: it takes its model from the flags the way
	// `zenforge run` does. Accepting the flag there would drop what an operator
	// asked for without saying so, which is the failure this whole command
	// refuses elsewhere -- a host that cannot do what it was told to refuses to
	// start instead of starting differently.
	if !consoleEnabled(*consoleMode) && strings.TrimSpace(*settingsFile) != "" {
		return invalidUsage(errors.New("--settings-file names the console's settings document, which a host started with --console=off never reads or writes; configure that host with --provider, --model, --api-key and --base-url instead"))
	}
	if *runTimeout <= 0 {
		return invalidUsage(errors.New("--run-timeout must be positive"))
	}
	// The check is deliberately about the address that will be bound, not
	// about the string: a bind to "0.0.0.0" or "[::]" reaches the network
	// even though neither string looks like a name, so anything not provably
	// loopback has to be opted into.
	if !*allowRemote && !isLoopbackListenAddr(*addr) {
		return invalidUsage(fmt.Errorf("--addr %q is not a loopback address; pass --allow-remote to bind it deliberately, which now also requires tokens unless --allow-anonymous-remote is given", *addr))
	}

	// The identity decision is made before anything is built, and fails closed:
	// a host asked to require tokens that holds none would only refuse every
	// caller, and one that cannot keep an audit trail would be unaccountable.
	authConfig, err := resolveServeAuth(serveAuthOptions{
		requireAuth:          *requireAuth,
		allowRemote:          *allowRemote,
		allowAnonymousRemote: *allowAnonymousRemote,
		tokenFile:            *authTokenFile,
		auditLog:             *auditLogFile,
		workspace:            opts.workspace,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := authConfig.close(); err != nil {
			_, _ = fmt.Fprintf(ioStreams.Stderr, "warning: closing the audit log: %v\n", err)
		}
	}()
	// A running host has to see a mint or a revoke made in another process; the
	// request path reads the in-memory set and this is what refreshes it.
	go authConfig.watchTokens(ctx, authTokenReloadInterval)

	// Registered before anything is built so every path out releases the
	// stores and MCP processes buildAgentConfig opens.
	defer drainClosers(&opts, ioStreams)

	app, err := newServeApp(ctx, &opts, ioStreams, serveConfig{
		runTimeout:    *runTimeout,
		webhookSecret: *webhookSecret,
		allowRemote:   *allowRemote,
		settingsFile:  *settingsFile,
		console:       *consoleMode,
		auth:          authConfig,
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
	if consoleEnabled(*consoleMode) {
		_, _ = fmt.Fprintln(ioStreams.Stdout, "open it in a browser; set the model endpoint and API key under the gear icon")
	} else {
		// A headless host tells the operator what it is instead of pointing them
		// at a page that would answer the disabled-console envelope.
		_, _ = fmt.Fprintln(ioStreams.Stdout, "console: disabled (--console=off); this host serves the harness API only")
	}
	if authConfig.required {
		_, _ = fmt.Fprintf(ioStreams.Stdout, "authentication: required; sign in at http://%s%s, and present tokens as `Authorization: Bearer <token>`\n", listener.Addr(), auth.SignInPath)
		if authConfig.audit != nil {
			_, _ = fmt.Fprintf(ioStreams.Stdout, "audit trail: %s\n", authConfig.audit.Path())
		}
	} else {
		_, _ = fmt.Fprintln(ioStreams.Stdout, "authentication: not required; every caller that can reach this host is served (--require-auth changes that)")
	}

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

// runRegistryPath is where the host keeps the run registry the console lists
// sessions from: inside the event store directory for the JSONL store, and
// beside the store file for the SQLite one, because that option names a file
// rather than a directory. The directory is created when it is missing, since a
// fresh install has no state directory until its first run.
func runRegistryPath(opts *options) (string, error) {
	stateDir := opts.checkpointDir
	if stateDir == "" {
		return "", fmt.Errorf("a run registry needs a state directory: set --checkpoint-dir")
	}
	if strings.EqualFold(opts.checkpointType, "sqlite") {
		stateDir = filepath.Dir(stateDir)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", fmt.Errorf("create the state directory %s: %w", stateDir, err)
	}
	return filepath.Join(stateDir, "run-registry.sqlite"), nil
}

// adoptStoredRuns records the runs the state directory already holds but the
// registry does not know. A host that starts with an empty registry would list
// nothing until its next turn, and the console has no other way to reach a
// transcript that is still on disk: the sidebar row is the only door (ADR 0109).
//
// Each run's own log decides its terminal state, because that is the writer of
// record; a run whose log ends without a terminal event is recorded as cancelled,
// which is what an interrupted run is. A run another process is still executing
// is left alone: its unexpired lease refuses the claim.
func adoptStoredRuns(ctx context.Context, events eventlog.Store, registry harnesshttp.RunRegistry, opts *options) error {
	stored, err := storedRuns(ctx, events, opts)
	if err != nil {
		return err
	}
	adopted := 0
	for _, run := range stored {
		if _, err := registry.Get(ctx, run.RunID); err == nil {
			continue
		}
		info := storedRunInfo(ctx, events, run)
		lease, err := registry.Claim(ctx, harnesshttp.RunClaim{
			RunID: run.RunID, OwnerID: "zenforge-serve", Status: harnesshttp.RunStarting,
			// The lease is only held across the release below; its expiry is what
			// makes a record left by a dead process readable as finished.
			LeaseUntil: time.Now().UTC().Add(30 * time.Second),
			StartedAt:  info.StartedAt, UpdatedAt: info.UpdatedAt,
		})
		if err != nil {
			// Another process is still executing the run: its unexpired lease
			// refuses the claim, and its own host will record the outcome.
			continue
		}
		if err := registry.Release(ctx, lease, info); err != nil {
			return fmt.Errorf("record the stored run %s: %w", run.RunID, err)
		}
		adopted++
	}
	if adopted > 0 {
		slog.Info("adopted the runs already in the state directory",
			"runs", adopted, "dir", opts.checkpointDir)
	}
	return nil
}

// storedRun is one run the state directory holds, with the newest checkpoint
// time when the store that named it has one.
type storedRun struct {
	RunID   string
	SavedAt time.Time
}

// storedRuns enumerates the runs already in the state directory. The event
// store's own listing is the wide one -- it holds a run even when its start
// failed before a checkpoint existed -- and the checkpoint summaries are the
// fallback for a store that cannot enumerate.
func storedRuns(ctx context.Context, events eventlog.Store, opts *options) ([]storedRun, error) {
	if lister, ok := events.(eventlog.RunLister); ok {
		ids, err := lister.RunIDs(ctx)
		if err != nil {
			return nil, fmt.Errorf("list the runs already in %s: %w", opts.checkpointDir, err)
		}
		out := make([]storedRun, 0, len(ids))
		for _, runID := range ids {
			out = append(out, storedRun{RunID: runID})
		}
		return out, nil
	}
	summaries, closeStore, err := listRuns(ctx, opts.checkpointType, opts.checkpointDir)
	if err != nil {
		return nil, fmt.Errorf("list the runs already in %s: %w", opts.checkpointDir, err)
	}
	defer func() { _ = closeStore() }()
	out := make([]storedRun, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, storedRun{RunID: summary.RunID, SavedAt: summary.SavedAt})
	}
	return out, nil
}

// storedRunInfo reads a stored run's log for the state the console needs: its
// terminal status, and when it started and last moved. The durable log is the
// single source -- a checkpoint can lag the run it belongs to -- and a run with
// no readable log keeps the checkpoint's timestamp, or now when nothing else
// dates it, because a registry claim requires one.
func storedRunInfo(ctx context.Context, events eventlog.Store, run storedRun) harnesshttp.RunInfo {
	at := run.SavedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	info := harnesshttp.RunInfo{
		RunID:      run.RunID,
		Status:     harnesshttp.RunCancelled,
		StartedAt:  at,
		UpdatedAt:  at,
		FinishedAt: at,
	}
	stored, err := events.Read(ctx, run.RunID, 0, 0)
	if err != nil || len(stored) == 0 {
		return info
	}
	info.StartedAt = time.UnixMilli(stored[0].Timestamp).UTC()
	if newest := time.UnixMilli(stored[len(stored)-1].Timestamp).UTC(); newest.After(info.UpdatedAt) {
		info.UpdatedAt = newest
		info.FinishedAt = newest
	}
	for _, event := range stored {
		switch event.Type {
		case zenforge.EventRunDone:
			info.Status = harnesshttp.RunCompleted
		case zenforge.EventRunError:
			info.Status = harnesshttp.RunFailed
		case zenforge.EventRunCancelled:
			info.Status = harnesshttp.RunCancelled
		}
	}
	return info
}

// serveConfig is the small set of serve-only knobs, kept separate from the
// shared options so newServeApp can be called by a test without a flag set.
type serveConfig struct {
	runTimeout    time.Duration
	webhookSecret string
	allowRemote   bool
	// settingsFile is the --settings-file decision: the path the console's
	// settings document lives at, or empty for the host's own configuration
	// directory (ADR 0102).
	settingsFile string
	// console is the --console decision: serve the DSH console at "/" (on), or
	// run headless with no console surface at all (off). An empty value is
	// the zero serveConfig a test builds, and it means on, so the default this
	// field protects is the flag's own default.
	console string
	// auth is the caller-identity decision: the tokens this host accepts, whether
	// it requires one, and the audit trail it appends every decision to. Nil means
	// the tests' default: no tokens, nothing required, nothing recorded.
	auth *serveAuth
}

// consoleEnabled reports whether a --console value asks for the console. It is a
// function of the value rather than a method so the startup line and the
// assembly below cannot disagree about what "off" means.
func consoleEnabled(mode string) bool {
	return !strings.EqualFold(strings.TrimSpace(mode), serveConsoleOff)
}

// consoleEnabled reports whether this host was asked to serve the console.
func (c serveConfig) consoleEnabled() bool { return consoleEnabled(c.console) }

// validateConsoleMode refuses a --console value this host does not implement, in
// the same shape validateSandboxBackend uses: the value and the accepted list
// are both named, so a typo is a usage error rather than a host that silently
// serves -- or silently withholds -- the console.
func validateConsoleMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case serveConsoleOn, serveConsoleOff:
		return nil
	default:
		return fmt.Errorf("unknown console mode %q (want on or off)", mode)
	}
}

// requireServeModel refuses a host that has neither a console nor a model.
//
// A console host may start with no credential: the settings panel is where an
// operator pastes one, and the run path reports the provider's failure when a
// run needs it. A headless host has no page to fix anything on, so starting
// without a model would mean serving an address whose every run fails with a
// provider error the caller cannot repair. It fails closed here instead, the way
// the identity rules in this file refuse a host that cannot authenticate.
//
// The flags are named because the provider's own error names an environment
// variable, and an operator who passed --provider or --model would not otherwise
// learn that the flag was accepted and the host still has no model. An explicit
// model override counts as configured: it is the caller's own adapter, and
// buildAgentConfig deliberately never falls back past one.
func requireServeModel(opts *options) error {
	if opts.modelOverride != nil {
		return nil
	}
	if _, err := buildModel(*opts); err != nil {
		return fmt.Errorf(
			"a host started with --console=off has no settings page to configure a model on, and this one has none: %w; start it with --provider, --model, --api-key and/or --base-url, or with the environment variables those flags read, or serve it with --console=on and fill in the settings panel",
			err)
	}
	return nil
}

// serveApp is the assembled server a command or a test can drive. It exists
// as a value so the tests exercise the real handler through httptest instead
// of binding a port and racing a goroutine to learn it.
type serveApp struct {
	runtime *harnesshttp.Runtime
	handler http.Handler
	// settings is the console's settings store, and nil on a host started with
	// --console=off: that host has no settings document, no /api/settings route,
	// and no console page that could read one. A caller must not assume it.
	settings  *settingsStore
	workspace string
	auth      *serveAuth
}

// Handler returns the fully routed handler: harness routes, the server-info
// route, and the embedded console when one was built, in the same precedence a
// browser sees. /api/settings is part of the console and is only routed on a
// console host.
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
//
// The assembly is in two tiers. What is built unconditionally is what the
// harness and the host's own APIs need: the approval broker, the agent
// configuration and its model, the run registry, the harness runtime, and the
// route table. The console is built by newConsoleAdapter at the end and only
// when this host was asked for it, so --console=off is a host that never
// constructs dshmount, dshstream, the settings document or the console's model
// configuration rather than one that builds a console and hides it (ADR 0099).
func newServeApp(ctx context.Context, opts *options, ioStreams IO, config serveConfig) (*serveApp, error) {
	// Approvals are answered from the browser through /approvals and
	// /approval, so the run's broker is the same in-memory pending broker the
	// HTTP handler exposes. The CLI's interactive broker would read the
	// server's own stdin, where no operator is sitting.
	inbox := approval.NewPendingBroker(0)
	opts.approvalOverride = inbox

	// The served directory. /api/server reports it and the console builds its
	// workspace registry around it; computing it here keeps the two the same
	// value, and it is resolved before anything reads it.
	workspace := opts.workspace
	if absolute, absErr := filepath.Abs(opts.workspace); absErr == nil {
		workspace = absolute
	}

	// The console's model path is the console's alone, and it is all-or-nothing.
	// A console host seeds a settings store from its flags, installs a delegating
	// adapter a settings POST can swap, wires the provider and selection stores the
	// console's pages read, and reads the durable document that outlives a restart.
	// A headless host builds none of it: no store, no document, no swappable
	// adapter. Its model is what `zenforge run` would use -- the flags and the
	// environment, read by buildAgentConfig below -- so --console=off removes the
	// coupling instead of hiding it behind the console's configuration.
	var (
		settings   *settingsStore
		profiles   *consoleProviderProfiles
		models     consoleModels
		selections *consoleModelSelection
	)
	if config.consoleEnabled() {
		// The model is a delegating adapter rather than a concrete one: the agent
		// is assembled once, and a later settings POST has to affect runs that
		// start afterwards without rebuilding the agent (which would drop the
		// tools, stores, and MCP processes already wired). Every model call reads
		// the adapter current at that moment.
		swappable := newSwappableModel()
		opts.modelOverride = swappable

		// The startup seed: what --base-url, --model, --api-key and the config
		// layers produced. The settings document is compared against it so the
		// host can name the fields it overrides.
		seed := serverSettings{
			baseURL:   opts.baseURL,
			model:     opts.model,
			provider:  opts.provider,
			apiKey:    opts.apiKey,
			apiKeyEnv: opts.apiKeyEnv,
		}
		settings = &settingsStore{
			current: seed,
			seed:    seed,
			model:   swappable,

			allowRemote: config.allowRemote,
		}
		profiles = newConsoleProviderProfiles(settings)
		settings.profiles = profiles
		// The model-selection store is built before the document is read, because
		// the document carries the model each session chose and a restart restores
		// it through this store (ADR 0103). It is wired to the settings store both
		// ways: the document's snapshot asks it for the records, and a recorded
		// choice asks the settings store to write the document.
		models = consoleModels{settings: settings, profiles: profiles}
		selections = newConsoleModelSelection(settings, models)
		settings.selections = selections
		// The settings document is read before the adapter is built, so the first
		// run this host serves already uses the endpoint, the model and the
		// credential the operator set in the browser last time (ADR 0102). A
		// document that exists but cannot be read stops the host here, before a
		// listener is opened, with the file named.
		if _, err := newConsoleSettingsDocument(config.settingsFile, workspace, settings, profiles); err != nil {
			return nil, err
		}
		// A missing key at startup is not fatal for a console host: the settings
		// panel exists so an operator can paste one. The build error is kept and
		// reported when a run actually needs the model, instead of refusing to start
		// the server.
		settings.rebuild()
	} else if err := requireServeModel(opts); err != nil {
		return nil, err
	}

	agentConfig, events, err := buildAgentConfig(ctx, opts, ioStreams)
	if err != nil {
		return nil, err
	}
	if config.consoleEnabled() {
		// A run started from a session's selection carries its own adapter, but a
		// resumed one carries only the route its checkpoint froze, so this host
		// must be able to rebuild that route by name. The console's catalog answers
		// for every route it declares, and anything it does not declare at all
		// falls to the CLI's resolver, which is how a workflow script's agent()
		// still names a provider the console never added (ADR 0140).
		//
		// A headless host keeps the CLI's resolver as buildAgentConfig set it,
		// which knows the host's own route and each provider's conventional
		// environment, and nothing else.
		agentConfig.ModelResolver = consoleRouteResolver{models: models, fallback: agentConfig.ModelResolver}
	}
	// The run registry is durable, and it belongs to the host rather than to the
	// console: the harness's own /runs route lists from it with or without a
	// console, and the console's session list is a projection of the same rows.
	// With only the manager's in-process records, a restart -- and the ten-minute
	// terminal retention -- emptied that list while every transcript stayed in the
	// event store. A SQLite registry next to the store keeps the status of every
	// run this install served, and a run whose owner died keeps its active status
	// but loses its lease, which a reader sees as not running (ADR 0109).
	registryPath, err := runRegistryPath(opts)
	if err != nil {
		return nil, err
	}
	registry, err := harnesshttp.OpenSQLiteRunRegistry(ctx, registryPath)
	if err != nil {
		return nil, fmt.Errorf("open the durable run registry at %s: %w", registryPath, err)
	}
	opts.addCloser("run registry", registry.Close)
	if err := adoptStoredRuns(ctx, events, registry, opts); err != nil {
		return nil, err
	}
	runtime, err := harnesshttp.NewRuntime(agentConfig, events, harnesshttp.RuntimeOptions{
		ApprovalInbox: inbox,
		Webhook:       harnesshttp.WebhookOptions{Secret: config.webhookSecret},
		// The identity the boundary resolved reaches every run this host starts
		// through its harness routes, so the run's approval grants are recorded
		// under the caller's own namespace. It does not refuse: the boundary is
		// the one policy, and a second opinion here would be a second thing to
		// keep in step.
		Access: config.auth.accessController(),
		Manager: harnesshttp.RunManagerOptions{
			MaxActive:         16,
			RunTimeout:        config.runTimeout,
			TerminalRetention: 10 * time.Minute,
			Registry:          registry,
			OwnerID:           "zenforge-serve",
		},
	})
	if err != nil {
		return nil, err
	}

	// The console is assembled last and separately, because it is optional: with
	// --console=off this is a nil adapter and not one of the stores, catalogs or
	// handlers below is built. The stores it is handed were built in the guarded
	// block above, which a headless host skips entirely.
	adapter, err := newConsoleAdapter(consoleAdapterConfig{
		opts:       opts,
		config:     config,
		settings:   settings,
		workspace:  workspace,
		profiles:   profiles,
		models:     models,
		selections: selections,
		runtime:    runtime,
	})
	if err != nil {
		return nil, err
	}
	console, stream := adapter.handlers()

	mux := newServeMux(serveMuxConfig{
		auth: config.auth,
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

	return &serveApp{
		runtime:   runtime,
		handler:   mux,
		settings:  settings,
		workspace: workspace,
		auth:      config.auth,
	}, nil
}

// consoleAdapterConfig is what the console-only assembly reads from the core
// assembly: the stores the console's namespaces answer from, the runtime whose
// manager and event store the mount and the stream share, and the serve knobs
// the console mirrors. It is a value rather than a list of arguments so the
// boundary between newServeApp and newConsoleAdapter is one readable call, and
// so a test can build an adapter without rebuilding a host.
type consoleAdapterConfig struct {
	opts *options
	// config carries the console decision itself, plus the one serve knob the
	// console's mount and stream mirror: --allow-remote, which decides whether
	// the console may be reached from off this machine.
	config serveConfig
	// settings is the console's model configuration and credential. The console
	// writes it through its own settings and credentials faces, and /api/settings
	// serves the same store; it is never nil here, because newConsoleAdapter
	// returns before reading it when this host has no console.
	settings *settingsStore
	// workspace is the absolute served directory the console's workspace
	// registry, file face and `@` menu are all built around.
	workspace string
	// profiles and models are the provider stores the Models page reads. They are
	// built by newServeApp because the host's settings document is read there,
	// before the adapter exists, and the document snapshots the profiles -- the
	// console is their reader, not their owner.
	profiles *consoleProviderProfiles
	models   consoleModels
	// selections is the per-session model-choice store the composer writes and
	// the follow stream's model-selection projections read.
	selections *consoleModelSelection
	// runtime is the assembled harness runtime. The console's mount answers
	// unary RPCs through its manager and event store, and its stream carries the
	// same runs' sessions, approvals and live events.
	runtime *harnesshttp.Runtime
}

// consoleAdapter is the console's two served halves: the unary mount at "/" and
// the WebSocket mux at the paths dshstream exports. They are one value because
// they are built from one set of stores, and a host either has both or neither.
type consoleAdapter struct {
	mount  *dshmount.Mux
	stream *dshstream.Handler
}

// handlers returns the handlers the route table mounts, or (nil, nil) on a host
// with no console. A nil adapter is not a failure to report: it is the
// --console=off host, and the route table answers the paths the console would
// have claimed with the disabled-console envelope instead of mounting a surface
// that was never built.
func (a *consoleAdapter) handlers() (http.Handler, http.Handler) {
	if a == nil {
		return nil, nil
	}
	return a.mount, a.stream
}

// newConsoleAdapter builds the DSH console mount and its stream, or returns a
// nil adapter when this host was asked to run headless.
//
// This function is the console tier's assembly, in one place: every store,
// catalog and handler below exists to build a dshmount.Config or a
// dshstream.Config, and none of them is constructed with --console=off. It is
// handed stores newServeApp built in the same guarded block -- the settings
// store, its provider and selection stores -- because those have to exist before
// the settings document is read and before the run's route resolver is wired,
// both of which happen before the adapter is assembled.
//
// The mount is also where a roster that cannot boot fails, before a listener is
// opened: a console host either serves a working console or does not start.
func newConsoleAdapter(cfg consoleAdapterConfig) (*consoleAdapter, error) {
	// A headless host returns before any of these stores is read, so the nil
	// settings, profiles and selections its config carries are never touched.
	if !cfg.config.consoleEnabled() {
		return nil, nil
	}
	// The console groups sessions by workspace, and this host runs every
	// session in one directory: the registry it hands the console is built
	// around that directory (ADR 0101). A workspace that cannot be opened
	// leaves the face nil, so the namespace answers unimplemented with the
	// dependency named instead of showing an empty registry -- and the reason
	// is logged once, here, instead of on every request.
	workspaces, workspacesErr := newConsoleWorkspaces(cfg.workspace)
	if workspacesErr != nil {
		slog.Warn("console workspace grouping is disabled: the host workspace could not be opened",
			"workspace", cfg.workspace, "error", workspacesErr)
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
	// console requires (its RPC base is hard-wired to the page origin).
	// The console's model selector asks the host what it is configured with.
	// The settings store is the answer, so the picker reports the operator's
	// own endpoint and model instead of an invented one; an unconfigured host
	// returns an empty catalog and the console says so.
	modelCatalog := cfg.models.Catalog
	// The console's goal dock reads and mutates a session's goal through the
	// goals/* namespace. The state is the framework's own goal store, in the same
	// directory the command line and the goal tools use, so a goal is the same
	// goal whichever surface created it (ADR 0125).
	consoleGoalStore := newConsoleGoals(goals.NewFileStore(goalStorePath(cfg.opts.checkpointDir)))
	// The Models page loads its provider directory before it renders any card,
	// so a missing answer is not a missing nicety: the page reports that loading
	// the directory failed and shows nothing. The live half is the route this
	// host is configured to serve; the configurable half is every route the
	// host's own adapter factory accepts.
	llmDirectory := func() dshapi.LlmDirectory {
		liveProvider := strings.TrimSpace(cfg.settings.view().Provider)
		if liveProvider == "" {
			liveProvider = provider.OpenAI
		}
		configurable := []dshapi.LlmConfigurableProvider{}
		// Every configurable route carries its address in the pi-ai namespace: the
		// built-in routes are seeded there with the host's own configuration, and
		// hand-declared routes follow them (ADR 0122). A route advertised under a
		// namespace name the console does not know renders as a card with no fields
		// and a disabled save, which is what the Models page used to show.
		for _, status := range cfg.profiles.ProviderProfiles() {
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
	// A projected transcript labels each assistant message with the model that
	// answered. The session's own choice wins where there is one (the console's
	// selection store reports it); this is the fallback: the route and model the
	// host was configured to serve, which is exactly what a session that chose
	// nothing runs on.
	modelDefault := func() dshwire.Identity {
		view := cfg.settings.view()
		route := strings.TrimSpace(view.Provider)
		if route == "" {
			route = provider.OpenAI
		}
		return dshwire.Identity{Provider: route, Model: strings.TrimSpace(view.Model)}
	}
	// The attachment store is built once and handed to both halves: the mount
	// publishes what a prompt carried, and the stream reads it back so the
	// console's transcript of that turn shows it (ADR 0139).
	attachments := consoleAttachments(cfg.opts.checkpointDir, cfg.opts.workspace)
	mount, err := dshmount.New(cfg.runtime.Manager, cfg.runtime.Events, dshmount.Config{
		AllowRemote:      cfg.config.allowRemote,
		ModelCatalog:     modelCatalog,
		ModelDefault:     modelDefault,
		Logger:           slog.Default(),
		Credentials:      consoleCredentials{settings: cfg.settings},
		LlmDirectory:     llmDirectory,
		Settings:         consoleSettings{settings: cfg.settings},
		ProviderProfiles: cfg.profiles,
		ModelSelections:  cfg.selections,
		Presets:          consolePresets(cfg.opts),
		WorkspaceFiles:   consoleFileFace(cfg.opts),
		// The console's `@` menu reads the same directory the file browser does
		// (ADR 0134), so a path it offers is a path the host can read.
		FileReferences: consoleFileReferences(cfg.opts.workspace),
		// The console's attachments live in the host's own state tree, next to the
		// sessions that refer to them (cli/attachments.go).
		Attachments: attachments,
		Commands:    consoleCommandCatalog(cfg.opts),
		Workspaces:  workspaceRegistry,
		Goals:       consoleGoalStore,
		// The skills panel reads the same catalog the runs advertise (ADR 0131),
		// so the panel cannot list a skill the agent would not be able to load.
		Skills: consoleSkillCatalog(*cfg.opts),
	})
	if err != nil {
		return nil, err
	}

	// The console cannot follow a session over unary RPC: sessions, live events
	// and approvals travel on the WebSocket mux, and an approval is answered on
	// its own route. Both are the same handler, mounted at the paths that
	// package exports, so the two halves cannot drift apart.
	stream, err := dshstream.New(cfg.runtime.Manager, cfg.runtime.Events, cfg.runtime.ApprovalInbox, dshstream.Config{
		AllowRemote:           cfg.config.allowRemote,
		InputAttachments:      dshapi.PromptInputAttachments(consolePromptAttachments(attachments, context.Background())),
		ModelSelections:       cfg.selections.States,
		ModelSelectionUpdates: cfg.selections.Updates,
		Workspaces:            workspaceBaseline,
		WorkspaceUpdates:      workspaceUpdates,
		// The console opens a session's history the moment it creates it, before
		// the first prompt has started a run. The RPC handler is the only place
		// that knows which sessions those drafts are, so the follow stream asks it
		// rather than refusing a session this host created (ADR 0104).
		DraftSessions: mount.IsDraftSession,
		// The same provenance label session/page writes, resolved the same way
		// (the session's choice first) so the transcript reads the same live and
		// after a reload.
		ModelDefault: modelDefault,
		// The goal dock's two live carriers: the control stream's projection
		// frames and the `goal/activation-changed` emit both ride this change
		// feed, so the two cannot disagree about a commit.
		Goals:       consoleGoalStore.Projection,
		GoalUpdates: consoleGoalStore.Updates,
		// The console's pending queue: the messages a prompt queued while a turn
		// was running, in the `inbox` cell the queue rows and the submission-echo
		// retirement read, and the change feed the control stream's frames ride
		// (ADR 0130).
		Queue:        mount.PendingQueue,
		QueueUpdates: mount.PendingQueueUpdates,
	})
	if err != nil {
		return nil, err
	}
	return &consoleAdapter{mount: mount, stream: stream}, nil
}

// serveMuxConfig is the route table's inputs. They are handlers and a
// registration callback rather than the runtime itself so the table can be
// built and asserted in a test without a model, an event store, or a run
// manager; newServeApp is the only production caller.
type serveMuxConfig struct {
	// auth is the admission policy every route below is wrapped in. Nil means this
	// host decides nothing about its callers, which is what a test that only cares
	// about routing wants.
	auth            *serveAuth
	registerHarness func(*http.ServeMux)
	// settings is the console's settings store, and nil on a host with no
	// console. A nil store means /api/settings is not registered, so it reaches
	// the disabled-console catch-all instead of a handler over a document this
	// host does not have.
	settings    *settingsStore
	workspace   string
	allowRemote bool
	// dsh is the console mount, or nil on a host started with --console=off. A
	// nil one is not a routing mistake: the root catch-all below mounts the
	// disabled-console envelope for it.
	dsh http.Handler
	// stream is the console's WebSocket mux, nil on the same host. Its paths are
	// simply not registered then, so they reach the same disabled envelope.
	stream http.Handler
}

// newServeMux assembles every served route. Precedence is the point:
//
//   - the harness routes and the server-info route register their exact patterns
//     and win over the console catch-all; /api/settings is the console's
//     settings-document API and is registered with them only on a console host;
//   - the DSH console is mounted at "/", where its own handler claims the shell,
//     the staged assets, /plugins/..., and /api/... — the root path is required
//     because the shell's asset URLs are relative;
//   - on a host built with --console=off there is no console handler, no stream
//     and no settings store, so the same catch-all answers the console's paths —
//     /api/settings among them — with the 404 console_disabled envelope.
//
// The console is the only console surface serve offers: the interim first-party
// console is deliberately not mounted, so a browser that asks for /classic/
// falls through to the console's own 404 rather than a second interface. (The
// sign-in page at /auth is a route, not a console, and it is served in either
// mode when authentication is configured.)
//
// The returned handler is the admission policy wrapped around the table when one
// was configured (ADR 0141). It is a handler rather than the mux itself so the
// policy cannot be forgotten by a caller that assembles a listener by hand.
func newServeMux(cfg serveMuxConfig) http.Handler {
	mux := http.NewServeMux()
	if cfg.registerHarness != nil {
		cfg.registerHarness(mux)
	}
	// The sign-in routes are registered before the console's catch-all. They are
	// the only paths a caller without a token may reach, because a browser with no
	// session has to be able to reach the form that gives it one.
	if cfg.auth != nil {
		authenticator := cfg.auth.authenticator()
		mux.HandleFunc(auth.SignInPath, authenticator.ServeSignInPage)
		mux.HandleFunc(auth.SessionPath, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost:
				authenticator.ServeSignIn(w, r)
			case http.MethodGet:
				authenticator.ServeCurrent(w, r)
			case http.MethodDelete:
				authenticator.ServeSignOut(w, r)
			default:
				auth.WriteRefusal(w, auth.Refusal{
					Status:  http.StatusMethodNotAllowed,
					Code:    "method_not_allowed",
					Message: "the session route answers GET, POST and DELETE",
				})
			}
		})
	}
	// /api/settings is the console's settings-document API: its POST rewrites the
	// model this host runs on through that document. A host built with
	// --console=off has no document to read or write (cfg.settings is nil), so the
	// route is not registered at all and falls to the catch-all below with every
	// other console path.
	if cfg.settings != nil {
		mux.HandleFunc("/api/settings", cfg.settings.serveHTTP)
	}
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
	if cfg.dsh != nil {
		mux.Handle("/", cfg.dsh)
	} else {
		// A host started with --console=off has no console to mount, and the
		// catch-all is still needed: without it, a path none of the exact routes
		// claimed would be answered by net/http's own plain-text 404 instead of
		// this host's envelope, and a client that already parses this host's
		// failures would need a second parser for one answer.
		mux.Handle("/", consoleDisabledHandler())
	}
	// The boundary wraps the assembled table rather than any one route: it is the
	// only layer that sees the console's /api/*, the harness routes, the settings
	// API, the sign-in routes and both streams, which is what makes it the one
	// place that decides who is served.
	return cfg.auth.wrap(mux)
}

// consoleDisabledHandler answers every path a console would have claimed on a
// host started with --console=off. It is what makes the console optional at
// runtime rather than only at compile time: with no console mounted, the paths
// it owned -- the shell, the staged assets, /plugins/..., the settings-document
// API at /api/settings, and every /api/<namespace>/<method> the console's client
// declares -- reach this handler instead of an empty page.
//
// The answer is a 404 with the host's usual envelope and the code
// console_disabled, not a 501: the path is genuinely not part of this host's
// API, and a client probing /api/session/page must read "no such surface here"
// rather than "this host is broken". The message names the flag, because an
// operator who forgot which mode a deployment runs in has to be able to tell
// from the response.
//
// It writes no HTML: a missing API path has to answer in the envelope every
// other failure of this host uses, and the one page this host could render --
// the sign-in form -- belongs to its own route rather than to a fallback.
func consoleDisabledHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeServeError(w, http.StatusNotFound, consoleDisabledCode,
			"the DSH console is not served by this host: it was started with --console=off, so only the harness routes, /api/server and (when authentication is configured) the sign-in routes are answered here")
	})
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
// CLI, environment, and config layers at startup and then from the settings
// document, which overrides the seed for every field it names; a value set from
// the console overrides both, and is written back into the document so the next
// start reads it again. The API key lives in this struct and in that one `0600`
// file: it is never returned by the API, never logged, and never written anywhere
// else (ADR 0084, ADR 0102).
type serverSettings struct {
	baseURL  string
	model    string
	provider string
	apiKey   string
	// apiKeyEnv names the environment fallback the CLI was configured with,
	// so hasApiKey can report a key the server would actually use without
	// copying that key into memory. It is startup configuration, not a console
	// setting, so the document does not carry it: a key the operator keeps in
	// an environment variable stays there rather than being copied into a file.
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
	// here with the rest of the console-written settings, and -- since this host
	// now has a settings document -- they are written to it (ADR 0102).
	consoleSections map[string]map[string]any
	// revisions is each settings namespace's current revision. It lives here,
	// beside the sections it numbers, so the number a console editor fenced
	// against and the document it came from cannot disagree (ADR 0087, ADR 0102).
	revisions map[string]int64
	// document is the durable half. A nil writer leaves this host exactly what it
	// was before ADR 0102: process-local settings that report hasDocument false.
	document *consoleSettingsWriter
	// seed is what the host was started with. It is kept only so the startup line
	// can name the fields the settings document overrides, and no request path
	// reads it.
	seed serverSettings
	// spoken records which settings fields the console itself wrote, as opposed to
	// which ones the host was started with. It is the difference between the user
	// layer and the resolved value: a flag is not a saved setting, and a page that
	// shows one as the other offers to save a configuration nobody saved. A field
	// the console wrote is written to the document and reported as `user` even
	// when it happens to equal the flag (ADR 0103).
	spoken map[string]bool
	// spokenRoutes records the provider namespace each console-written field was
	// written through. A field belongs to the card it was written from, so this is
	// what keeps a value the operator saved on one provider from appearing on
	// another. A field with no recorded route -- one written before this host kept
	// routes, or by a caller with no namespace -- is attributed to the configured
	// provider instead.
	spokenRoutes map[string]string
	// profiles is the declared-provider store whose profiles the document also
	// carries. It is a face rather than the concrete store because the two are
	// built together in newServeApp and each holds the other.
	profiles profileSnapshot
	// selections is the per-session model-selection store, which the document also
	// carries: the model an operator picks in the composer is console-written
	// state like the endpoint, and it comes back with the rest of the document
	// (ADR 0103). Nil means this host keeps selections in the process only.
	selections selectionSnapshot
}

// selectionSnapshot is the part of the model-selection store the settings
// document needs: the sessions whose model an operator chose, and a way to adopt
// the ones a loaded document carries.
type selectionSnapshot interface {
	SelectionRecords() map[string]consoleSelectionRecordFile
	AdoptSelectionRecords(map[string]consoleSelectionRecordFile)
}

// profileSnapshot is the part of the provider-profile store the settings document
// needs: the declared profiles, in declaration order, with their serviceability
// diagnostics.
type profileSnapshot interface {
	ProviderProfiles() []dshapi.ProviderProfileStatus
}

// persist writes the settings document after a committed change. Every mutator
// calls it after it has committed and released its lock, never while holding one:
// the writer pulls a fresh snapshot of the live state, and a snapshot taken under
// the store's own lock would deadlock against the next one.
func (s *settingsStore) persist() error { return s.document.save() }

// documentFile renders the console-written state as the document: the settings the
// console itself wrote, the namespaces the console owns, the revisions, the
// declared provider profiles this store was wired to, and the model each session
// chose (ADR 0102, ADR 0103).
func (s *settingsStore) documentFile() consoleSettingsFile {
	file := consoleSettingsFile{}
	if s == nil {
		return file
	}
	s.mu.RLock()
	current := s.current
	spoken := make(map[string]bool, len(s.spoken))
	for field, written := range s.spoken {
		spoken[field] = written
	}
	routes := make(map[string]string, len(s.spokenRoutes))
	for field, written := range s.spokenRoutes {
		routes[field] = written
	}
	sections := make(map[string]map[string]any, len(s.consoleSections))
	for namespace, section := range s.consoleSections {
		stored := make(map[string]any, len(section))
		for key, value := range section {
			stored[key] = value
		}
		sections[namespace] = stored
	}
	revisions := make(map[string]int64, len(s.revisions))
	for namespace, revision := range s.revisions {
		revisions[namespace] = revision
	}
	s.mu.RUnlock()
	file.Provider = settingsSpokenField(current.provider, spoken["provider"])
	file.Model = settingsSpokenField(current.model, spoken["model"])
	file.BaseURL = settingsSpokenField(current.baseURL, spoken["baseUrl"])
	file.APIKey = settingsSpokenField(current.apiKey, spoken["apiKey"])
	if len(routes) > 0 {
		file.ConsoleRoutes = routes
	}
	if len(sections) > 0 {
		file.ConsoleSections = sections
	}
	if len(revisions) > 0 {
		file.Revisions = revisions
	}
	if s.profiles != nil {
		file.ProviderProfiles = profileRecords(s.profiles.ProviderProfiles())
	}
	if s.selections != nil {
		file.ModelSelections = s.selections.SelectionRecords()
	}
	return file
}

// settingsSpokenField names a document field only where the console wrote it. A
// field nobody wrote is left out, so the seed keeps answering it -- which is what
// stops a host started with --api-key from being told, by a write that only ever
// touched the model, that its credential had been cleared. A field the console did
// write is named even when it equals the flag it was started with: the operator
// saved it, and a document that dropped it would lose the setting the moment they
// stopped passing the flag (ADR 0103).
func settingsSpokenField(current string, spoken bool) *string {
	if !spoken {
		return nil
	}
	return &current
}

// markSpoken records that the console itself wrote each named field, through the
// route it wrote them from. Fields the host was merely started with are never
// marked, which is what keeps a flag out of the user layer and out of the document.
func (s *settingsStore) markSpoken(route string, fields ...string) {
	if s == nil || len(fields) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spoken == nil {
		s.spoken = make(map[string]bool, len(fields))
	}
	if s.spokenRoutes == nil {
		s.spokenRoutes = make(map[string]string, len(fields))
	}
	for _, field := range fields {
		s.spoken[field] = true
		if route != "" {
			s.spokenRoutes[field] = route
		}
	}
}

// SettingsUserSection reports the section the console wrote for one route, so a
// namespace view can report what an operator saved here instead of restating the
// configuration the host was started with (ADR 0103).
func (s *settingsStore) SettingsUserSection(route string) (map[string]any, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	api := map[string]any{}
	if s.spoken["baseUrl"] && s.fieldBelongsToRoute("baseUrl", route) {
		api["baseURL"] = s.current.baseURL
	}
	if s.spoken["model"] && s.fieldBelongsToRoute("model", route) {
		api["model"] = s.current.model
	}
	if len(api) == 0 {
		return nil, false
	}
	return map[string]any{"providers": map[string]any{route: map[string]any{"api": api}}}, true
}

// fieldBelongsToRoute reports whether a console-written field was written through
// one route. The caller holds the read lock. A field with no recorded route is
// attributed to the provider this host is configured with, which is where a
// document written before routes were recorded -- or a write made with no
// namespace -- belongs.
func (s *settingsStore) fieldBelongsToRoute(field, route string) bool {
	written, ok := s.spokenRoutes[field]
	if !ok {
		configured := s.current.provider
		if configured == "" {
			configured = provider.OpenAI
		}
		return configured == route
	}
	return written == route
}

// applyDocument copies a loaded document over the startup seed. A field the
// document leaves out keeps the value the host was started with: the document
// records what the console wrote, and an absent field is one it never spoke about.
// A field it does name is marked spoken, because it was the console's statement
// when it was written and stays one for every later write of the document
// (ADR 0103).
func (s *settingsStore) applyDocument(file consoleSettingsFile) {
	if s == nil {
		return
	}
	s.mu.Lock()
	spoken := map[string]bool{}
	if file.Provider != nil {
		s.current.provider = *file.Provider
		spoken["provider"] = true
	}
	if file.Model != nil {
		s.current.model = *file.Model
		spoken["model"] = true
	}
	if file.BaseURL != nil {
		s.current.baseURL = *file.BaseURL
		spoken["baseUrl"] = true
	}
	if file.APIKey != nil {
		s.current.apiKey = *file.APIKey
		spoken["apiKey"] = true
	}
	if s.spoken == nil {
		s.spoken = map[string]bool{}
	}
	if s.spokenRoutes == nil {
		s.spokenRoutes = map[string]string{}
	}
	for field := range spoken {
		s.spoken[field] = true
	}
	// A route the document recorded is restored with the field: the card a value
	// was saved on is part of what was saved.
	for field, route := range file.ConsoleRoutes {
		s.spokenRoutes[field] = route
	}
	if len(file.ConsoleSections) > 0 {
		if s.consoleSections == nil {
			s.consoleSections = make(map[string]map[string]any, len(file.ConsoleSections))
		}
		for namespace, section := range file.ConsoleSections {
			stored := make(map[string]any, len(section))
			for key, value := range section {
				stored[key] = value
			}
			s.consoleSections[namespace] = stored
		}
	}
	if len(file.Revisions) > 0 {
		if s.revisions == nil {
			s.revisions = make(map[string]int64, len(file.Revisions))
		}
		for namespace, revision := range file.Revisions {
			s.revisions[namespace] = revision
		}
	}
	s.mu.Unlock()
	// The selections are the other store's state, so they are adopted after this
	// store's lock is released: the two are wired to each other and neither takes
	// the other's lock while holding its own.
	if s.selections != nil && len(file.ModelSelections) > 0 {
		s.selections.AdoptSelectionRecords(file.ModelSelections)
	}
}

// SettingsRevision reports one namespace's revision as this host's document has
// it. An unwritten namespace starts at the initial revision, which is what an
// editor's first read fences against.
func (s *settingsStore) SettingsRevision(namespace string) int64 {
	if s == nil {
		return dshapi.SettingsInitialRevision
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if revision, ok := s.revisions[namespace]; ok {
		return revision
	}
	return dshapi.SettingsInitialRevision
}

// AdvanceSettingsRevision records one committed change to a namespace and returns
// the revision the change produced, then writes it through the document.
//
// The write cannot be part of the caller's decision: by the time the handler
// advances a revision the change it numbers has already committed and persisted,
// so a document that will not take the number is put back to the one it holds and
// reported. The field write and the file then still agree, at a revision one
// lower than this process answered with -- the conservative direction, since a
// client fencing against a number the file does not have yet gets a conflict and
// re-reads rather than overwriting something it never saw.
func (s *settingsStore) AdvanceSettingsRevision(namespace string) int64 {
	if s == nil {
		return dshapi.SettingsInitialRevision
	}
	s.mu.Lock()
	if s.revisions == nil {
		s.revisions = make(map[string]int64, 4)
	}
	previous, had := s.revisions[namespace]
	// An unwritten namespace stands at the initial revision, so the change this
	// call numbers moves it to the next one -- the same rule the handler follows
	// when a store does not version its own namespaces.
	standing := dshapi.SettingsInitialRevision
	if had {
		standing = previous
	}
	revision := standing + 1
	s.revisions[namespace] = revision
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		if had {
			s.revisions[namespace] = previous
		} else {
			delete(s.revisions, namespace)
		}
		s.mu.Unlock()
		slog.Warn("the settings revision could not be written to the settings document", "namespace", namespace, "error", err)
	}
	return revision
}

// HasSettingsDocument reports whether this host's console settings are backed by a
// document it has read or written (ADR 0102). It is what `settings/describe`
// answers as hasDocument.
func (s *settingsStore) HasSettingsDocument() bool {
	if s == nil {
		return false
	}
	return s.document.hasDocument()
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
// was never written. The change is then written through the settings document: an
// acknowledgement the console saved is now expected to outlive the host that saved
// it (ADR 0102), and a document that refuses to take it puts the section back.
func (s *settingsStore) SetConsoleSection(namespace string, section map[string]any) error {
	stored := make(map[string]any, len(section))
	for key, value := range section {
		stored[key] = value
	}
	s.mu.Lock()
	if s.consoleSections == nil {
		s.consoleSections = make(map[string]map[string]any)
	}
	previous, had := s.consoleSections[namespace]
	s.consoleSections[namespace] = stored
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		if had {
			s.consoleSections[namespace] = previous
		} else {
			delete(s.consoleSections, namespace)
		}
		s.mu.Unlock()
		return err
	}
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
	// The credential is not part of the provider cards' user layer -- it travels
	// as a secret -- so it is marked with no route.
	s.markSpoken("", "apiKey")
	s.mu.RLock()
	next := s.current
	s.mu.RUnlock()
	next.apiKey = ""
	return s.commitSettings(next)
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
	if _, err := provider.FromEnv(provider.Config{
		Protocol:  next.provider,
		Model:     next.model,
		BaseURL:   next.baseURL,
		APIKey:    next.apiKey,
		APIKeyEnv: next.apiKeyEnv,
	}); err != nil {
		return err
	}
	// Marked only once the key is known to build, so a refused key does not leave
	// the document claiming the console wrote a credential it never accepted.
	s.markSpoken("", "apiKey")
	return s.commitSettings(next)
}

// commitSettings stores one candidate configuration, rebuilds the adapter from
// it, and writes the settings document. A document that cannot be written puts
// back what the store held and returns the failure: a change that is live but not
// durable is the disagreement between the Models page and the next restart that
// this document exists to remove, so a refused write leaves the process and its
// file agreeing on the old value.
func (s *settingsStore) commitSettings(next serverSettings) error {
	s.mu.Lock()
	previous := s.current
	s.current = next
	s.mu.Unlock()
	s.rebuild()
	if err := s.persist(); err != nil {
		s.mu.Lock()
		s.current = previous
		s.mu.Unlock()
		s.rebuild()
		return err
	}
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
func (s *settingsStore) replaceEndpoint(route, baseURL, model string) error {
	normalized, err := normalizeSettingsRequest(settingsRequest{BaseURL: baseURL, Model: model})
	if err != nil {
		return err
	}
	s.mu.RLock()
	next := s.current
	s.mu.RUnlock()
	next.baseURL = normalized.BaseURL
	fields := []string{"baseUrl"}
	if normalized.Model != "" {
		next.model = normalized.Model
		fields = append(fields, "model")
	}
	// The console wrote these fields from one provider card, so they are recorded
	// and reported as that card's user layer from here on (ADR 0103).
	s.markSpoken(route, fields...)
	// A rebuild that cannot build an adapter records the failure instead of
	// returning it, exactly as startup does for an operator who has not
	// configured a key yet.
	return s.commitSettings(next)
}

// replaceModel commits a new default model while leaving the endpoint, the
// provider and the credential alone. It is separate from replaceEndpoint because
// the console's model field is its own write: marking the endpoint as written
// because a model changed would report a saved endpoint the operator never
// entered (ADR 0103).
func (s *settingsStore) replaceModel(route, model string) error {
	normalized, err := normalizeSettingsRequest(settingsRequest{Model: model})
	if err != nil {
		return err
	}
	if normalized.Model == "" {
		return nil
	}
	s.mu.RLock()
	next := s.current
	s.mu.RUnlock()
	next.model = normalized.Model
	s.markSpoken(route, "model")
	return s.commitSettings(next)
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

// SetSettingsEndpoint, SetSettingsModel, SetSettingsKey and ClearSettingsKey are
// the console's field writes. Each mutator marks the field it wrote as the
// console's, which is what puts it in the document and in the page's user layer
// (ADR 0103); these wrappers only name which field the request addressed.
func (c consoleSettings) SetSettingsEndpoint(route, baseURL string) error {
	return c.settings.replaceEndpoint(route, baseURL, "")
}

func (c consoleSettings) SetSettingsModel(route, model string) error {
	return c.settings.replaceModel(route, model)
}

func (c consoleSettings) SetSettingsKey(value string) error { return c.settings.setAPIKey(value) }

func (c consoleSettings) ClearSettingsKey() error { return c.settings.clearAPIKey() }

// SettingsUserSection passes the console-written layer through, so the page's
// provider cards show what an operator saved here and not the flags this host was
// started with (ADR 0103).
func (c consoleSettings) SettingsUserSection(route string) (map[string]any, bool) {
	return c.settings.SettingsUserSection(route)
}

// ConsoleSection and SetConsoleSection pass the console-owned namespaces
// straight through: the store holds them, this host's configuration does not
// have to understand them.
func (c consoleSettings) ConsoleSection(namespace string) map[string]any {
	return c.settings.ConsoleSection(namespace)
}

func (c consoleSettings) SetConsoleSection(namespace string, section map[string]any) error {
	return c.settings.SetConsoleSection(namespace, section)
}

// HasSettingsDocument, SettingsRevision and AdvanceSettingsRevision pass the
// durable half through: the document the store owns is what the console's page
// reads `hasDocument` from, and the revisions it numbers are the fences an editor
// writes back against (ADR 0087, ADR 0102).
func (c consoleSettings) HasSettingsDocument() bool { return c.settings.HasSettingsDocument() }

func (c consoleSettings) SettingsRevision(namespace string) int64 {
	return c.settings.SettingsRevision(namespace)
}

func (c consoleSettings) AdvanceSettingsRevision(namespace string) int64 {
	return c.settings.AdvanceSettingsRevision(namespace)
}

// The settings adapter is pinned to both faces so a method added to either one of
// the console's settings contracts fails the build here rather than answering
// unimplemented at runtime.
var (
	_ dshapi.SettingsDocumentStore = consoleSettings{}
	_ dshapi.SettingsRevisionStore = consoleSettings{}
	_ dshapi.SettingsUserLayer     = consoleSettings{}
)

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

// adapterForModel builds an adapter for the configured route with the given model
// name. It deliberately leaves the live swappable adapter alone: a run that
// resolved the configured route gets its own adapter from this call, so a settings
// change made later reaches the runs that start after it and never a run already
// answering (ADR 0140). The model name is the caller's, because a caller that
// names one -- a checkpoint's frozen route, a workflow script -- is not the
// picker, and the catalog's list is the picker's rule.
func (s *settingsStore) adapterForModel(modelName string) (model.Model, error) {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	return provider.FromEnv(provider.Config{
		Protocol:  current.provider,
		Model:     strings.TrimSpace(modelName),
		BaseURL:   current.baseURL,
		APIKey:    current.apiKey,
		APIKeyEnv: current.apiKeyEnv,
	})
}

// adapterFromSettings builds the adapter the settings as they stand name.
func (s *settingsStore) adapterFromSettings() (model.Model, error) {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	return s.adapterForModel(current.model)
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

// rebuild builds the adapter from the current settings and installs it as the
// live default. A failure is recorded rather than returned: at startup the server
// must still come up so the operator can fix the settings in the browser, and the
// error surfaces from the model call a run makes.
func (s *settingsStore) rebuild() {
	adapter, err := s.adapterFromSettings()
	s.model.set(adapter, err)
}

// apply validates and commits a settings change. The candidate adapter is
// built before anything is stored, so a request that names an unreachable or
// incomplete configuration leaves the running server untouched, and a settings
// document that cannot be written leaves it untouched as well (ADR 0102).
func (s *settingsStore) apply(req settingsRequest) (settingsView, error) {
	normalized, err := normalizeSettingsRequest(req)
	if err != nil {
		return settingsView{}, err
	}
	s.mu.RLock()
	next := s.current
	s.mu.RUnlock()
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
	if _, err := provider.FromEnv(provider.Config{
		Protocol:  next.provider,
		Model:     next.model,
		BaseURL:   next.baseURL,
		APIKey:    next.apiKey,
		APIKeyEnv: next.apiKeyEnv,
	}); err != nil {
		return settingsView{}, err
	}
	// The panel this endpoint serves writes the whole form, so every field the
	// request named is the console's: baseUrl is always assigned (an empty value
	// clears the override), the rest only when they were sent (ADR 0103).
	spoken := []string{"baseUrl"}
	if normalized.Model != "" {
		spoken = append(spoken, "model")
	}
	if normalized.Provider != "" {
		spoken = append(spoken, "provider")
	}
	if normalized.APIKey != "" {
		spoken = append(spoken, "apiKey")
	}
	s.markSpoken(next.provider, spoken...)
	if err := s.commitSettings(next); err != nil {
		return settingsView{}, err
	}
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
