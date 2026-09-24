package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/server/auth"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

// testServeAuth builds the identity decision a served host would make, with its
// tokens and audit trail in a temporary directory so a test never touches the
// operator's own configuration.
func testServeAuth(t *testing.T, requireAuth bool) *serveAuth {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "tokens.json")
	if requireAuth {
		// resolveServeAuth refuses to start a host that must require tokens and
		// holds none, so any host under test has to have one before it resolves.
		seed, err := auth.OpenTokenStore(tokenPath)
		if err != nil {
			t.Fatalf("open the token store: %v", err)
		}
		if _, _, err := seed.Create("seed", "bootstrap", "seeded by the test"); err != nil {
			t.Fatalf("seed a token: %v", err)
		}
	}
	config, err := resolveServeAuth(serveAuthOptions{
		requireAuth: requireAuth,
		tokenFile:   tokenPath,
		auditLog:    filepath.Join(dir, "audit.jsonl"),
	})
	if err != nil {
		t.Fatalf("resolve the serve identity: %v", err)
	}
	t.Cleanup(func() {
		if err := config.close(); err != nil {
			t.Errorf("close the audit trail: %v", err)
		}
	})
	return config
}

// mintTestToken mints one token for the configured store and returns the secret.
func mintTestToken(t *testing.T, config *serveAuth, tenant, subject string) string {
	t.Helper()
	secret, _, err := config.tokens.Create(tenant, subject, "test")
	if err != nil {
		t.Fatalf("mint a token: %v", err)
	}
	return secret
}

// consoleStub stands in for the console mount: the point of these tests is the
// boundary in front of it, not what it serves.
func consoleStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "console")
	})
}

func testServeMux(t *testing.T, config *serveAuth) http.Handler {
	t.Helper()
	return newServeMux(serveMuxConfig{
		auth:      config,
		settings:  newTestSettingsStore(t, false),
		workspace: "/tmp/zenforge-serve-workspace",
		dsh:       consoleStub(),
	})
}

// TestServeRefusesRemoteWithoutTokens pins the fail-closed start: --allow-remote
// used to expose every route to anyone who could reach the port, so a host that
// is asked to expose itself now has to hold a token first.
func TestServeRefusesRemoteWithoutTokens(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveServeAuth(serveAuthOptions{
		allowRemote: true,
		tokenFile:   filepath.Join(dir, "tokens.json"),
		auditLog:    filepath.Join(dir, "audit.jsonl"),
	})
	if err == nil {
		t.Fatal("--allow-remote with no token was accepted")
	}
	for _, want := range []string{"zenforge token create", "--allow-anonymous-remote"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestServeAllowsAnonymousRemoteOnlyWhenAsked pins the escape hatch: an operator
// on a network they already trust keeps the old behaviour, but has to say so.
func TestServeAllowsAnonymousRemoteOnlyWhenAsked(t *testing.T) {
	dir := t.TempDir()
	config, err := resolveServeAuth(serveAuthOptions{
		allowRemote:          true,
		allowAnonymousRemote: true,
		tokenFile:            filepath.Join(dir, "tokens.json"),
	})
	if err != nil {
		t.Fatalf("--allow-anonymous-remote was refused: %v", err)
	}
	defer func() { _ = config.close() }()
	if config.required {
		t.Fatal("a host that was told to allow anonymous remote callers requires tokens anyway")
	}
	if config.audit != nil {
		t.Fatal("a host that requires nothing opened an audit trail it was not asked for")
	}
}

// TestServeRequiresATokenFileWhenAuthenticationIsRequired pins the other
// fail-closed start: a host that requires tokens and has nowhere to keep them
// must not silently serve nobody.
func TestServeRequiresATokenFileWhenAuthenticationIsRequired(t *testing.T) {
	// No configuration directory at all: no ZENFORGE_CONFIG_DIR, no
	// XDG_CONFIG_HOME and no home to fall back to.
	t.Setenv("ZENFORGE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	_, err := resolveServeAuth(serveAuthOptions{requireAuth: true})
	if err == nil {
		t.Fatal("--require-auth without a token file was accepted")
	}
	if !strings.Contains(err.Error(), "--auth-token-file") {
		t.Errorf("error %q does not name the flag that fixes it", err)
	}
}

// TestServeKeepsTheAuditTrailBesideTheTokens pins the default location: a
// deployment that requires authentication gets an audit trail without asking, in
// the same directory its tokens live in.
func TestServeKeepsTheAuditTrailBesideTheTokens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZENFORGE_CONFIG_DIR", dir)
	seed, err := auth.OpenTokenStore(filepath.Join(dir, authTokensFileName))
	if err != nil {
		t.Fatalf("open the default token store: %v", err)
	}
	if _, _, err := seed.Create("seed", "bootstrap", ""); err != nil {
		t.Fatalf("seed a token: %v", err)
	}
	config, err := resolveServeAuth(serveAuthOptions{requireAuth: true})
	if err != nil {
		t.Fatalf("resolve the serve identity: %v", err)
	}
	defer func() { _ = config.close() }()
	if config.audit == nil {
		t.Fatal("a host that requires authentication opened no audit trail")
	}
	if got, want := config.audit.Path(), filepath.Join(dir, authAuditFileName); got != want {
		t.Fatalf("audit trail = %q, want %q", got, want)
	}
}

// TestServeRefusesASecretFileInsideTheWorkspace pins the reason the settings
// document is already refused there: the console reads workspace files back to
// the browser, so a credentials file inside one is a browsable document.
func TestServeRefusesASecretFileInsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	_, err := resolveServeAuth(serveAuthOptions{
		requireAuth: true,
		tokenFile:   filepath.Join(workspace, "tokens.json"),
		auditLog:    filepath.Join(t.TempDir(), "audit.jsonl"),
		workspace:   workspace,
	})
	if err == nil {
		t.Fatal("a token file inside the served workspace was accepted")
	}
	if !strings.Contains(err.Error(), "--auth-token-file") {
		t.Errorf("error %q does not name the flag", err)
	}

	// The token store has to hold a token, or the host refuses to start for a
	// different reason before it ever looks at the audit path.
	tokenPath := filepath.Join(t.TempDir(), "tokens.json")
	store, err := auth.OpenTokenStore(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create("acme", "ci", ""); err != nil {
		t.Fatal(err)
	}
	_, err = resolveServeAuth(serveAuthOptions{
		requireAuth: true,
		tokenFile:   tokenPath,
		auditLog:    filepath.Join(workspace, "audit.jsonl"),
		workspace:   workspace,
	})
	if err == nil {
		t.Fatal("an audit trail inside the served workspace was accepted")
	}
	if !strings.Contains(err.Error(), "--audit-log") {
		t.Errorf("error %q does not name the flag", err)
	}
}

// TestServeBoundaryServesOnlyAnAuthenticatedCaller is the whole point of the
// chain: one middleware in front of every route, a token that names a tenant, a
// cookie the browser console can use without being extended, and a decision
// recorded for each request.
func TestServeBoundaryServesOnlyAnAuthenticatedCaller(t *testing.T) {
	config := testServeAuth(t, true)
	secret := mintTestToken(t, config, "acme", "ci")
	handler := testServeMux(t, config)

	// An anonymous API caller is refused, and told what was wanted.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/server", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/server = %d, want 401", recorder.Code)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
	var refusal struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("refusal is not JSON: %v (%s)", err, recorder.Body.String())
	}
	if refusal.Error.Code != "unauthorized" {
		t.Errorf("refusal code = %q, want unauthorized", refusal.Error.Code)
	}

	// The same caller with the bearer token is served.
	authed := httptest.NewRequest(http.MethodGet, "/api/server", nil)
	authed.Header.Set("Authorization", "Bearer "+secret)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authed)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated GET /api/server = %d, want 200 (%s)", recorder.Code, recorder.Body.String())
	}

	// The sign-in form is reachable without a credential, which is the only way
	// a browser can get one.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, auth.SignInPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", auth.SignInPath, recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `action="`+auth.SessionPath+`"`) {
		t.Errorf("the sign-in page does not post to %s", auth.SessionPath)
	}

	// Signing in with the form sets the session cookie and lands on the console.
	form := url.Values{"token": {secret}}
	signIn := httptest.NewRequest(http.MethodPost, auth.SessionPath, strings.NewReader(form.Encode()))
	signIn.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, signIn)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("form sign-in = %d, want 303 (%s)", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
		t.Fatalf("sign-in set cookies %v, want one %s", cookies, auth.SessionCookieName)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie is not HttpOnly+Strict: %+v", cookies[0])
	}

	// The console's own transport sends that cookie, so the boundary now serves
	// the console exactly as it served the bearer caller.
	withCookie := httptest.NewRequest(http.MethodGet, "/api/server", nil)
	withCookie.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: secret})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, withCookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated GET /api/server = %d, want 200", recorder.Code)
	}

	// A browser navigation with no session lands on the form rather than on a
	// bare refusal a person cannot act on.
	navigation := httptest.NewRequest(http.MethodGet, "/", nil)
	navigation.Header.Set("Accept", "text/html,application/xhtml+xml")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, navigation)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != auth.SignInPath {
		t.Fatalf("anonymous navigation = %d to %q, want 303 to %s", recorder.Code, recorder.Header().Get("Location"), auth.SignInPath)
	}

	// An asset is not a navigation: it is refused, not redirected.
	recorder = httptest.NewRecorder()
	asset := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	asset.Header.Set("Accept", "text/html")
	handler.ServeHTTP(recorder, asset)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous asset = %d, want 401", recorder.Code)
	}

	// The provenance read a client uses to check its token reports the tenant.
	current := httptest.NewRequest(http.MethodGet, auth.SessionPath, nil)
	current.Header.Set("Authorization", "Bearer "+secret)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, current)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", auth.SessionPath, recorder.Code)
	}
	var who struct {
		Tenant  string `json:"tenant"`
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &who); err != nil {
		t.Fatalf("identity is not JSON: %v (%s)", err, recorder.Body.String())
	}
	if who.Tenant != "acme" || who.Subject != "ci" {
		t.Fatalf("identity = %+v, want acme/ci", who)
	}

	// Every one of those decisions is in the trail, attributed.
	lines := readAuditLines(t, config.audit.Path())
	if len(lines) != 8 {
		t.Fatalf("audit trail has %d lines, want 8:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var allowed, denied, public int
	for _, line := range lines {
		var entry auth.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("audit line is not JSON: %v (%s)", err, line)
		}
		switch entry.Decision {
		case auth.DecisionAllow:
			allowed++
			if entry.Tenant != "acme" || entry.Subject != "ci" {
				t.Errorf("allowed entry is not attributed: %+v", entry)
			}
		case auth.DecisionDeny:
			denied++
		case auth.DecisionAllowPublic:
			// The sign-in form is the one thing a caller without a token may
			// reach; it is not an authenticated decision and must not be recorded
			// as one.
			public++
			if entry.Tenant != "" || entry.Subject != "" {
				t.Errorf("a public entry carries an identity: %+v", entry)
			}
		default:
			t.Errorf("unexpected decision %q in %+v", entry.Decision, entry)
		}
		if strings.Contains(line, secret) {
			t.Fatalf("the audit trail contains the token: %s", line)
		}
	}
	if allowed != 3 || denied != 3 || public != 2 {
		t.Fatalf("audited %d allows, %d denies and %d public decisions, want 3, 3 and 2", allowed, denied, public)
	}

}

// TestServeBoundaryAdmitsEveryoneWhenNothingIsRequired pins the unchanged local
// default: the console on loopback keeps working, and the trail still says who
// was admitted and that it was anonymous.
func TestServeBoundaryAdmitsEveryoneWhenNothingIsRequired(t *testing.T) {
	config := testServeAuth(t, false)
	handler := testServeMux(t, config)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/server", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/server = %d, want 200 on a host that requires nothing", recorder.Code)
	}

	lines := readAuditLines(t, config.audit.Path())
	if len(lines) != 1 {
		t.Fatalf("audit trail has %d lines, want 1", len(lines))
	}
	var entry auth.Entry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("audit line is not JSON: %v", err)
	}
	if entry.Decision != auth.DecisionAllowAnonymous {
		t.Fatalf("decision = %q, want %q", entry.Decision, auth.DecisionAllowAnonymous)
	}
	if entry.Status != http.StatusOK {
		t.Fatalf("audited status = %d, want 200", entry.Status)
	}
}

// TestTokenCommandMintsListsAndRevokes pins the operator's side: the plaintext is
// printed once, the file keeps only its hash, and a revoked token stops working.
func TestTokenCommandMintsListsAndRevokes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	var out, errOut bytes.Buffer
	streams := IO{Stdout: &out, Stderr: &errOut}

	if err := tokenCommand(t.Context(), []string{"create", "--tenant", "acme", "--subject", "ci", "--note", "canary", "--token-file", path}, streams); err != nil {
		t.Fatalf("token create: %v", err)
	}
	secret := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "token:") {
			secret = strings.TrimSpace(strings.TrimPrefix(line, "token:"))
		}
	}
	if !strings.HasPrefix(secret, "zf_") {
		t.Fatalf("create printed no token:\n%s", out.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("the token file contains the plaintext:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(auth.HashToken(secret))) {
		t.Fatalf("the token file does not contain the token's hash:\n%s", raw)
	}

	out.Reset()
	if err := tokenCommand(t.Context(), []string{"list", "--token-file", path}, streams); err != nil {
		t.Fatalf("token list: %v", err)
	}
	if !strings.Contains(out.String(), "acme") || !strings.Contains(out.String(), "ci") {
		t.Fatalf("list does not name the token:\n%s", out.String())
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("list printed the secret:\n%s", out.String())
	}

	store, err := auth.OpenTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tokens := store.List()
	if len(tokens) != 1 {
		t.Fatalf("store holds %d tokens, want 1", len(tokens))
	}
	tokenID := tokens[0].ID

	out.Reset()
	if err := tokenCommand(t.Context(), []string{"revoke", "--id", tokenID, "--token-file", path}, streams); err != nil {
		t.Fatalf("token revoke: %v", err)
	}
	// The revoke was written by another store instance, which is exactly the case
	// Reload exists for: a long-running host has to see a revoke it did not make.
	if err := store.Reload(); err != nil {
		t.Fatalf("reload after a revoke: %v", err)
	}
	if _, ok := store.Lookup(secret); ok {
		t.Fatal("a revoked token still authenticates")
	}
	if err := tokenCommand(t.Context(), []string{"revoke", "--id", tokenID, "--token-file", path}, streams); err == nil {
		t.Fatal("revoking a token twice was accepted")
	}
}

// TestTokenCommandRefusesIncompleteArguments pins the subcommand's own guards.
func TestTokenCommandRefusesIncompleteArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	streams := IO{Stdout: &out, Stderr: &errOut}
	path := filepath.Join(t.TempDir(), "tokens.json")
	for _, args := range [][]string{
		{},
		{"mint"},
		{"create", "--token-file", path},
		{"create", "--tenant", "acme", "--token-file", path},
		{"revoke", "--token-file", path},
	} {
		if err := tokenCommand(context.Background(), args, streams); err == nil {
			t.Errorf("tokenCommand(%v) was accepted", args)
		}
	}
}

// TestTokenCommandCreatesAnEmptyStoreForTheFirstMint pins that an operator
// minting the first token does not have to create the file by hand.
func TestTokenCommandCreatesAnEmptyStoreForTheFirstMint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "tokens.json")
	var out, errOut bytes.Buffer
	streams := IO{Stdout: &out, Stderr: &errOut}
	if err := tokenCommand(context.Background(), []string{"json", "--token-file", path}, streams); err == nil {
		t.Fatal("an unknown token subcommand was accepted")
	}
	if err := tokenCommand(context.Background(), []string{"create", "--tenant", "acme", "--subject", "ci", "--json", "--token-file", path}, streams); err != nil {
		t.Fatalf("token create --json: %v", err)
	}
	var minted struct {
		Token  string `json:"token"`
		Tenant string `json:"tenant"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &minted); err != nil {
		t.Fatalf("--json printed no JSON object: %v (%s)", err, out.String())
	}
	if minted.Tenant != "acme" || !strings.HasPrefix(minted.Token, "zf_") {
		t.Fatalf("minted = %+v", minted)
	}
}

// readAuditLines reads the audit trail as the log shipper would.
func readAuditLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the audit trail: %v", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestServeSeesATokenMintedOrRevokedInAnotherProcess pins the reload interval: an
// operator's `zenforge token revoke` is worthless if a running host keeps
// honouring the token until it is restarted, and a mint has to arrive the same
// way -- the token file is written by a different process.
func TestServeSeesATokenMintedOrRevokedInAnotherProcess(t *testing.T) {
	config := testServeAuth(t, true)
	handler := testServeMux(t, config)

	// Another process over the same file: a separate store instance, exactly as
	// `zenforge token` opens it.
	other, err := auth.OpenTokenStore(config.tokens.Path())
	if err != nil {
		t.Fatalf("open the same token file: %v", err)
	}
	secret, minted, err := other.Create("acme", "ci", "minted while the host runs")
	if err != nil {
		t.Fatalf("mint in the other process: %v", err)
	}

	serve := func() int {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/server", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if got := serve(); got != http.StatusUnauthorized {
		t.Fatalf("a token minted after start was accepted before the reload: %d", got)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go config.watchTokens(ctx, 5*time.Millisecond)

	waitForStatus := func(want int, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if got := serve(); got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: status never became %d", what, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitForStatus(http.StatusOK, "the minted token")

	if err := other.Revoke(minted.ID); err != nil {
		t.Fatalf("revoke in the other process: %v", err)
	}
	waitForStatus(http.StatusUnauthorized, "the revoked token")
}

// TestServeBoundaryCarriesTheCallersTenantIntoTheRoute pins the seam between the
// boundary and the run: a handler behind the boundary must see the caller's
// namespace in the request context, and the controller the harness routes ask is
// the same identity. That pair is what makes a run's persistent approval grants
// the caller's rather than the host's, so it is pinned at the assembly the host
// actually uses rather than in either package alone.
func TestServeBoundaryCarriesTheCallersTenantIntoTheRoute(t *testing.T) {
	config := testServeAuth(t, true)
	secret := mintTestToken(t, config, "acme", "ci")

	type observed struct {
		namespace approval.Namespace
		found     bool
	}
	var saw observed
	handler := newServeMux(serveMuxConfig{
		auth:      config,
		settings:  newTestSettingsStore(t, false),
		dsh:       consoleStub(),
		workspace: "/tmp/zenforge-serve-workspace",
		registerHarness: func(mux *http.ServeMux) {
			mux.HandleFunc("/runs/start", func(w http.ResponseWriter, r *http.Request) {
				saw.namespace, saw.found = approval.NamespaceFrom(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/runs/start", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("authenticated POST /runs/start = %d, want 204 (%s)", recorder.Code, recorder.Body.String())
	}
	want := approval.Namespace{Tenant: "acme", Subject: "ci"}
	if !saw.found || saw.namespace != want {
		t.Fatalf("the route saw namespace %+v (present=%v), want %+v", saw.namespace, saw.found, want)
	}

	// The harness routes ask a second question -- server/harnesshttp's
	// AccessController -- and it has to answer with the same identity rather than
	// an empty one, or the run would be attributed to the host.
	controller := config.accessController()
	if controller == nil {
		t.Fatal("a configured host installed no access controller")
	}
	decision, err := controller.Authorize(approval.WithNamespace(t.Context(), want), req, harnesshttp.Operation{Name: "run"})
	if err != nil {
		t.Fatalf("the access controller refused a request the boundary admitted: %v", err)
	}
	if decision.ApprovalNamespace != want {
		t.Fatalf("the access decision carries %+v, want %+v", decision.ApprovalNamespace, want)
	}
	anonymous, err := controller.Authorize(t.Context(), req, harnesshttp.Operation{Name: "run"})
	if err != nil {
		t.Fatalf("the access controller refused a request with no identity: %v", err)
	}
	if anonymous.ApprovalNamespace != (approval.Namespace{}) {
		t.Fatalf("a request with no identity carried %+v", anonymous.ApprovalNamespace)
	}
}
