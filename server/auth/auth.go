// Package auth is a served host's caller identity: bearer tokens that carry a
// tenant, the browser session the console can use without being extended, the
// admission policy that decides whether a request is served at all, and the
// append-only audit trail that records every decision.
//
// It exists because a ZenForge service bound to anything but loopback is a
// remote shell with a stylesheet (ADR 0078), and because the harness itself
// deliberately owns no auth (ADR 0099, docs/deployment-guide.md): the harness
// exposes an access-control seam and the application supplies the policy. This
// package is the policy the shipped `zenforge serve` application installs. An
// embedder that already has its own tokens or its own user directory can ignore
// it entirely and keep using server/harnesshttp's AccessController.
//
// The identity is approval.Namespace -- the tenant and subject pair the core
// already uses to isolate persistent approval grants -- so a caller that
// authenticates here runs with that caller's namespace and a grant one tenant
// recorded never answers for another's call.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

// SessionCookieName is the cookie a browser session is carried in. The console
// is not extended to send a credential -- it is a vendored frontend -- but every
// same-origin fetch, EventSource and WebSocket carries this cookie, which is what
// makes an authenticated console possible without touching it.
const SessionCookieName = "zenforge_session"

// bearerPrefix is the only credential an API client presents. It is matched
// case-insensitively because HTTP authentication schemes are case-insensitive.
const bearerPrefix = "bearer "

// Decision values recorded in the audit trail.
const (
	// DecisionAllow is a request that presented a valid token.
	DecisionAllow = "allow"
	// DecisionAllowAnonymous is a request admitted without a token because it was
	// a loopback peer and the policy admits those (the local operator's console).
	DecisionAllowAnonymous = "allow-anonymous"
	// DecisionAllowPublic is a request admitted without a token because its path is
	// public (the sign-in routes the operator has to reach to get a token).
	DecisionAllowPublic = "allow-public"
	// DecisionDeny is a request refused for want of a valid credential.
	DecisionDeny = "deny"
)

// Refusal is the credential refusal a policy answers with, in the same shape the
// host's other refusals use. Reason is the stable machine name a client and an
// audit line both read; Message is what a person reads.
type Refusal struct {
	// Status is the HTTP status. It is 401 when the caller presented no credential
	// (or one this host cannot verify) and 403 when the caller's credential is
	// valid but the request is not.
	Status int
	// Code is the wire code, e.g. "unauthorized".
	Code string
	// Reason is the audit reason, a stable short name: "missing-token",
	// "invalid-token", "not-required".
	Reason string
	// Message is the human sentence.
	Message string
}

// UnauthorizedRefusal is the refusal a request without a usable credential gets.
// It names the credential it wanted in the standard place, so a client that
// knows HTTP can react without reading the body.
func UnauthorizedRefusal(reason string) Refusal {
	return Refusal{
		Status:  401,
		Code:    "unauthorized",
		Reason:  reason,
		Message: "this host requires a bearer token; mint one with `zenforge token create`, then present it as `Authorization: Bearer <token>` or sign in at /auth",
	}
}

// IdentitySource resolves the caller identity a request already presented. A
// *Authenticator is one; a test can be a function.
type IdentitySource interface {
	// Namespace reports the tenant and subject the request authenticated as. A
	// false second value means the request presented no usable credential, which
	// is not an error: an anonymous loopback caller is a normal case.
	Namespace(r *http.Request) (approval.Namespace, bool)
}

// TokenIdentified is an IdentitySource that can also name the credential behind
// the request, for the audit line. It is optional: a source that cannot name one
// still audits the decision.
type TokenIdentified interface {
	TokenID(r *http.Request) (string, bool)
}

// AuditSink receives one record per decision. A nil sink records nothing, which
// is what a host that was not asked to keep an audit trail does.
type AuditSink interface {
	Write(Entry) error
}

// Entry is one audit line. It is deliberately flat JSON: an operator greps it,
// and a log shipper reads it without a schema.
type Entry struct {
	Time       time.Time `json:"time"`
	Decision   string    `json:"decision"`
	Reason     string    `json:"reason,omitempty"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	Tenant     string    `json:"tenant,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	TokenID    string    `json:"tokenId,omitempty"`
	RemoteAddr string    `json:"remoteAddr"`
}

// Token is a stored credential as it is read back: never the plaintext, which
// exists only in the response to the mint that created it.
type Token struct {
	ID        string    `json:"id"`
	Tenant    string    `json:"tenant"`
	Subject   string    `json:"subject"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
	Note      string    `json:"note,omitempty"`
}

// Namespace is the identity this token authenticates as.
func (t Token) Namespace() approval.Namespace {
	return approval.Namespace{Tenant: t.Tenant, Subject: t.Subject}
}

// TokenSource is what an Authenticator needs of a token store: the lookup that
// turns a presented secret into the token it was minted as.
type TokenSource interface {
	// Lookup reports the token a presented plaintext belongs to. It is false for
	// an unknown token, an empty one, and a revoked one alike.
	Lookup(plaintext string) (Token, bool)
}

// HashToken is how a token is stored: the plaintext is never written anywhere.
// SHA-256 is not a password hash and does not need to be one -- a minted token is
// 32 bytes of crypto/rand, so there is no dictionary to search, and the token
// file is the secret.
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NewTokenID mints the identifier a token is listed and revoked by. It is not a
// secret: it names the credential in the audit trail without revealing it.
func NewTokenID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail on any supported platform. A token id is not
		// worth a panic, so a failure degrades to a fixed prefix and the store
		// still refuses a duplicate.
		return "tok_unknown"
	}
	return "tok_" + hex.EncodeToString(buf[:])
}

// NewTokenSecret mints the plaintext a caller presents. 32 bytes of crypto/rand
// is the whole security argument: it is not guessable and it is not derived from
// anything the host knows.
func NewTokenSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "zf_" + hex.EncodeToString(buf[:]), nil
}

// BearerToken returns the token a request presented in its Authorization header,
// if any. It never looks in a query string: a credential in a URL ends up in
// history, in a proxy log, and in a Referer header.
func BearerToken(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// PublicPath reports whether a path is served without a credential. The sign-in
// routes are the only ones: a caller with no token has to be able to reach the
// form that takes one, and the form itself reveals nothing but the fact that this
// host asks for a token.
func PublicPath(path string) bool {
	switch strings.TrimSuffix(path, "/") {
	case "/auth", "/auth/session":
		return true
	default:
		return false
	}
}

// SignInPath is the form an operator opens in a browser to sign in.
const SignInPath = "/auth"

// SessionPath is the route the form posts to, and the route a script uses to ask
// who it is (GET) or to end the session (DELETE).
const SessionPath = "/auth/session"

// WantsHTML reports whether a request is a browser navigation rather than an API
// call or an asset fetch. It is what lets an unauthenticated console navigation
// land on the sign-in form instead of a bare 401.
func WantsHTML(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// ShellPath reports whether a path is the console's own document, the one a
// browser navigation asks for. Only that path is redirected to the sign-in form;
// an asset or an RPC answered with a redirect would confuse a client that is not
// a person.
func ShellPath(path string) bool {
	switch path {
	case "/", "/index.html":
		return true
	default:
		return false
	}
}

// WriteRefusal answers a request with a credential refusal. The body is the same
// shape this host's other refusals use -- {"error":{"code","message"}} -- so a
// client that already parses one refusal parses this one, and the status is a
// real HTTP status rather than a 200 carrying a failure: the console's own RPC
// envelope has no code for "you are not signed in" (its vocabulary is closed),
// and a browser that is not signed in should land on the sign-in form rather than
// read a business error inside a panel.
func WriteRefusal(w http.ResponseWriter, refusal Refusal) {
	status := refusal.Status
	if status == 0 {
		status = http.StatusUnauthorized
	}
	code := refusal.Code
	if code == "" {
		code = "unauthorized"
	}
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{"code": code, "message": refusal.Message},
	})
	if err != nil {
		// A static map of strings cannot fail to marshal; refusing to answer
		// because of it would be worse than answering without a body.
		body = []byte(`{"error":{"code":"unauthorized","message":"authentication is required"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusUnauthorized {
		// The standard place to say what credential was wanted, so a client that
		// knows HTTP can react without reading the body.
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
