// Package dshmount assembles the rebranded upstream DSH console into one
// http.Handler: the boot-injected shell, its staged assets, the /plugins
// bundle route, and the /api unary RPC route.
//
// It is the seam between the three pieces that already exist and the serve
// command. internal/dshboot composes and serves the module graph; internal/dshapi
// answers the console's unary RPCs; package dshconsole holds the staged bytes.
// None of them knows where the others are mounted, and the console only boots
// when all of them are reachable from the same origin with the exact paths the
// shell expects. This package fixes those paths once, so cli/serve.go can mount
// the console with a single call and a test can drive the whole surface.
//
// The graph roster is data, not code: roster.json is generated from the pinned
// upstream revision by scripts/gen-dsh-roster.py, because a client bundle
// registers a factory and carries no graph metadata of its own. See the
// package doc comment on roster.go for the derivation and for the staged
// bundles that are deliberately withheld from the advertised graph.
//
// This package intentionally does not implement the WebSocket mux
// (/api/remote.mux) or the settings namespace. The console boots and renders
// without them; live sessions and streaming do not work until they exist.
package dshmount

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/internal/dshboot"
	"github.com/feiyu912/zenforge/server/harnesshttp"
	dshconsole "github.com/feiyu912/zenforge/webui/dsh"
)

// Config carries the mount's policy knobs. AllowRemote is the serve command's
// --allow-remote decision, passed through to the dshapi trust fence so the
// console's RPCs obey the same rule as the settings endpoint.
type Config struct {
	AllowRemote bool
	// ModelCatalog lets the host answer the console's model selector from its
	// own configuration. Without it the method answers an honest unimplemented
	// error rather than a model this host never configured.
	ModelCatalog dshapi.ModelCatalogSource
	// Logger receives the diagnostic line per endpoint the console asks for
	// and this host does not serve. Nil stays silent.
	Logger *slog.Logger
	// Credentials lets the console's Models page read credential state and
	// store the value an operator types. Without it those methods answer an
	// honest unimplemented error (ADR 0084).
	Credentials dshapi.CredentialStore
	// LlmDirectory answers the console's provider directory reads. Without it
	// the Models page reports that loading the directory failed and renders no
	// cards at all.
	LlmDirectory dshapi.LlmDirectorySource
}

// Mux is the assembled console host. It is immutable after New, which is what
// lets every request be answered from tables built at construction rather than
// from work performed per request.
type Mux struct {
	shell         http.Handler
	assets        http.Handler
	plugins       http.Handler
	api           http.Handler
	graph         *dshboot.Graph
	blocked       []BlockedEntry
	shellProvided []string
}

// New composes the boot graph, injects it into the staged shell, builds the
// unary RPC handler, and returns the routed handler. Every failure here is
// fatal and describe-the-cause: a mount that cannot read its roster or resolve
// a bundle must not fall back to serving a raw shell, because a shell without
// the boot graph is the console's guaranteed failure page.
func New(manager *harnesshttp.RunManager, events eventlog.Store, cfg Config) (*Mux, error) {
	manifest, err := parseRoster(rosterJSON)
	if err != nil {
		return nil, err
	}
	entries, blocked, err := buildEntries(dshconsole.Plugins(), manifest)
	if err != nil {
		return nil, err
	}
	bundle, err := dshboot.New(entries)
	if err != nil {
		return nil, fmt.Errorf("dshmount: compose the console boot graph: %w", err)
	}
	shell, err := dshconsole.Index()
	if err != nil {
		return nil, fmt.Errorf("dshmount: read the staged console shell: %w", err)
	}
	api, err := dshapi.New(manager, events, dshapi.Config{AllowRemote: cfg.AllowRemote, Logger: cfg.Logger})
	if err != nil {
		return nil, fmt.Errorf("dshmount: build the console RPC handler: %w", err)
	}
	if cfg.ModelCatalog != nil {
		api.SetModelCatalog(cfg.ModelCatalog)
	}
	if cfg.Credentials != nil {
		api.SetCredentials(cfg.Credentials)
	}
	if cfg.LlmDirectory != nil {
		api.SetLlmDirectory(cfg.LlmDirectory)
	}
	return &Mux{
		shell:         shellHandler(bundle.Inject(string(shell))),
		assets:        dshconsole.Handler(),
		plugins:       bundle.Handler(),
		api:           api,
		graph:         bundle.Graph(),
		blocked:       blocked,
		shellProvided: shellProvidedModules(manifest),
	}, nil
}

// ServeHTTP routes one console request. The switch is on the path prefix rather
// than a nested ServeMux so the /plugins combination URLs (whose query starts
// with a literal '?') and the /api path are matched exactly as the shell builds
// them and never cleaned, redirected, or shadowed. Anything not claimed by the
// console falls through to the staged-asset handler, whose own 404 keeps the
// mount from answering paths it does not own.
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case hasPathPrefix(r.URL.Path, "/plugins/"):
		m.plugins.ServeHTTP(w, r)
	case hasPathPrefix(r.URL.Path, "/api/"):
		m.api.ServeHTTP(w, r)
	case r.URL.Path == "/" || r.URL.Path == "/index.html":
		m.shell.ServeHTTP(w, r)
	default:
		m.assets.ServeHTTP(w, r)
	}
}

// Graph returns the composed wire graph the mount advertises. It is what the
// injected shell carries and what tests walk to prove every advertised URL
// resolves.
func (m *Mux) Graph() *dshboot.Graph {
	return m.graph
}

// Plugins returns the exact-match /plugins route. Exposing it lets a test drive
// the advertised bundle URLs without going through the whole mount, which is
// what the protocol's failure mode requires: a bundle that is advertised but
// not served breaks boot, so both halves need to be checked against the graph.
func (m *Mux) Plugins() http.Handler {
	return m.plugins
}

// Blocked returns the staged bundles deliberately withheld from the advertised
// graph, with the reason for each. It exists so the decision is inspectable
// (and testable) rather than invisible filtering.
func (m *Mux) Blocked() []BlockedEntry {
	return append([]BlockedEntry(nil), m.blocked...)
}

// ShellProvided returns the module specifiers the staged shell seeds, in sorted
// order. A bundle may require one of these without a graph row; the accounting
// test uses the list to prove every scanned external request has a provider.
func (m *Mux) ShellProvided() []string {
	return append([]string(nil), m.shellProvided...)
}

// hasPathPrefix reports whether path is prefix or lies beneath it. A plain
// strings.HasPrefix would let "/pluginsfoo" claim the /plugins route, and the
// bare subtree root ("/plugins", "/api") must still match its own prefix.
func hasPathPrefix(path, prefix string) bool {
	return strings.HasPrefix(path, prefix) || path == strings.TrimSuffix(prefix, "/")
}

// shellHandler serves the injected shell. The content type and no-store cache
// match the staged-shell handler: the shell names fingerprinted assets and
// carries the boot graph, so it must never be reused stale.
func shellHandler(shell string) http.Handler {
	body := []byte(shell)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		header := w.Header()
		header.Set("Content-Type", "text/html; charset=utf-8")
		header.Set("Cache-Control", "no-store")
		header.Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}
