package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

// authFakeTokens is an in-memory TokenSource. The real store is a file with a
// mode check and a revoke that rewrites it, and neither is what this file is
// about: a fake can hold a token, hand it back, and then forget it, which is
// exactly the difference between a live credential and a revoked one.
type authFakeTokens struct {
	tokens map[string]Token
}

func newAuthFakeTokens() *authFakeTokens {
	return &authFakeTokens{tokens: make(map[string]Token)}
}

// put records one live token.
func (f *authFakeTokens) put(secret, tenant, subject, id string) {
	f.tokens[secret] = Token{
		ID:        id,
		Tenant:    tenant,
		Subject:   subject,
		Hash:      HashToken(secret),
		CreatedAt: time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC),
	}
}

// revoke models a revoked token: there is no row left for Lookup to match.
func (f *authFakeTokens) revoke(secret string) {
	delete(f.tokens, secret)
}

// Lookup mirrors the store's contract, including its refusal of a blank secret.
func (f *authFakeTokens) Lookup(secret string) (Token, bool) {
	if strings.TrimSpace(secret) == "" {
		return Token{}, false
	}
	token, ok := f.tokens[secret]
	return token, ok
}

// authTestTTL is the configured bound used by tests that do not set their own.
// It is deliberately not DefaultSessionTTL, so a test asserting a Max-Age says
// which TTL it expected instead of accepting either answer.
const authTestTTL = 90 * time.Minute

// newAuthTestAuthenticator builds an authenticator over one token source with a
// TTL and clock that are easy to read back out of the cookie.
func newAuthTestAuthenticator(store TokenSource) *Authenticator {
	return &Authenticator{
		Store: store,
		TTL:   authTestTTL,
	}
}

// authTestCookie parses the one Set-Cookie a handler wrote. It fails the test on
// none or on more than one, because "a refusal sets no cookie" and "sign-in sets
// exactly one" are both part of the contract.
func authTestCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	raw := rec.Header().Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("Set-Cookie count = %d (%q), want exactly 1", len(raw), raw)
	}
	header := http.Header{}
	header.Add("Set-Cookie", raw[0])
	cookies := (&http.Response{Header: header}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("parse %q: got %d cookies, want 1", raw[0], len(cookies))
	}
	return cookies[0]
}

// authTestSetCookie is the raw Set-Cookie header, for the attributes a parsed
// cookie cannot show -- that Expires is absent, and that Secure is absent.
func authTestSetCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	raw := rec.Header().Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("Set-Cookie count = %d (%q), want exactly 1", len(raw), raw)
	}
	return raw[0]
}

// authTestRequest builds a request that presents a bearer credential.
func authTestRequest(method, target, secret string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	return req
}

func TestAuthenticatorCredentialPrefersBearerOverCookie(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_header", "acme", "ci", "tok_header")
	store.put("zf_cookie", "acme", "browser", "tok_cookie")
	auth := newAuthTestAuthenticator(store)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer zf_header")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "zf_cookie"})

	secret, ok := auth.Credential(req)
	if !ok || secret != "zf_header" {
		t.Fatalf("Credential = %q, %v; want the header token zf_header, true", secret, ok)
	}
	namespace, ok := auth.Namespace(req)
	if !ok || namespace != (approval.Namespace{Tenant: "acme", Subject: "ci"}) {
		t.Fatalf("Namespace = %+v, %v; want the header token's namespace", namespace, ok)
	}
	id, ok := auth.TokenID(req)
	if !ok || id != "tok_header" {
		t.Fatalf("TokenID = %q, %v; want tok_header, true", id, ok)
	}
}

func TestAuthenticatorCredentialFallsBackToCookie(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_cookie", "acme", "browser", "tok_cookie")
	auth := newAuthTestAuthenticator(store)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "  zf_cookie  "})

	secret, ok := auth.Credential(req)
	if !ok || secret != "zf_cookie" {
		t.Fatalf("Credential = %q, %v; want the trimmed cookie token, true", secret, ok)
	}
	namespace, ok := auth.Namespace(req)
	if !ok || namespace != (approval.Namespace{Tenant: "acme", Subject: "browser"}) {
		t.Fatalf("Namespace = %+v, %v; want the cookie token's namespace", namespace, ok)
	}
}

func TestAuthenticatorCredentialRejectsAnythingButHeaderOrCookie(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")

	cases := []struct {
		name   string
		build  func() *http.Request
		reason string
	}{
		{
			name: "no credential at all",
			build: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/", nil)
			},
			reason: "a request with nothing on it",
		},
		{
			name: "empty Authorization header",
			build: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "")
				return req
			},
			reason: "an empty header is not a credential",
		},
		{
			name: "non-bearer scheme",
			build: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "Basic zf_live")
				return req
			},
			reason: "only Bearer is a credential here",
		},
		{
			name: "bearer with an empty token",
			build: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "Bearer   ")
				return req
			},
			reason: "a scheme with no secret",
		},
		{
			name: "token in the query string",
			build: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/?token=zf_live", nil)
			},
			reason: "a credential in a URL reaches history and logs",
		},
		{
			name: "empty session cookie",
			build: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: ""})
				return req
			},
			reason: "an empty cookie value",
		},
		{
			name: "whitespace session cookie",
			build: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "   "})
				return req
			},
			reason: "a cookie holding only blanks",
		},
		{
			name: "unrelated cookie",
			build: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(&http.Cookie{Name: "other", Value: "zf_live"})
				return req
			},
			reason: "only the session cookie is read",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := newAuthTestAuthenticator(store)
			req := tc.build()
			if secret, ok := auth.Credential(req); ok {
				t.Fatalf("Credential = %q, true; want no credential (%s)", secret, tc.reason)
			}
			if namespace, ok := auth.Namespace(req); ok {
				t.Fatalf("Namespace = %+v, true; want no identity (%s)", namespace, tc.reason)
			}
			if id, ok := auth.TokenID(req); ok {
				t.Fatalf("TokenID = %q, true; want no identity (%s)", id, tc.reason)
			}
		})
	}
}

func TestAuthenticatorNamespaceRefusesUnknownEmptyAndRevoked(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_gone", "acme", "ci", "tok_gone")

	auth := newAuthTestAuthenticator(store)
	if namespace, ok := auth.Namespace(authTestRequest(http.MethodGet, "/", "zf_never_minted")); ok {
		t.Fatalf("Namespace = %+v, true; want false for an unknown secret", namespace)
	}
	if id, ok := auth.TokenID(authTestRequest(http.MethodGet, "/", "zf_never_minted")); ok {
		t.Fatalf("TokenID = %q, true; want false for an unknown secret", id)
	}
	if _, ok := auth.Namespace(authTestRequest(http.MethodGet, "/", "")); ok {
		t.Fatal("an empty secret resolved an identity")
	}

	// A revoked token is the case that matters most: it must stop working
	// immediately, because the browser session is the same secret.
	if _, ok := auth.Namespace(authTestRequest(http.MethodGet, "/", "zf_gone")); !ok {
		t.Fatal("a live token did not resolve before the revoke")
	}
	store.revoke("zf_gone")
	if namespace, ok := auth.Namespace(authTestRequest(http.MethodGet, "/", "zf_gone")); ok {
		t.Fatalf("Namespace = %+v, true; want false after the revoke", namespace)
	}
	if id, ok := auth.TokenID(authTestRequest(http.MethodGet, "/", "zf_gone")); ok {
		t.Fatalf("TokenID = %q, true; want false after the revoke", id)
	}
}

func TestAuthenticatorNilStoreReportsNoIdentity(t *testing.T) {
	req := authTestRequest(http.MethodGet, "/", "zf_anything")
	presenting := &Authenticator{}
	if namespace, ok := presenting.Namespace(req); ok {
		t.Fatalf("Namespace = %+v, true; want false with a nil Store", namespace)
	}
	if id, ok := presenting.TokenID(req); ok {
		t.Fatalf("TokenID = %q, true; want false with a nil Store", id)
	}
	if secret, ok := presenting.Credential(req); !ok || secret != "zf_anything" {
		t.Fatalf("Credential = %q, %v; reading a header needs no store", secret, ok)
	}

	// A nil receiver is a host nobody finished wiring; it must be closed, not a
	// panic in the middle of a request.
	var unwired *Authenticator
	if _, ok := unwired.Namespace(req); ok {
		t.Fatal("a nil Authenticator reported an identity")
	}
	if _, ok := unwired.TokenID(req); ok {
		t.Fatal("a nil Authenticator named a token")
	}
}

func TestAuthenticatorSignInWithHeaderSetsCookie(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	rec := httptest.NewRecorder()
	auth.ServeSignIn(rec, authTestRequest(http.MethodPost, SessionPath, "zf_live"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want none for a header sign-in", got)
	}

	cookie := authTestCookie(t, rec)
	if cookie.Name != SessionCookieName {
		t.Fatalf("cookie name = %q, want %q", cookie.Name, SessionCookieName)
	}
	if cookie.Value != "zf_live" {
		t.Fatalf("cookie value = %q, want the credential verbatim", cookie.Value)
	}
	if cookie.Path != "/" {
		t.Fatalf("cookie path = %q, want /", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Fatal("cookie is not HttpOnly; a script could read the token")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.MaxAge != int(authTestTTL/time.Second) {
		t.Fatalf("cookie Max-Age = %d, want %d", cookie.MaxAge, int(authTestTTL/time.Second))
	}
	if cookie.Secure {
		t.Fatal("cookie is Secure although the authenticator does not say so")
	}
	raw := authTestSetCookie(t, rec)
	if strings.Contains(raw, "Expires=") {
		t.Fatalf("Set-Cookie = %q, want no Expires; Max-Age is the bound", raw)
	}
	if !strings.Contains(raw, "Max-Age=5400") {
		t.Fatalf("Set-Cookie = %q, want Max-Age=5400", raw)
	}
	if strings.Contains(raw, "Secure") {
		t.Fatalf("Set-Cookie = %q, want no Secure on a plain-HTTP host", raw)
	}
}

func TestAuthenticatorSignInWithFormRedirects(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "browser", "tok_live")
	auth := newAuthTestAuthenticator(store)

	form := url.Values{"token": {"zf_live"}}
	req := httptest.NewRequest(http.MethodPost, SessionPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	auth.ServeSignIn(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location = %q, want /", got)
	}
	cookie := authTestCookie(t, rec)
	if cookie.Name != SessionCookieName || cookie.Value != "zf_live" {
		t.Fatalf("cookie = %q=%q, want %s=zf_live", cookie.Name, cookie.Value,
			SessionCookieName)
	}
	// The credential is never echoed into the redirect body.
	if strings.Contains(rec.Body.String(), "zf_live") {
		t.Fatalf("redirect body = %q, want no credential in it", rec.Body.String())
	}
}

func TestAuthenticatorSignInRefusesBadTokens(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{
			name: "missing token",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, SessionPath, nil)
			},
		},
		{
			name: "unknown bearer token",
			req: func() *http.Request {
				return authTestRequest(http.MethodPost, SessionPath, "zf_unknown")
			},
		},
		{
			name: "unknown form token",
			req: func() *http.Request {
				form := url.Values{"token": {"zf_unknown"}}
				req := httptest.NewRequest(http.MethodPost, SessionPath,
					strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			},
		},
		{
			name: "blank form token",
			req: func() *http.Request {
				form := url.Values{"token": {"   "}}
				req := httptest.NewRequest(http.MethodPost, SessionPath,
					strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return req
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			auth.ServeSignIn(rec, tc.req())

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %q", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
			}
			if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
				t.Fatalf("Set-Cookie = %q, want none on a refusal", got)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("refusal body %q is not JSON: %v", rec.Body.String(), err)
			}
			if body.Error.Code != "unauthorized" {
				t.Fatalf("refusal code = %q, want unauthorized", body.Error.Code)
			}
		})
	}
}

func TestAuthenticatorSignInRefusesAnOversizedBody(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	body := "token=" + strings.Repeat("x", authSignInBodyLimit+64)
	req := httptest.NewRequest(http.MethodPost, SessionPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	auth.ServeSignIn(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %q, want none for a body this host refused", got)
	}
}

func TestAuthenticatorServeCurrentReportsAndRefuses(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	rec := httptest.NewRecorder()
	auth.ServeCurrent(rec, authTestRequest(http.MethodGet, SessionPath, "zf_live"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var current struct {
		Tenant  string `json:"tenant"`
		Subject string `json:"subject"`
		TokenID string `json:"tokenId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &current); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if current.Tenant != "acme" || current.Subject != "ci" || current.TokenID != "tok_live" {
		t.Fatalf("current = %+v, want acme/ci/tok_live", current)
	}
	if strings.Contains(rec.Body.String(), "zf_live") {
		t.Fatalf("body = %q, want no credential in it", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	auth.ServeCurrent(rec, httptest.NewRequest(http.MethodGet, SessionPath, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %q, want none on a refusal", got)
	}
}

func TestAuthenticatorServeCurrentRejectsOtherMethods(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	rec := httptest.NewRecorder()
	auth.ServeCurrent(rec, authTestRequest(http.MethodPost, SessionPath, "zf_live"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"method_not_allowed"`) {
		t.Fatalf("body = %q, want the method_not_allowed refusal shape", rec.Body.String())
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %q, want none for a rejected method", got)
	}
}

func TestAuthenticatorServeSignOutClearsTheCookie(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	cases := []struct {
		name string
		req  *http.Request
	}{
		{
			name: "with a live credential",
			req:  authTestRequest(http.MethodPost, SessionPath, "zf_live"),
		},
		{
			name: "with no credential at all",
			req:  httptest.NewRequest(http.MethodPost, SessionPath, nil),
		},
		{
			name: "with a revoked credential",
			req:  authTestRequest(http.MethodDelete, SessionPath, "zf_gone"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			auth.ServeSignOut(rec, tc.req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			cookie := authTestCookie(t, rec)
			if cookie.Name != SessionCookieName || cookie.Value != "" {
				t.Fatalf("cookie = %q=%q, want an emptied %s", cookie.Name, cookie.Value,
					SessionCookieName)
			}
			if cookie.Path != "/" || !cookie.HttpOnly ||
				cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("clearing cookie lost its attributes: %+v", cookie)
			}
			raw := authTestSetCookie(t, rec)
			if !strings.Contains(raw, "Max-Age=0") {
				t.Fatalf("Set-Cookie = %q, want Max-Age=0", raw)
			}
			if strings.Contains(raw, "Expires=") {
				t.Fatalf("Set-Cookie = %q, want no Expires", raw)
			}
		})
	}
}

func TestAuthenticatorSessionTTLZeroMeansDefault(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")

	var auth Authenticator
	auth.Store = store

	rec := httptest.NewRecorder()
	auth.ServeSignIn(rec, authTestRequest(http.MethodPost, SessionPath, "zf_live"))

	cookie := authTestCookie(t, rec)
	if want := int(DefaultSessionTTL / time.Second); cookie.MaxAge != want {
		t.Fatalf("cookie Max-Age = %d, want DefaultSessionTTL of %d seconds",
			cookie.MaxAge, want)
	}
	if !strings.Contains(authTestSetCookie(t, rec), "Max-Age=43200") {
		t.Fatalf("Set-Cookie = %q, want Max-Age=43200", authTestSetCookie(t, rec))
	}
}

func TestAuthenticatorSessionCookieSecureOnlyWhenSet(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")

	for _, secure := range []bool{false, true} {
		auth := newAuthTestAuthenticator(store)
		auth.Secure = secure

		rec := httptest.NewRecorder()
		auth.ServeSignIn(rec, authTestRequest(http.MethodPost, SessionPath, "zf_live"))
		raw := authTestSetCookie(t, rec)
		if got := strings.Contains(raw, "Secure"); got != secure {
			t.Fatalf("Secure=%v: Set-Cookie = %q, contains Secure = %v", secure, raw, got)
		}
		if cookie := authTestCookie(t, rec); cookie.Secure != secure {
			t.Fatalf("Secure=%v: parsed cookie Secure = %v", secure, cookie.Secure)
		}

		// The clearing cookie has to match, or a Secure session survives sign-out.
		rec = httptest.NewRecorder()
		auth.ServeSignOut(rec, httptest.NewRequest(http.MethodPost, SessionPath, nil))
		raw = authTestSetCookie(t, rec)
		if got := strings.Contains(raw, "Secure"); got != secure {
			t.Fatalf("Secure=%v: clearing Set-Cookie = %q, contains Secure = %v", secure, raw, got)
		}
	}
}

func TestAuthenticatorSignInPageIsMinimalAndCredentialFree(t *testing.T) {
	store := newAuthFakeTokens()
	store.put("zf_live", "acme", "ci", "tok_live")
	auth := newAuthTestAuthenticator(store)

	req := httptest.NewRequest(http.MethodGet, SignInPath, nil)
	req.Header.Set("Authorization", "Bearer zf_live")
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "zf_live"})
	rec := httptest.NewRecorder()
	auth.ServeSignInPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	page := rec.Body.String()
	for _, want := range []string{
		"<!doctype html>",
		"<title>",
		`<form method="post" action="/auth/session">`,
		`name="token"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("page does not contain %q:\n%s", want, page)
		}
	}
	if strings.Count(page, "<form") != 1 {
		t.Fatalf("page has %d forms, want 1", strings.Count(page, "<form"))
	}
	if strings.Count(page, "<input") != 1 {
		t.Fatalf("page has %d inputs, want 1", strings.Count(page, "<input"))
	}
	if strings.Contains(page, "zf_live") {
		t.Fatalf("page echoed the credential:\n%s", page)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie = %q, want none from the page", got)
	}
}

func TestAuthenticatorSignInPageRejectsOtherMethods(t *testing.T) {
	auth := newAuthTestAuthenticator(newAuthFakeTokens())

	rec := httptest.NewRecorder()
	auth.ServeSignInPage(rec, httptest.NewRequest(http.MethodPost, SignInPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"method_not_allowed"`) {
		t.Fatalf("body = %q, want the method_not_allowed refusal shape", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}
