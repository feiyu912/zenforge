// This file is the browser half of the caller identity: the cookie a console
// that cannot be extended to send a header still presents, and the small
// sign-in surface an operator uses to put one there.

package auth

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

// DefaultSessionTTL bounds a browser session when none was configured. Twelve
// hours is a working day: long enough that an operator is not signing in again
// mid-task, short enough that a cookie taken from a shared machine is not a
// credential that outlives the reason it was issued.
const DefaultSessionTTL = 12 * time.Hour

// The sign-in form is one short field posted by a browser, so the body that may
// be read to find it is bounded before it is read at all. An unauthenticated
// endpoint that buffers whatever a caller sends is a memory hole with a route.
const authSignInBodyLimit = 1 << 16

// authFormContentType is what a browser sends when it submits the sign-in form.
// It is also how the answer is chosen: a form post lands in a browser as a
// document and gets a redirect, while everything a script sends gets a 204.
const authFormContentType = "application/x-www-form-urlencoded"

// Authenticator turns a presented credential into the caller it belongs to. A
// request presents one as `Authorization: Bearer <token>` or as the session
// cookie the sign-in form sets.
//
// The cookie's value is the bearer token itself, deliberately. The console is a
// vendored frontend that cannot be extended to send an Authorization header, but
// its RPC fetch, its EventSource and its WebSocket all send same-origin cookies;
// a second secret derived from the token would be a second thing to rotate
// without adding a boundary. Because the cookie is the token, revoking the token
// ends the browser session on its next request with nothing else to revoke.
type Authenticator struct {
	// Store is where tokens are looked up. Required. A nil Store refuses every
	// credential rather than panicking, so a host whose configuration is
	// incomplete is closed instead of open.
	Store TokenSource
	// TTL bounds a session cookie. Zero means DefaultSessionTTL.
	TTL time.Duration
	// Secure marks the cookie Secure. Set it when this host is reached over TLS.
	//
	// It is a field and not an inference because getting it wrong is not quiet:
	// a browser silently drops a Secure cookie that arrives over plain HTTP, so
	// marking one on an HTTP host does not harden anything -- it produces an
	// endless sign-in loop, with the form accepting the token and the next
	// request arriving credential-less again.
	Secure bool
}

// Credential is the secret a request presented: the bearer header first, then
// the session cookie. Never a query parameter.
//
// A credential in a URL reaches browser history, a proxy access log, and the
// Referer of the next request the page makes, so `?token=` is refused even
// though it would be the shortest thing to support. The header wins over the
// cookie because a caller that took the trouble to set one is speaking for this
// request, while the cookie is whatever the browser happens to be holding.
func (a *Authenticator) Credential(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	if token, ok := BearerToken(r); ok {
		return token, true
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return "", false
	}
	token := strings.TrimSpace(cookie.Value)
	if token == "" {
		return "", false
	}
	return token, true
}

// Namespace implements IdentitySource. A false second value is not an error: an
// anonymous caller is a normal case on a host that admits loopback traffic.
func (a *Authenticator) Namespace(r *http.Request) (approval.Namespace, bool) {
	token, ok := a.authTokenForRequest(r)
	if !ok {
		return approval.Namespace{}, false
	}
	return token.Namespace(), true
}

// TokenID names the credential behind the request for the audit line. It is the
// token's id and never the secret: the trail is read long after the request it
// describes, and a secret written there would still be live then.
func (a *Authenticator) TokenID(r *http.Request) (string, bool) {
	token, ok := a.authTokenForRequest(r)
	if !ok {
		return "", false
	}
	return token.ID, true
}

// ServeSignIn is POST /auth/session: it exchanges a credential for the session
// cookie. It accepts the credential as `Authorization: Bearer`, or as the
// `token` field of a form post, which is what the sign-in page sends.
//
// A browser form post is answered with a redirect to `/` -- a document that
// arrived as a 204 would leave the operator staring at a blank page -- and
// anything else with 204, because a script that posted a form wants the result
// and has no browser to follow a Location. A refusal never sets a cookie:
// handing out a session for a token this host just rejected is the one bug that
// would make the sign-in surface worse than not having one.
func (a *Authenticator) ServeSignIn(w http.ResponseWriter, r *http.Request) {
	secret, ok := BearerToken(r)
	if !ok {
		r.Body = http.MaxBytesReader(w, r.Body, authSignInBodyLimit)
		if err := r.ParseForm(); err != nil {
			WriteRefusal(w, Refusal{
				Status:  http.StatusBadRequest,
				Code:    "invalid_body",
				Reason:  "invalid-form",
				Message: "the sign-in form could not be read; post the token as application/x-www-form-urlencoded",
			})
			return
		}
		secret = strings.TrimSpace(r.FormValue("token"))
		ok = secret != ""
	}
	if !ok {
		WriteRefusal(w, UnauthorizedRefusal("missing-token"))
		return
	}
	if _, found := a.authToken(secret); !found {
		WriteRefusal(w, UnauthorizedRefusal("invalid-token"))
		return
	}
	// The cookie is written before the answer's status, so it travels with the
	// redirect as well as with the 204.
	a.authSetSessionCookie(w, secret)
	if authIsFormPost(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ServeCurrent is GET /auth/session: it reports who the caller is, or refuses.
// It is how a script asks who a cookie belongs to without spending an RPC that
// the console's closed error vocabulary cannot describe.
func (a *Authenticator) ServeCurrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// Written inline rather than through a helper: a sibling file in this
		// package defines refusals of its own, and "method not allowed" is a few
		// lines that are not worth a shared name either file could claim.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "method_not_allowed",
				"message": "the session status requires GET",
			},
		})
		return
	}
	token, ok := a.authTokenForRequest(r)
	if !ok {
		reason := "missing-token"
		if _, presented := a.Credential(r); presented {
			reason = "invalid-token"
		}
		WriteRefusal(w, UnauthorizedRefusal(reason))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The answer is about this caller and this credential, so nothing about it
	// may be kept: a shared cache holding it would answer the next caller with
	// the previous one's identity.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tenant":  token.Tenant,
		"subject": token.Subject,
		"tokenId": token.ID,
	})
}

// ServeSignOut clears the session cookie. It never requires a credential.
//
// The caller who needs to sign out is by definition not presenting a working
// one -- a browser whose token was revoked still holds the cookie and still
// sends it -- so demanding a credential here would leave exactly that caller
// unable to stop sending a secret this host no longer honours. Clearing an
// absent cookie costs nothing.
func (a *Authenticator) ServeSignOut(w http.ResponseWriter, r *http.Request) {
	a.authClearSessionCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// ServeSignInPage is GET /auth: the only HTML this package writes.
//
// It has to exist because the console has no login UI we can extend, and it is
// deliberately the smallest document that can do the job: an operator who
// reaches it has already been refused, so every asset it referenced would be one
// more request this host serves to someone with no credential. The page is a
// constant that never reads the request, which makes "no credential is echoed
// here" a property of the code rather than a review of a format string.
func (a *Authenticator) ServeSignInPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "method_not_allowed",
				"message": "the sign-in page requires GET",
			},
		})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, authSignInPage)
}

// authSignInPage is the whole sign-in document. It is one field, one submit and
// one sentence about where the token comes from; there is no stylesheet and no
// script, because a page that cannot be wrong is the right page to serve to a
// caller this host does not trust yet.
const authSignInPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Sign in to ZenForge</title>
</head>
<body>
<h1>Sign in</h1>
<p>Paste the token you minted with <code>zenforge token create</code> to start a session in this browser.</p>
<form method="post" action="/auth/session">
<label for="token">Token</label>
<input id="token" name="token" type="password" autocomplete="off" autofocus>
<button type="submit">Sign in</button>
</form>
</body>
</html>
`

// authToken looks a presented secret up in the store. A nil receiver or a nil
// store is a closed host, not a crash: a process that was never wired with a
// store refuses every caller instead of taking itself down on the first request.
func (a *Authenticator) authToken(secret string) (Token, bool) {
	if a == nil || a.Store == nil {
		return Token{}, false
	}
	if strings.TrimSpace(secret) == "" {
		return Token{}, false
	}
	return a.Store.Lookup(secret)
}

// authTokenForRequest resolves the credential a request presented, if any.
func (a *Authenticator) authTokenForRequest(r *http.Request) (Token, bool) {
	secret, ok := a.Credential(r)
	if !ok {
		return Token{}, false
	}
	return a.authToken(secret)
}

// authSessionTTL is the effective cookie lifetime: the configured TTL, or
// DefaultSessionTTL when none was configured. A zero or negative TTL is treated
// as unset rather than as an already-expired cookie, because a cookie that never
// survives a request is a sign-in loop, not a shorter session.
func (a *Authenticator) authSessionTTL() time.Duration {
	if a != nil && a.TTL > 0 {
		return a.TTL
	}
	return DefaultSessionTTL
}

// authIsFormPost reports whether a request is a browser's own form submission.
// Only that case is answered with a redirect: it is the one whose response lands
// in a browser as a document. A script that posted the same content type wants
// the status, not a Location it will not follow like a person's browser would.
func authIsFormPost(r *http.Request) bool {
	if r == nil {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == authFormContentType
}

// authSetSessionCookie writes the session cookie. The value is the credential
// itself, so the host keeps no session table and a revoke invalidates the
// browser just as it invalidates an API client.
//
// SameSite=Strict is the second lock on this door. A browser attaches a cookie
// to whatever request is being made, so without it another site could point a
// form or a fetch at this host and have the operator's session answer for it.
// The console's own Origin and Sec-Fetch-Site fence is the first lock, but that
// fence is the console's code; this one holds for every route the cookie reaches.
//
// Path=/ so every console route sees the cookie, and HttpOnly so a script
// injected into a panel cannot read the token out of document.cookie. Expires is
// deliberately unset: Max-Age is relative to when the browser received the
// cookie, so the deadline needs no clock to agree with the host's, while an
// absolute Expires would be wrong the moment one clock drifts.
func (a *Authenticator) authSetSessionCookie(w http.ResponseWriter, secret string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    secret,
		Path:     "/",
		HttpOnly: true,
		Secure:   a != nil && a.Secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(a.authSessionTTL() / time.Second),
	})
}

// authClearSessionCookie removes the cookie with Max-Age=0. The other attributes
// have to match the ones it was set with -- browsers match a clearing cookie by
// name, path and domain, and maybe by Secure -- or the clear is treated as a
// different cookie and the session cookie stays where it is.
func (a *Authenticator) authClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a != nil && a.Secure,
		SameSite: http.SameSiteStrictMode,
		// Go writes Max-Age=0 for a negative MaxAge; zero would omit the
		// attribute entirely and delete nothing.
		MaxAge: -1,
	})
}
