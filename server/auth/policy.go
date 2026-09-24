// The admission policy is the outermost layer of a served host: the single place
// that decides whether a request is answered at all, whose identity it is
// answered for, and what the host recorded about it. It has to be outermost
// because it is the only layer that sees every route -- the DSH console's
// /api/*, the harness /runs/*, /api/settings, and the SSE and WebSocket streams
// all leave through the same mux, and a gate that a later route can be added
// behind is a gate that a later route will be added behind. A host whose only
// protection is "the listener is loopback, or the operator passed
// --allow-remote" (ADR 0078, ADR 0081) is a remote shell the moment that flag is
// set, and this is the layer that makes the flag safe to set.

package auth

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

// Policy is the admission decision for every request a served host answers.
//
// The zero Policy admits everything and records nothing, which is what a host
// that was not asked to enforce anything should do: it is the same shape as a
// nil AuditSink or a nil IdentitySource, so a deployment that enables no
// authentication runs this exact code path instead of branching around it.
type Policy struct {
	// Require refuses a request that presents no valid credential, except on a
	// public path. When false, every request is admitted; a credential that is
	// presented is still verified, attributed and audited.
	Require bool
	// Auth resolves the caller identity. Nil accepts no credential.
	Auth IdentitySource
	// Audit records one entry per decision. Nil records nothing.
	Audit AuditSink
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Middleware wraps a host's whole route table. It is the outermost layer: it must
// be, because it is the only one that sees every route.
//
// Every request that reaches it produces exactly one audit line, written after
// the response so the line carries the status the caller actually got rather
// than the status the host intended. Nothing about the trail may change that
// status: a nil Audit records nothing, a nil Auth resolves nothing, and a write
// that fails is reported and swallowed, because a host that started refusing
// traffic when its disk filled would turn a logging fault into an outage.
//
// A verified identity always wins over every other rule: even a request the
// policy would have admitted anyway is attributed to its caller, so the run it
// starts belongs to that caller's namespace and one tenant's grant can never
// answer for another's call.
func (p Policy) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer, recorder := policyWrap(w)
		entry := Entry{
			Time:   policyNow(p).UTC(),
			Method: r.Method,
			// Only the path, never the query string: a URL is where a credential
			// could ride, and an audit line outlives the request that carried it.
			Path:       r.URL.Path,
			RemoteAddr: r.RemoteAddr,
		}
		namespace, resolved := policyIdentity(p, r)
		switch {
		case resolved:
			entry.Decision = DecisionAllow
			entry.Tenant = namespace.Tenant
			entry.Subject = namespace.Subject
			entry.TokenID = policyTokenID(p, r)
			next.ServeHTTP(writer, r.WithContext(approval.WithNamespace(r.Context(), namespace)))
		case !p.Require:
			entry.Decision = DecisionAllowAnonymous
			next.ServeHTTP(writer, r)
		case WantsHTML(r) && ShellPath(r.URL.Path):
			// A person who navigated to the console document gets the form they
			// can act on instead of a bare 401 they can only stare at. It is
			// decided before the public-path rule so that a navigation is a
			// redirect and everything else on that path is a refusal: the shell
			// is host content, not a public document, and a client that is not a
			// person gets the 401 below.
			entry.Decision = DecisionDeny
			entry.Reason = policyReason(r)
			http.Redirect(writer, r, SignInPath, http.StatusSeeOther)
		case PublicPath(r.URL.Path):
			// The sign-in routes are reachable without a credential by design: a
			// caller with no token has to be able to reach the form that takes
			// one. An asset or an RPC reached here is still refused below.
			entry.Decision = DecisionAllowPublic
			next.ServeHTTP(writer, r)
		default:
			entry.Decision = DecisionDeny
			entry.Reason = policyReason(r)
			WriteRefusal(writer, UnauthorizedRefusal(entry.Reason))
		}
		entry.Status = recorder.statusCode()
		policyAudit(p, entry)
	})
}

// policyNow reads the clock a policy was built with. A policy assembled without
// one still needs a timestamp, and a real clock is the only sensible default.
func policyNow(p Policy) time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

// policyIdentity asks the source a policy was given who the caller is. A nil
// source is a host that accepts no credential, not an error: it resolves nobody
// and must not panic on the request path.
func policyIdentity(p Policy, r *http.Request) (approval.Namespace, bool) {
	if p.Auth == nil {
		return approval.Namespace{}, false
	}
	return p.Auth.Namespace(r)
}

// policyTokenID names the credential behind a resolved request when the source
// can, so an operator can revoke exactly the token an audit line names without
// the line ever holding the token itself. The type assertion is what keeps
// TokenIdentified optional: a source that cannot name one still audits.
func policyTokenID(p Policy, r *http.Request) string {
	identified, ok := p.Auth.(TokenIdentified)
	if !ok {
		return ""
	}
	id, ok := identified.TokenID(r)
	if !ok {
		return ""
	}
	return id
}

// policyReason is the audit reason for a refusal. A credential that was
// presented and did not resolve is a different event from one that was never
// presented -- the first says a client is misconfigured or its token was
// revoked, the second says it simply did not authenticate -- and an operator
// triaging a wall of 401s needs to tell those apart.
func policyReason(r *http.Request) string {
	if policyPresented(r) {
		return "invalid-token"
	}
	return "missing-token"
}

// policyPresented reports whether a request offered any credential at all,
// without reading or verifying it. The distinction only has to survive long
// enough to name the refusal: a non-empty session cookie counts even if it names
// no live session, because the caller did present something.
func policyPresented(r *http.Request) bool {
	if r == nil {
		return false
	}
	if _, ok := BearerToken(r); ok {
		return true
	}
	cookie, err := r.Cookie(SessionCookieName)
	return err == nil && cookie.Value != ""
}

// policyAudit writes the one line a decision produced. A failure is reported and
// then swallowed: the caller has already been answered and the answer must not
// change, but a trail that silently went nowhere is worse than a loud fault, so
// the loss is never silent. Neither the credential nor the body is ever logged,
// only the error.
func policyAudit(p Policy, entry Entry) {
	if p.Audit == nil {
		return
	}
	if err := p.Audit.Write(entry); err != nil {
		slog.Default().Error("auth: audit write failed", "error", err)
	}
}

// policyWriter records the status a handler answered with while passing every
// write through untouched. It is the wrapper the policy reads its audit status
// from, and the thing every other layer of the host sees instead of the raw
// ResponseWriter.
type policyWriter struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

// WriteHeader records the first status and forwards it. A second call is a
// no-op in net/http, so it must be a no-op here too: the audit line names the
// status the caller got, not the last one a handler tried to send.
func (w *policyWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Write forwards the body and accounts for the implicit 200 a handler gets when
// it writes a body without naming a status, which is the common case for a
// handler that just serves a document.
func (w *policyWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// Unwrap exposes the writer underneath. It is how http.ResponseController -- and
// anything else that walks the chain -- reaches the real response instead of
// stopping at this wrapper.
func (w *policyWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// statusCode is the status to audit: the one that was written, or the 200
// net/http sends when the handler wrote no status of its own.
func (w *policyWriter) statusCode() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// policyWrap builds the writer the policy hands to the host.
//
// It forwards Flusher and Hijacker only when the writer underneath implements
// them, because a wrapper that always offered them would lie about what it can
// do. net/http finds both by type assertion, so the ability has to survive as an
// ability: an SSE handler that asserted http.Flusher and got a silent no-op would
// stream nothing, and a WebSocket upgrade that asserted http.Hijacker and got a
// wrapper would turn a working console into a console whose live stream never
// connects. A writer that supports neither keeps the bare wrapper, so the type
// assertions the host's own code makes keep telling the truth in both
// directions.
func policyWrap(w http.ResponseWriter) (http.ResponseWriter, *policyWriter) {
	recorder := &policyWriter{ResponseWriter: w}
	flusher, canFlush := w.(http.Flusher)
	hijacker, canHijack := w.(http.Hijacker)
	switch {
	case canFlush && canHijack:
		return struct {
			http.Flusher
			http.Hijacker
			*policyWriter
		}{flusher, hijacker, recorder}, recorder
	case canFlush:
		return struct {
			http.Flusher
			*policyWriter
		}{flusher, recorder}, recorder
	case canHijack:
		return struct {
			http.Hijacker
			*policyWriter
		}{hijacker, recorder}, recorder
	default:
		return recorder, recorder
	}
}
