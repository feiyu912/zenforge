package dshmount

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/feiyu912/zenforge/eventlog"
	"github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/internal/dshboot"
	"github.com/feiyu912/zenforge/server/harnesshttp"
	dshconsole "github.com/feiyu912/zenforge/webui/dsh"
)

// newTestMux builds the real mount over an in-memory event store. It resolves
// the committed roster against the embedded plugin tree, so it exercises the
// bytes the server will actually serve.
func newTestMux(t *testing.T) *Mux {
	t.Helper()
	store := memory.New()
	manager := harnesshttp.NewRunManager(nil, store, eventlog.NewBus(), harnesshttp.RunManagerOptions{})
	mux, err := New(manager, store, Config{})
	if err != nil {
		t.Fatalf("dshmount.New: %v", err)
	}
	return mux
}

// The graph is the one wire the console rejects totally when it is wrong, so
// the roster has to compose into a graph the same validator accepts, and every
// advertised row must carry the fields the console reads.
func TestRosterComposesAValidBootGraph(t *testing.T) {
	mux := newTestMux(t)
	graph := mux.Graph()
	if graph == nil {
		t.Fatal("the mount exposed no boot graph")
	}
	if err := dshboot.ValidateGraph(graph); err != nil {
		t.Fatalf("the composed graph does not validate: %v", err)
	}
	if len(graph.Entries) == 0 {
		t.Fatal("the composed graph advertises no entries")
	}

	var bootstrap int
	for _, entry := range graph.Entries {
		if entry.ID == "" || entry.URL == "" || entry.Rev == "" {
			t.Errorf("entry %+v is missing an id, url, or rev", entry)
		}
		if entry.ID == dshboot.ClientModulesID {
			bootstrap++
		}
	}
	if bootstrap != 1 {
		t.Errorf("graph advertises %d bootstrap module entries, want exactly 1", bootstrap)
	}

	// The bootstrap package is scheduled parser-blocking and every other row is
	// an application preload, matching upstream's PARSER_PRELOAD_IDS.
	var sawBootstrapBatch bool
	for _, batch := range graph.Batches {
		if batch.Phase == dshboot.PhaseBootstrap {
			sawBootstrapBatch = true
			if len(batch.Entries) != 1 || batch.Entries[0] != dshboot.ClientModulesID {
				t.Errorf("bootstrap batch entries = %v, want just %s", batch.Entries, dshboot.ClientModulesID)
			}
		}
	}
	if !sawBootstrapBatch {
		t.Error("the graph has no bootstrap batch")
	}

	// The roster records three deliberately withheld staged bundles. Each must
	// be absent from the graph and carry the evidence for its exclusion.
	blocked := mux.Blocked()
	if len(blocked) != 3 {
		t.Fatalf("blocked entries = %+v, want the three documented exclusions", blocked)
	}
	byDir := map[string]BlockedEntry{}
	for _, entry := range blocked {
		byDir[entry.Dir] = entry
	}
	cordis, ok := byDir["extensions/ui-cordis"]
	if !ok || cordis.ID != "@deepseek-ai/dsh-client-ui-cordis" {
		t.Fatalf("blocked entries = %+v, want the ui-cordis exclusion", blocked)
	}
	if !strings.Contains(cordis.Reason, "cordis-client-runner") {
		t.Errorf("the ui-cordis exclusion does not cite the missing provider: %q", cordis.Reason)
	}
	for _, dir := range []string{"client/ui-directory-picker-browse", "client/ui-directory-picker-native"} {
		entry, ok := byDir[dir]
		if !ok {
			t.Fatalf("blocked entries = %+v, want the %s exclusion", blocked, dir)
		}
		if !strings.Contains(entry.Reason, "activation") {
			t.Errorf("the %s exclusion does not cite the activation failure: %q", dir, entry.Reason)
		}
	}
	blockedIDs := map[string]struct{}{}
	for _, entry := range blocked {
		blockedIDs[entry.ID] = struct{}{}
	}
	for _, entry := range graph.Entries {
		if _, withheld := blockedIDs[entry.ID]; withheld {
			t.Errorf("%s is blocked but still advertised", entry.ID)
		}
	}

	// Every advertised row records the module requests scanned from its staged
	// bundle, and the gateway client specifier survives the roster round trip
	// unchanged so orderByModuleGraph can alias it onto the bare gateway row.
	for _, entry := range graph.Entries {
		if entry.ID != "@deepseek-ai/dsh-api-session-controller" {
			continue
		}
		if !contains(entry.External, "@deepseek-ai/dsh-api-gateway/client") {
			t.Errorf("session-controller external = %v, want the gateway client specifier", entry.External)
		}
		if len(entry.External) == 0 {
			t.Error("session-controller advertises no external module requests at all")
		}
	}
}

// An advertised URL that does not resolve is a fatal boot failure, not a
// degraded feature, so every row and batch URL is fetched through the exact
// /plugins route and checked for the JavaScript the classic script needs.
func TestEveryAdvertisedBundleURLServesJavaScript(t *testing.T) {
	mux := newTestMux(t)
	graph := mux.Graph()
	handler := mux.Plugins()

	var urls []string
	for _, entry := range graph.Entries {
		urls = append(urls, entry.URL)
	}
	for _, batch := range graph.Batches {
		urls = append(urls, batch.URL)
	}
	if len(urls) < 2 {
		t.Fatalf("graph advertises %d URLs; expected the bootstrap and application batches", len(urls))
	}
	for _, target := range urls {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, recorder.Code)
			continue
		}
		if got := recorder.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Errorf("GET %s content type = %q, want text/javascript", target, got)
		}
		if !strings.Contains(recorder.Body.String(), "__ModuleLoader__") {
			t.Errorf("GET %s served bytes that register no module loader factory", target)
		}
		if !strings.Contains(target, "rev=") {
			t.Errorf("advertised URL %s carries no rev parameter", target)
		}
	}
}

// The shell is only useful after Bundle.Inject has spliced in the boot rows:
// a raw index.html is the console's guaranteed failure page. The mounted root
// must carry all four injected pieces and the ZenForge title.
func TestMountedHandlerServesTheInjectedShellAtTheRoot(t *testing.T) {
	mux := newTestMux(t)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET / content type = %q, want HTML", got)
	}
	body := recorder.Body.String()
	for _, wanted := range []string{
		"<title>ZenForge</title>",
		"pendingQueue",                  // the queue sentinel the shell depends on
		`window.__ModuleLoader__`,       // the module-loader facade
		`globalThis["__DSH_BOOT__"]`,    // the boot graph global
		`globalThis.__DSH_BOOT_READY__`, // the readiness tail
		"/plugins/??",                   // a revisioned batch URL
	} {
		if !strings.Contains(body, wanted) {
			t.Errorf("the injected shell at / is missing %q", wanted)
		}
	}

	raw, err := dshconsole.Index()
	if err != nil {
		t.Fatalf("read the raw staged shell: %v", err)
	}
	if !strings.Contains(string(raw), "<title>ZenForge</title>") {
		t.Fatal("the staged shell lost its ZenForge title")
	}
	if strings.Contains(string(raw), "__DSH_BOOT__") {
		t.Fatal("the raw staged shell already carries the boot graph; the injection test proves nothing")
	}
	if strings.Contains(body, "HARNESS") {
		t.Error("the served shell still carries the upstream boot wordmark")
	}
}

// The mount owns the root path, so the staged assets and the /plugins route
// have to stay reachable from it, and everything it does not advertise must
// 404 rather than fall through to a path the API owns.
func TestMountedHandlerServesAssetsAndRefusesTheRest(t *testing.T) {
	mux := newTestMux(t)

	for _, target := range []string{"/manifest.webmanifest", "/favicon.svg"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, recorder.Code)
		}
	}

	// The shell names its own fingerprinted assets; every one it references
	// must resolve through the mount.
	shell := httptest.NewRecorder()
	mux.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, ref := range shellAssetRefs(shell.Body.String()) {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ref, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("the shell references %s, which the mount answers with %d", ref, recorder.Code)
		}
	}

	// An advertised bundle is reachable through the mount as well as through
	// the exact route.
	advertised := mux.Graph().Entries[0].URL
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, advertised, nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("GET %s through the mount = %d, want 200", advertised, recorder.Code)
	}

	// Anything under /plugins that the graph did not advertise is a 404.
	for _, target := range []string{"/plugins/", "/plugins/??not/a/package/client.js&rev=deadbeef"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, recorder.Code)
		}
	}

	// The unary RPC route is mounted at the full /api/<ns>/<method> path, so an
	// endpoint the harness does not implement is dshapi's 404, never a panic
	// and never the console shell. The body is a well-formed envelope, because
	// the 404 is about the endpoint, not about a malformed request.
	for _, target := range []string{"/api/unknown/method", "/api/remote.mux"} {
		envelope := `{"type":"client-request","rpcId":"mount-test","method":"` +
			strings.TrimPrefix(target, "/api/") + `","payload":{"args":{}}}`
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(envelope))
		request.RemoteAddr = "127.0.0.1:12345"
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d (%s), want the dshapi 404", target, recorder.Code, recorder.Body.String())
		}
	}
}

// A bundle that is advertised but whose module request is not on the graph
// cannot materialize: system.ts makeRequire throws "require(...) missed the
// module table" and boot-client's activation audit then rejects the whole
// console. The roster therefore has to be a complete accounting of the
// require() calls in the shipped bytes. This test re-scans the staged bundles
// independently of the generator and proves three things for every advertised
// entry: the roster's external list is exactly the bundle's non-relative
// require() set, every graph-row request names a row ordered before its
// consumer, and every remaining request is one the staged shell seeds.
func TestEveryScannedModuleRequestHasAProvider(t *testing.T) {
	mux := newTestMux(t)
	manifest, err := parseRoster(rosterJSON)
	if err != nil {
		t.Fatalf("parse the committed roster: %v", err)
	}

	shellProvided := map[string]struct{}{}
	for name := range manifest.ShellProvided {
		shellProvided[name] = struct{}{}
	}

	order := map[string]int{}
	external := map[string][]string{}
	for index, entry := range mux.Graph().Entries {
		order[entry.ID] = index
		external[entry.ID] = entry.External
	}

	blockedDir := map[string]struct{}{}
	for _, entry := range manifest.Blocked {
		blockedDir[entry.Dir] = struct{}{}
	}
	plugins := dshconsole.Plugins()
	advertised := 0
	for _, row := range manifest.Entries {
		if _, withheld := blockedDir[row.Dir]; withheld {
			continue
		}
		advertised++
		dir, err := fs.Sub(plugins, row.Dir)
		if err != nil {
			t.Fatalf("open staged plugins/%s: %v", row.Dir, err)
		}
		bundle, err := fs.ReadFile(dir, "client.js")
		if err != nil {
			t.Fatalf("read staged plugins/%s/client.js: %v", row.Dir, err)
		}

		scanned := nonRelativeRequires(scannedRequires(bundle))
		if !sameStrings(scanned, external[row.ID]) {
			t.Errorf(
				"roster external for %s = %v, want the bundle's scanned requests %v",
				row.ID, external[row.ID], scanned,
			)
			continue
		}
		for _, specifier := range external[row.ID] {
			bare := strings.TrimSuffix(specifier, "/client")
			if provider, ok := order[bare]; ok {
				if provider >= order[row.ID] {
					t.Errorf(
						"%s requests %q, provided by %s at graph position %d, not before its position %d",
						row.ID, specifier, bare, provider, order[row.ID],
					)
				}
				continue
			}
			if _, seeded := shellProvided[specifier]; seeded {
				continue
			}
			t.Errorf(
				"%s requests %q, which is neither a graph row nor a shell-provided module",
				row.ID, specifier,
			)
		}
	}
	if advertised != len(mux.Graph().Entries) {
		t.Errorf(
			"the roster advertises %d entries but the graph carries %d",
			advertised, len(mux.Graph().Entries),
		)
	}
}

// requireCall matches a factory's CJS module request. A plain regex over the
// file also matches the pattern inside JSDoc comments (the inlined picomatch
// documentation does exactly this) and inside a template literal that renders a
// module-system error message, so callers must keep only matches that start in
// executable code.
var requireCall = regexp.MustCompile(`require\(\s*["']([^"']+)["']\s*\)`)

// scannedRequires returns the specifier of every require("...") call whose
// match begins in code rather than in a string, template literal, or comment.
func scannedRequires(source []byte) []string {
	var found []string
	for _, match := range requireCall.FindAllSubmatchIndex(source, -1) {
		if !inCode(source, match[0]) {
			continue
		}
		found = append(found, string(source[match[2]:match[3]]))
	}
	return found
}

// inCode reports whether offset falls in executable code. It walks the source
// once per call, which is fine for a test over 55 bundles.
func inCode(source []byte, offset int) bool {
	for _, span := range codeSpans(source) {
		if span[0] <= offset && offset < span[1] {
			return true
		}
	}
	return false
}

// codeSpans splits a JavaScript source into the ranges that are executable
// code, skipping line comments, block comments, quoted strings, and template
// literals in the same way the generator's scanner does.
func codeSpans(source []byte) [][2]int {
	var spans [][2]int
	length := len(source)
	index := 0
	start := 0
	for index < length {
		switch char := source[index]; {
		case char == '/' && index+1 < length && source[index+1] == '/':
			spans = append(spans, [2]int{start, index})
			newline := bytes.IndexByte(source[index:], '\n')
			if newline < 0 {
				index = length
			} else {
				index += newline + 1
			}
			start = index
		case char == '/' && index+1 < length && source[index+1] == '*':
			spans = append(spans, [2]int{start, index})
			close := bytes.Index(source[index+2:], []byte("*/"))
			if close < 0 {
				index = length
			} else {
				index += close + 4
			}
			start = index
		case char == '"' || char == '\'' || char == '`':
			spans = append(spans, [2]int{start, index})
			index++
			for index < length {
				if source[index] == '\\' {
					index += 2
					continue
				}
				if source[index] == char {
					index++
					break
				}
				index++
			}
			start = index
		default:
			index++
		}
	}
	spans = append(spans, [2]int{start, length})
	return spans
}

// nonRelativeRequires drops package-local chunk requests, which are not
// module-table requests and so are not part of the graph's external edges.
func nonRelativeRequires(specifiers []string) []string {
	var kept []string
	seen := map[string]struct{}{}
	for _, specifier := range specifiers {
		if strings.HasPrefix(specifier, ".") {
			continue
		}
		if _, duplicate := seen[specifier]; duplicate {
			continue
		}
		seen[specifier] = struct{}{}
		kept = append(kept, specifier)
	}
	sort.Strings(kept)
	return kept
}

// sameStrings reports whether two specifier lists are the same set in the same
// order; the generator emits sorted lists so equality is order-sensitive.
func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// contains reports whether list holds value.
func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// A roster the served bytes cannot satisfy must fail at construction, naming
// what is missing. Each case here is a roster shape a rebuild or a hand edit
// could produce.
func TestLoadEntriesFailsLoudlyOnAnUnusableRoster(t *testing.T) {
	tree := fstest.MapFS{
		"good/client.js":    &fstest.MapFile{Data: []byte("window.__ModuleLoader__.load({id:'good'})")},
		"blocked/client.js": &fstest.MapFile{Data: []byte("window.__ModuleLoader__.load({id:'blocked'})")},
	}
	entry := func(dir, id string) rosterEntry { return rosterEntry{Dir: dir, ID: id} }
	roster := func(entries []rosterEntry, blocked []rosterBlocked) []byte {
		payload, err := json.Marshal(rosterManifest{
			Source:        rosterSource{Revision: "ddefc45"},
			ShellProvided: map[string]string{"react": "assets/index.js"},
			Entries:       entries,
			Blocked:       blocked,
		})
		if err != nil {
			t.Fatalf("marshal roster: %v", err)
		}
		return payload
	}
	// A valid manifest whose source records no revision.
	unversioned := func() []byte {
		payload, err := json.Marshal(rosterManifest{
			Entries: []rosterEntry{entry("good", "good")},
		})
		if err != nil {
			t.Fatalf("marshal roster: %v", err)
		}
		return payload
	}

	cases := []struct {
		name   string
		raw    []byte
		reason string
	}{
		{
			name:   "malformed json",
			raw:    []byte("{not json"),
			reason: "not valid JSON",
		},
		{
			name:   "no revision",
			raw:    unversioned(),
			reason: "no upstream revision",
		},
		{
			name:   "empty roster",
			raw:    roster(nil, nil),
			reason: "no client entries",
		},
		{
			name:   "missing staged bundle",
			raw:    roster([]rosterEntry{entry("absent", "absent")}, nil),
			reason: "client.js",
		},
		{
			name: "blocked dir is not an entry",
			raw: roster(
				[]rosterEntry{entry("good", "good")},
				[]rosterBlocked{{Dir: "other", ID: "other", Reason: "because"}},
			),
			reason: "not a roster entry",
		},
		{
			name: "blocked id mismatch",
			raw: roster(
				[]rosterEntry{entry("blocked", "blocked")},
				[]rosterBlocked{{Dir: "blocked", ID: "other", Reason: "because"}},
			),
			reason: "names id",
		},
		{
			name: "everything blocked",
			raw: roster(
				[]rosterEntry{entry("blocked", "blocked")},
				[]rosterBlocked{{Dir: "blocked", ID: "blocked", Reason: "because"}},
			),
			reason: "nothing to boot",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := loadEntries(tree, tc.raw)
			if err == nil {
				t.Fatalf("loadEntries accepted a roster it cannot serve")
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not name the cause %q", err, tc.reason)
			}
		})
	}

	// The control: the same tree with a satisfiable roster loads, and the
	// blocked entry is excluded rather than advertised.
	entries, blocked, err := loadEntries(tree, roster(
		[]rosterEntry{entry("good", "good"), entry("blocked", "blocked")},
		[]rosterBlocked{{Dir: "blocked", ID: "blocked", Reason: "only provider absent"}},
	))
	if err != nil {
		t.Fatalf("loadEntries rejected a satisfiable roster: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "good" {
		t.Errorf("entries = %+v, want just good", entries)
	}
	if entries[0].Rev == "" {
		t.Error("the loaded entry has no derived revision")
	}
	if len(blocked) != 1 || blocked[0].ID != "blocked" {
		t.Errorf("blocked = %+v, want just blocked", blocked)
	}
}

// shellAssetRefs extracts the local asset references from the served shell and
// normalizes them to the root-absolute form a browser would request from "/".
func shellAssetRefs(shell string) []string {
	var refs []string
	seen := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(shell, func(r rune) bool { return r == '"' }) {
		ref := strings.TrimPrefix(field, ".")
		if !strings.HasPrefix(ref, "/assets/") {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}
