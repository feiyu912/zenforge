package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/feiyu912/zenforge/approval"
)

// policyTestTime is a fixed clock, so an audit line's Time is asserted exactly
// instead of approximately. It sits in a non-UTC zone on purpose: the policy has
// to stamp the line in UTC, and a fixed UTC clock could not tell whether it did.
var policyTestTime = time.Date(
	2025, time.March, 4, 5, 6, 7, 0, time.FixedZone("test", 2*60*60),
)

// policyTestNamespace is the identity every admitted request in these tests
// authenticates as.
var policyTestNamespace = approval.Namespace{Tenant: "acme", Subject: "ops"}

// policyTestNow is the clock a policy under test is built with.
func policyTestNow() time.Time { return policyTestTime }

// policyTestSource is an IdentitySource a test drives by value: it says who the
// request authenticated as without needing a token store or a request shape.
type policyTestSource struct {
	namespace approval.Namespace
	ok        bool
}

// Namespace reports the identity this source was told to resolve.
func (s policyTestSource) Namespace(*http.Request) (approval.Namespace, bool) {
	return s.namespace, s.ok
}

// policyTestResolved resolves the fixed namespace.
func policyTestResolved() policyTestSource {
	return policyTestSource{namespace: policyTestNamespace, ok: true}
}

// policyTestIdentifiedSource is a source that can also name its credential,
// which is what the audit line's TokenID comes from. It is separate from
// policyTestSource so a test can pin that TokenIdentified stays optional.
type policyTestIdentifiedSource struct {
	policyTestSource
	tokenID string
}

// TokenID names the credential behind a request this source resolved.
func (s policyTestIdentifiedSource) TokenID(*http.Request) (string, bool) {
	return s.tokenID, s.tokenID != ""
}

// policyTestAudit collects the entries a policy wrote so a test can assert the
// trail as data rather than as a log line. It is used by pointer because the
// policy writes through the interface.
type policyTestAudit struct {
	mu      sync.Mutex
	entries []Entry
	// err, when set, is what every Write returns: it stands in for a full disk.
	err error
}

// Write appends one entry, or reports the configured failure.
func (a *policyTestAudit) Write(entry Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, entry)
	return nil
}

// snapshot copies the entries collected so far.
func (a *policyTestAudit) snapshot() []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Entry(nil), a.entries...)
}

// only asserts that exactly one entry was written and returns it: every request
// that reaches the middleware produces exactly one line.
func (a *policyTestAudit) only(t *testing.T) Entry {
	t.Helper()
	entries := a.snapshot()
	if len(entries) != 1 {
		t.Fatalf("audit entries: got %d want 1: %+v", len(entries), entries)
	}
	return entries[0]
}

// policyTestRequired builds the policy a host that refuses anonymous callers
// runs: the one every refusal and redirect test needs.
func policyTestRequired(auth IdentitySource, audit *policyTestAudit) Policy {
	return Policy{Require: true, Auth: auth, Audit: audit, Now: policyTestNow}
}

// policyTestAdmitting builds the policy a host that enforces nothing runs; a
// credential offered to it is still verified and attributed.
func policyTestAdmitting(auth IdentitySource, audit *policyTestAudit) Policy {
	return Policy{Auth: auth, Audit: audit, Now: policyTestNow}
}

// policyTestServe runs a handler through the middleware and returns the
// recorded response.
func policyTestServe(
	t *testing.T, policy Policy, next http.Handler, req *http.Request,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	policy.Middleware(next).ServeHTTP(recorder, req)
	return recorder
}

// policyTestOK is the smallest admitted handler: it writes no status of its own,
// which is also how a handler that just serves a document behaves.
func policyTestOK() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

// policyTestStatus is a handler that answers with exactly one status.
func policyTestStatus(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	})
}

// policyTestNever fails if a refused request ever reaches the host.
func policyTestNever(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a refused request must never reach the host's handler")
	})
}

// policyTestRefusalBody is the shape every refusal in this host uses.
type policyTestRefusalBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// policyTestDecodeRefusal reads a refusal body and fails the test if it does not
// parse, because a client that cannot parse a refusal cannot act on it.
func policyTestDecodeRefusal(
	t *testing.T, recorder *httptest.ResponseRecorder,
) policyTestRefusalBody {
	t.Helper()
	var body policyTestRefusalBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal body %q: %v", recorder.Body.String(), err)
	}
	return body
}

// policyTestPlainWriter implements http.ResponseWriter and nothing else, so a
// test can pin that the wrapper does not invent abilities the writer lacks.
type policyTestPlainWriter struct {
	header http.Header
	code   int
}

func (w *policyTestPlainWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *policyTestPlainWriter) Write(body []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return len(body), nil
}

func (w *policyTestPlainWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}

// policyTestStreamWriter is a writer that really does support Flush and Hijack,
// which httptest.ResponseRecorder does not: the wrapper's streaming contract can
// only be pinned against an underlying writer that can stream.
type policyTestStreamWriter struct {
	header     http.Header
	code       int
	flushes    int
	hijacks    int
	conn       net.Conn
	readWriter *bufio.ReadWriter
}

func (w *policyTestStreamWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *policyTestStreamWriter) Write(body []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return len(body), nil
}

func (w *policyTestStreamWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}

func (w *policyTestStreamWriter) Flush() { w.flushes++ }

func (w *policyTestStreamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacks++
	return w.conn, w.readWriter, nil
}

// policyTestLogHandler captures slog records so a test can pin that an audit
// failure is reported -- and reported without the credential.
type policyTestLogHandler struct {
	mu      sync.Mutex
	records []string
}

func (h *policyTestLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *policyTestLogHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Message)
	return nil
}

func (h *policyTestLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *policyTestLogHandler) WithGroup(string) slog.Handler { return h }

// policyTestCaptureLog redirects the default logger for one test and restores it
// afterwards, so the captured records do not leak into other tests.
func policyTestCaptureLog(t *testing.T) *policyTestLogHandler {
	t.Helper()
	handler := &policyTestLogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return handler
}

// messages copies the captured log messages.
func (h *policyTestLogHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.records...)
}

func TestPolicyAllowCarriesIdentityIntoContext(t *testing.T) {
	audit := &policyTestAudit{}
	var (
		carried   approval.Namespace
		carriedOK bool
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		carried, carriedOK = approval.NamespaceFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	policy := Policy{
		Require: true,
		Audit:   audit,
		Now:     policyTestNow,
		Auth:    policyTestIdentifiedSource{policyTestResolved(), "tok_1"},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	req.RemoteAddr = "10.0.0.7:51000"

	recorder := policyTestServe(t, policy, next, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", recorder.Code, http.StatusOK)
	}
	if !carriedOK || carried != policyTestNamespace {
		t.Fatalf("namespace in context: got %+v ok=%v", carried, carriedOK)
	}
	entry := audit.only(t)
	if entry.Decision != DecisionAllow {
		t.Fatalf("decision: got %q want %q", entry.Decision, DecisionAllow)
	}
	if entry.Tenant != "acme" || entry.Subject != "ops" {
		t.Fatalf("identity: got %q/%q want acme/ops", entry.Tenant, entry.Subject)
	}
	if entry.TokenID != "tok_1" {
		t.Fatalf("token id: got %q want %q", entry.TokenID, "tok_1")
	}
	if !entry.Time.Equal(policyTestTime) || entry.Time.Location() != time.UTC {
		t.Fatalf("time: got %v want %v in UTC", entry.Time, policyTestTime)
	}
	if entry.Method != http.MethodGet || entry.Path != "/api/runs" {
		t.Fatalf("request: got %s %s want GET /api/runs", entry.Method, entry.Path)
	}
	if entry.Status != http.StatusOK {
		t.Fatalf("status in audit: got %d want %d", entry.Status, http.StatusOK)
	}
	if entry.RemoteAddr != "10.0.0.7:51000" {
		t.Fatalf("remote addr: got %q", entry.RemoteAddr)
	}
	if entry.Reason != "" {
		t.Fatalf("reason on an allow: got %q want empty", entry.Reason)
	}
}

func TestPolicyAllowAnonymousWhenNotRequired(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		audit := &policyTestAudit{}
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := approval.NamespaceFrom(r.Context()); ok {
				t.Error("an anonymous request must not carry a namespace")
			}
			w.WriteHeader(http.StatusOK)
		})
		policy := policyTestAdmitting(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)

		recorder := policyTestServe(t, policy, next, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status: got %d want %d", recorder.Code, http.StatusOK)
		}
		entry := audit.only(t)
		if entry.Decision != DecisionAllowAnonymous {
			t.Fatalf("decision: got %q want %q", entry.Decision, DecisionAllowAnonymous)
		}
		if entry.Tenant != "" || entry.Subject != "" || entry.TokenID != "" {
			t.Fatalf("anonymous entry carries identity: %+v", entry)
		}
	})

	t.Run("invalid credential still admitted", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestAdmitting(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		req.Header.Set("Authorization", "Bearer revoked")

		recorder := policyTestServe(t, policy, policyTestOK(), req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status: got %d want %d", recorder.Code, http.StatusOK)
		}
		entry := audit.only(t)
		if entry.Decision != DecisionAllowAnonymous {
			t.Fatalf("decision: got %q want %q", entry.Decision, DecisionAllowAnonymous)
		}
	})
}

func TestPolicyAllowPublicOnSignInPathsWhenRequired(t *testing.T) {
	for _, path := range []string{"/auth", "/auth/", "/auth/session"} {
		t.Run(path, func(t *testing.T) {
			audit := &policyTestAudit{}
			policy := policyTestRequired(policyTestSource{}, audit)
			req := httptest.NewRequest(http.MethodGet, path, nil)

			recorder := policyTestServe(t, policy, policyTestOK(), req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status: got %d want %d", recorder.Code, http.StatusOK)
			}
			entry := audit.only(t)
			if entry.Decision != DecisionAllowPublic {
				t.Fatalf("decision: got %q want %q", entry.Decision, DecisionAllowPublic)
			}
			if entry.Reason != "" {
				t.Fatalf("reason on a public allow: got %q want empty", entry.Reason)
			}
		})
	}
}

func TestPolicyDenyMissingToken(t *testing.T) {
	audit := &policyTestAudit{}
	policy := policyTestRequired(policyTestSource{}, audit)
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.RemoteAddr = "10.0.0.9:51000"

	recorder := policyTestServe(t, policy, policyTestNever(t), req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate: got %q want %q", got, "Bearer")
	}
	body := policyTestDecodeRefusal(t, recorder)
	if body.Error.Code != "unauthorized" {
		t.Fatalf("refusal code: got %q want %q", body.Error.Code, "unauthorized")
	}
	entry := audit.only(t)
	if entry.Decision != DecisionDeny {
		t.Fatalf("decision: got %q want %q", entry.Decision, DecisionDeny)
	}
	if entry.Reason != "missing-token" {
		t.Fatalf("reason: got %q want %q", entry.Reason, "missing-token")
	}
	if entry.Status != http.StatusUnauthorized {
		t.Fatalf("status in audit: got %d", entry.Status)
	}
	if entry.Tenant != "" || entry.Subject != "" || entry.TokenID != "" {
		t.Fatalf("denied entry carries identity: %+v", entry)
	}
}

func TestPolicyDenyInvalidToken(t *testing.T) {
	presenters := map[string]func(*http.Request){
		"bearer": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer revoked")
		},
		"cookie": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "stale"})
		},
	}
	for name, present := range presenters {
		t.Run(name, func(t *testing.T) {
			audit := &policyTestAudit{}
			policy := policyTestRequired(policyTestSource{}, audit)
			req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
			present(req)

			recorder := policyTestServe(t, policy, policyTestNever(t), req)

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status: got %d want 401", recorder.Code)
			}
			entry := audit.only(t)
			if entry.Decision != DecisionDeny || entry.Reason != "invalid-token" {
				t.Fatalf("entry: got %q/%q want deny/invalid-token", entry.Decision, entry.Reason)
			}
			if entry.Status != http.StatusUnauthorized {
				t.Fatalf("status in audit: got %d want 401", entry.Status)
			}
		})
	}
}

func TestPolicySignInRedirectForShellNavigation(t *testing.T) {
	t.Run("shell navigation redirects", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestRequired(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		recorder := policyTestServe(t, policy, policyTestNever(t), req)

		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("status: got %d want 303", recorder.Code)
		}
		if got := recorder.Header().Get("Location"); got != SignInPath {
			t.Fatalf("location: got %q want %q", got, SignInPath)
		}
		entry := audit.only(t)
		if entry.Decision != DecisionDeny {
			t.Fatalf("decision: got %q want %q", entry.Decision, DecisionDeny)
		}
		if entry.Status != http.StatusSeeOther {
			t.Fatalf("status in audit: got %d want 303", entry.Status)
		}
		if entry.Reason != "missing-token" {
			t.Fatalf("reason: got %q want missing-token", entry.Reason)
		}
	})

	t.Run("non-html client is not redirected", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestRequired(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", "application/json")

		recorder := policyTestServe(t, policy, policyTestNever(t), req)

		if got := recorder.Header().Get("Location"); got != "" {
			t.Fatalf("location: got %q want empty", got)
		}
		// The shell is not a public path: a client that is not a person gets the
		// refusal, not the console's own document.
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status: got %d want 401", recorder.Code)
		}
		entry := audit.only(t)
		if entry.Decision != DecisionDeny {
			t.Fatalf("decision: got %q want %q", entry.Decision, DecisionDeny)
		}
	})

	t.Run("asset is not redirected", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestRequired(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
		req.Header.Set("Accept", "text/html")

		recorder := policyTestServe(t, policy, policyTestNever(t), req)

		if got := recorder.Header().Get("Location"); got != "" {
			t.Fatalf("location: got %q want empty", got)
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status: got %d want 401", recorder.Code)
		}
		entry := audit.only(t)
		if entry.Decision != DecisionDeny || entry.Reason != "missing-token" {
			t.Fatalf("entry: got %q/%q want deny/missing-token", entry.Decision, entry.Reason)
		}
	})
}

func TestPolicyAuditPathOmitsQueryString(t *testing.T) {
	audit := &policyTestAudit{}
	policy := policyTestAdmitting(policyTestSource{}, audit)
	req := httptest.NewRequest(http.MethodGet, "/api/x?token=secret", nil)

	policyTestServe(t, policy, policyTestOK(), req)

	entry := audit.only(t)
	if entry.Path != "/api/x" {
		t.Fatalf("path: got %q want %q", entry.Path, "/api/x")
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode audit entry: %v", err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("audit entry leaked the query string: %s", encoded)
	}
	if strings.Contains(string(encoded), "?") {
		t.Fatalf("audit entry contains a query separator: %s", encoded)
	}
}

func TestPolicyAuditCapturesStatus(t *testing.T) {
	t.Run("explicit status", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestAdmitting(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)

		recorder := policyTestServe(t, policy, policyTestStatus(http.StatusTeapot), req)

		if recorder.Code != http.StatusTeapot {
			t.Fatalf("status: got %d want 418", recorder.Code)
		}
		if entry := audit.only(t); entry.Status != http.StatusTeapot {
			t.Fatalf("status in audit: got %d want 418", entry.Status)
		}
	})

	t.Run("implicit 200", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestAdmitting(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)

		policyTestServe(t, policy, policyTestOK(), req)

		if entry := audit.only(t); entry.Status != http.StatusOK {
			t.Fatalf("status in audit: got %d want 200", entry.Status)
		}
	})

	t.Run("only the first status counts", func(t *testing.T) {
		audit := &policyTestAudit{}
		policy := policyTestAdmitting(policyTestSource{}, audit)
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			w.WriteHeader(http.StatusInternalServerError)
		})

		recorder := policyTestServe(t, policy, next, req)

		if recorder.Code != http.StatusCreated {
			t.Fatalf("status: got %d want 201", recorder.Code)
		}
		if entry := audit.only(t); entry.Status != http.StatusCreated {
			t.Fatalf("status in audit: got %d want 201", entry.Status)
		}
	})
}

func TestPolicyWriterForwardsFlushAndHijack(t *testing.T) {
	t.Run("streaming abilities survive", func(t *testing.T) {
		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()
		base := &policyTestStreamWriter{
			conn:       server,
			readWriter: bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)),
		}
		audit := &policyTestAudit{}
		policy := policyTestRequired(policyTestResolved(), audit)
		var (
			flusherOK  bool
			hijackerOK bool
			hijacked   net.Conn
		)
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			flusher, ok := w.(http.Flusher)
			flusherOK = ok
			if ok {
				flusher.Flush()
			}
			hijacker, ok := w.(http.Hijacker)
			hijackerOK = ok
			if !ok {
				return
			}
			conn, readWriter, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("Hijack returned error: %v", err)
				return
			}
			if readWriter == nil {
				t.Error("Hijack returned a nil read writer")
			}
			hijacked = conn
		})
		req := httptest.NewRequest(http.MethodGet, "/runs/1/events", nil)

		policy.Middleware(next).ServeHTTP(base, req)

		if !flusherOK {
			t.Error("the wrapper swallowed http.Flusher, so SSE would never flush")
		}
		if !hijackerOK {
			t.Error("the wrapper swallowed http.Hijacker, so no upgrade connects")
		}
		if base.flushes != 1 {
			t.Fatalf("underlying flushes: got %d want 1", base.flushes)
		}
		if base.hijacks != 1 {
			t.Fatalf("underlying hijacks: got %d want 1", base.hijacks)
		}
		if hijacked != net.Conn(server) {
			t.Fatalf("hijacked conn: got %v want %v", hijacked, server)
		}
		if entry := audit.only(t); entry.Status != http.StatusOK {
			t.Fatalf("status in audit: got %d want 200", entry.Status)
		}
	})

	t.Run("no ability is invented", func(t *testing.T) {
		base := &policyTestPlainWriter{}
		wrapped, _ := policyWrap(base)
		if _, ok := wrapped.(http.Flusher); ok {
			t.Error("wrapper claims http.Flusher over a writer that cannot flush")
		}
		if _, ok := wrapped.(http.Hijacker); ok {
			t.Error("wrapper claims http.Hijacker over a writer that cannot hijack")
		}
		unwrapper, ok := wrapped.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			t.Fatal("wrapper does not expose Unwrap")
		}
		if unwrapper.Unwrap() != http.ResponseWriter(base) {
			t.Fatalf("Unwrap: got %v want the underlying %v", unwrapper.Unwrap(), base)
		}
	})
}

func TestPolicyNilAuthAndAudit(t *testing.T) {
	t.Run("not required", func(t *testing.T) {
		policy := Policy{Now: policyTestNow}
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)

		recorder := policyTestServe(t, policy, policyTestOK(), req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status: got %d want 200", recorder.Code)
		}
	})

	t.Run("required refuses without panicking", func(t *testing.T) {
		policy := Policy{Require: true, Now: policyTestNow}
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)

		recorder := policyTestServe(t, policy, policyTestNever(t), req)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status: got %d want 401", recorder.Code)
		}
		body := policyTestDecodeRefusal(t, recorder)
		if body.Error.Code != "unauthorized" {
			t.Fatalf("refusal code: got %q want unauthorized", body.Error.Code)
		}
	})
}

func TestPolicyAuditFailureDoesNotChangeResponse(t *testing.T) {
	logs := policyTestCaptureLog(t)
	audit := &policyTestAudit{err: errors.New("no space left on device")}
	policy := policyTestRequired(policyTestResolved(), audit)
	req := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	req.Header.Set("Authorization", "Bearer zf_supersecret")

	recorder := policyTestServe(t, policy, policyTestStatus(http.StatusAccepted), req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", recorder.Code)
	}
	if entries := audit.snapshot(); len(entries) != 0 {
		t.Fatalf("audit entries: got %d want 0", len(entries))
	}
	messages := logs.messages()
	if len(messages) != 1 || messages[0] != "auth: audit write failed" {
		t.Fatalf("log messages: got %q want one auth message", messages)
	}
}

func TestPolicyRefusalIsStillAuditedWhenRequired(t *testing.T) {
	audit := &policyTestAudit{}
	policy := policyTestRequired(policyTestSource{}, audit)
	req := httptest.NewRequest(http.MethodPost, "/runs", nil)

	policyTestServe(t, policy, policyTestNever(t), req)

	entry := audit.only(t)
	if entry.Method != http.MethodPost || entry.Path != "/runs" {
		t.Fatalf("request: got %s %s want POST /runs", entry.Method, entry.Path)
	}
	if entry.Decision != DecisionDeny || entry.Status != http.StatusUnauthorized {
		t.Fatalf("entry: got %q/%d want deny/401", entry.Decision, entry.Status)
	}
}

// TestPublicPathIsOnlyTheSignInRoutes pins the list itself. The console's own
// document is served under "/", and an earlier version of this predicate accepted
// an empty path -- which, after trimming a trailing slash, made "/" public and
// served the shell to an anonymous client instead of refusing it.
func TestPublicPathIsOnlyTheSignInRoutes(t *testing.T) {
	for _, path := range []string{SignInPath, SignInPath + "/", SessionPath} {
		if !PublicPath(path) {
			t.Errorf("PublicPath(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"", "/", "/index.html", "/assets/app.js", "/api/server", "/runs/start", "/auth/session/extra"} {
		if PublicPath(path) {
			t.Errorf("PublicPath(%q) = true, want false", path)
		}
	}
}
