package dshapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// stubCredentials is the injected credential face the tests drive. It records
// what it was handed so a test can prove the value arrived and, just as
// importantly, that it never came back out.
type stubCredentials struct {
	configured bool
	stored     []string
	removed    int
	failStore  error
	failRemove error
}

func (s *stubCredentials) CredentialConfigured() bool { return s.configured }

func (s *stubCredentials) StoreCredential(value string) error {
	if s.failStore != nil {
		return s.failStore
	}
	s.stored = append(s.stored, value)
	s.configured = true
	return nil
}

func (s *stubCredentials) RemoveCredential() error {
	if s.failRemove != nil {
		return s.failRemove
	}
	s.removed++
	s.configured = false
	return nil
}

// credentialsFixture installs a stub store on a fresh handler.
func credentialsFixture(t *testing.T, cfg Config) (*fixture, *stubCredentials) {
	t.Helper()
	f := newFixture(t, cfg)
	store := &stubCredentials{configured: true}
	f.handler.SetCredentials(store)
	return f, store
}

func TestCredentialsDescribeReportsOneRedactedViewPerReference(t *testing.T) {
	f, _ := credentialsFixture(t, Config{})
	recorder := f.post(t, "/api/credentials/describe",
		rpcBody(t, "c1", "credentials/describe", `{"refs":["DASHSCOPE_API_KEY","OPENAI_API_KEY"]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeResponse(t, recorder)
	if !response.Result.OK {
		t.Fatalf("result = %+v, want ok", response.Result.Error)
	}
	var views map[string]map[string]any
	if err := json.Unmarshal(response.Result.Value, &views); err != nil {
		t.Fatalf("decode value %s: %v", response.Result.Value, err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %v, want one per requested reference", views)
	}
	for ref, view := range views {
		if len(view) != 3 {
			t.Fatalf("%s view = %v, want exactly configured/source/writable", ref, view)
		}
		if view["configured"] != true || view["writable"] != true || view["source"] != credentialRefSource {
			t.Fatalf("%s view = %v, want configured and writable true from source %q", ref, view, credentialRefSource)
		}
	}
	// The view is the whole answer: no field carries a credential. The exact
	// per-view field set is asserted above, so this checks the response as a
	// whole for the names a secret would travel under.
	for _, forbidden := range []string{"apiKey", "secret", "sk-"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response mentions %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestCredentialsDescribeReportsAnAbsentCredential(t *testing.T) {
	f, store := credentialsFixture(t, Config{})
	store.configured = false
	response := decodeResponse(t, f.post(t, "/api/credentials/describe",
		rpcBody(t, "c1", "credentials/describe", `{"refs":["OPENAI_API_KEY"]}`)))
	var views map[string]CredentialInfo
	if err := json.Unmarshal(response.Result.Value, &views); err != nil {
		t.Fatalf("decode value %s: %v", response.Result.Value, err)
	}
	if views["OPENAI_API_KEY"].Configured {
		t.Fatalf("view = %+v, want configured false", views["OPENAI_API_KEY"])
	}
}

func TestCredentialsSetStoresTheValueAndNeverReturnsIt(t *testing.T) {
	const secret = "sk-sentinel-2f4c9a17"
	f, store := credentialsFixture(t, Config{})
	recorder := f.post(t, "/api/credentials/set",
		rpcBody(t, "c1", "credentials/set", fmt.Sprintf(`{"ref":"DASHSCOPE_API_KEY","value":%q}`, secret)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	response := decodeResponse(t, recorder)
	if !response.Result.OK {
		t.Fatalf("result = %+v, want ok", response.Result.Error)
	}
	if response.Result.Value != nil && string(response.Result.Value) != "null" {
		t.Fatalf("value = %s, want no value: the upstream shape is Promise<void>", response.Result.Value)
	}
	if len(store.stored) != 1 || store.stored[0] != secret {
		t.Fatalf("stored = %q, want the value to have arrived once", store.stored)
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("the secret came back in the response: %s", recorder.Body.String())
	}
}

func TestCredentialsSetAcceptsAnyReferenceForTheSingleCredential(t *testing.T) {
	// ADR 0084: one credential backs every reference, so a Qwen key named under
	// any provider's reference is accepted rather than refused for a mismatch
	// this host cannot know about.
	f, store := credentialsFixture(t, Config{})
	for _, ref := range []string{"DASHSCOPE_API_KEY", "OPENAI_API_KEY", "QWEN_API_KEY"} {
		response := decodeResponse(t, f.post(t, "/api/credentials/set",
			rpcBody(t, "c1", "credentials/set", fmt.Sprintf(`{"ref":%q,"value":"sk-value"}`, ref))))
		if !response.Result.OK {
			t.Fatalf("ref %q: result = %+v, want ok", ref, response.Result.Error)
		}
	}
	if len(store.stored) != 3 {
		t.Fatalf("stored = %q, want three writes", store.stored)
	}
}

func TestCredentialsArgumentsAreValidated(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		method   string
		args     string
		code     string
	}{
		{"set rejects a reference with a dash", "/api/credentials/set", "credentials/set", `{"ref":"HAS-DASH","value":"v"}`, codeBadRequest},
		{"set rejects a reference starting with a digit", "/api/credentials/set", "credentials/set", `{"ref":"1BAD","value":"v"}`, codeBadRequest},
		{"set rejects an empty reference", "/api/credentials/set", "credentials/set", `{"ref":"","value":"v"}`, codeBadRequest},
		{"set rejects a missing reference", "/api/credentials/set", "credentials/set", `{"value":"v"}`, codeBadRequest},
		{"set rejects an empty value", "/api/credentials/set", "credentials/set", `{"ref":"OPENAI_API_KEY","value":""}`, codeBadRequest},
		{"set rejects a missing value", "/api/credentials/set", "credentials/set", `{"ref":"OPENAI_API_KEY"}`, codeBadRequest},
		{"set rejects a non-string value", "/api/credentials/set", "credentials/set", `{"ref":"OPENAI_API_KEY","value":12}`, codeArgumentsInvalid},
		{"set rejects an unexpected argument", "/api/credentials/set", "credentials/set", `{"ref":"OPENAI_API_KEY","value":"v","extra":1}`, codeArgumentsInvalid},
		{"unset rejects an unexpected argument", "/api/credentials/unset", "credentials/unset", `{"ref":"OPENAI_API_KEY","extra":1}`, codeArgumentsInvalid},
		{"describe requires refs", "/api/credentials/describe", "credentials/describe", `{}`, codeArgumentsInvalid},
		{"describe rejects a non-array", "/api/credentials/describe", "credentials/describe", `{"refs":"OPENAI_API_KEY"}`, codeBadRequest},
		{"describe rejects a bad entry", "/api/credentials/describe", "credentials/describe", `{"refs":["GOOD_ONE","has-dash"]}`, codeBadRequest},
		{"describe rejects an unexpected argument", "/api/credentials/describe", "credentials/describe", `{"refs":["A"],"extra":1}`, codeArgumentsInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f, store := credentialsFixture(t, Config{})
			response := decodeResponse(t, f.post(t, testCase.endpoint, rpcBody(t, "c1", testCase.method, testCase.args)))
			if response.Result.OK {
				t.Fatalf("result = ok, want failure %q", testCase.code)
			}
			if response.Result.Error.Code != testCase.code {
				t.Fatalf("code = %q, want %q (%s)", response.Result.Error.Code, testCase.code, response.Result.Error.Message)
			}
			if len(store.stored) != 0 || store.removed != 0 {
				t.Fatalf("a refused request reached the store: stored=%q removed=%d", store.stored, store.removed)
			}
		})
	}
}

func TestCredentialsDescribeBoundsTheReferenceBatch(t *testing.T) {
	refs := make([]string, 0, maxDescribeCredentialRefs+1)
	for index := 0; index <= maxDescribeCredentialRefs; index++ {
		refs = append(refs, fmt.Sprintf("REF_%d", index))
	}
	encoded, err := json.Marshal(map[string]any{"refs": refs})
	if err != nil {
		t.Fatalf("marshal refs: %v", err)
	}
	f, _ := credentialsFixture(t, Config{})
	response := decodeResponse(t, f.post(t, "/api/credentials/describe",
		rpcBody(t, "c1", "credentials/describe", string(encoded))))
	if response.Result.OK {
		t.Fatal("result = ok, want the batch bound to refuse it")
	}
	if response.Result.Error.Code != codeBadRequest {
		t.Fatalf("code = %q, want %q", response.Result.Error.Code, codeBadRequest)
	}
	if response.Result.Error.Details["limit"] != float64(maxDescribeCredentialRefs) {
		t.Fatalf("details = %v, want the bound named", response.Result.Error.Details)
	}
}

func TestCredentialsUnsetClearsTheCredential(t *testing.T) {
	f, store := credentialsFixture(t, Config{})
	response := decodeResponse(t, f.post(t, "/api/credentials/unset",
		rpcBody(t, "c1", "credentials/unset", `{"ref":"OPENAI_API_KEY"}`)))
	if !response.Result.OK {
		t.Fatalf("result = %+v, want ok", response.Result.Error)
	}
	if store.removed != 1 || store.configured {
		t.Fatalf("removed = %d configured = %v, want one removal and no credential", store.removed, store.configured)
	}
}

func TestCredentialsWithoutAStoreAnswerUnimplemented(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct{ endpoint, method, args string }{
		{"/api/credentials/describe", "credentials/describe", `{"refs":["OPENAI_API_KEY"]}`},
		{"/api/credentials/set", "credentials/set", `{"ref":"OPENAI_API_KEY","value":"v"}`},
		{"/api/credentials/unset", "credentials/unset", `{"ref":"OPENAI_API_KEY"}`},
	}
	for _, testCase := range cases {
		response := decodeResponse(t, f.post(t, testCase.endpoint, rpcBody(t, "c1", testCase.method, testCase.args)))
		if response.Result.OK {
			t.Fatalf("%s: result = ok, want unimplemented", testCase.method)
		}
		if response.Result.Error.Code != codeUnimplemented {
			t.Fatalf("%s: code = %q, want %q", testCase.method, response.Result.Error.Code, codeUnimplemented)
		}
		if response.Result.Error.Details["dependency"] != "CredentialStore" {
			t.Fatalf("%s: details = %v, want the missing dependency named", testCase.method, response.Result.Error.Details)
		}
	}
}

func TestCredentialsValueNeverReachesTheLog(t *testing.T) {
	const secret = "sk-sentinel-log-9b3e"
	var logged bytes.Buffer
	f, _ := credentialsFixture(t, Config{Logger: slog.New(slog.NewTextHandler(&logged, nil))})
	f.post(t, "/api/credentials/set",
		rpcBody(t, "c1", "credentials/set", fmt.Sprintf(`{"ref":"DASHSCOPE_API_KEY","value":%q}`, secret)))
	// An unserved endpoint forces a diagnostic line, proving the logger this
	// test installed is the one the handler uses.
	notFound := f.post(t, "/api/workspace/files", rpcBody(t, "c2", "workspace/files", `{}`))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", notFound.Code)
	}
	if !strings.Contains(logged.String(), "workspace") {
		t.Fatalf("log = %q, want the unserved endpoint recorded", logged.String())
	}
	if strings.Contains(logged.String(), secret) {
		t.Fatalf("the secret reached the log: %q", logged.String())
	}
}

func TestCredentialsStoreFailureIsReportedWithoutTheSecret(t *testing.T) {
	const secret = "sk-sentinel-refused-77aa"
	f := newFixture(t, Config{})
	// A provider error string is not trusted to be free of the value it was
	// just handed, so the refusal must redact it.
	f.handler.SetCredentials(&stubCredentials{failStore: fmt.Errorf("provider rejected %s", secret)})
	response := decodeResponse(t, f.post(t, "/api/credentials/set",
		rpcBody(t, "c1", "credentials/set", fmt.Sprintf(`{"ref":"DASHSCOPE_API_KEY","value":%q}`, secret))))
	if response.Result.OK {
		t.Fatal("result = ok, want the refusal reported")
	}
	if strings.Contains(response.Result.Error.Message, secret) {
		t.Fatalf("message = %q, want the secret redacted", response.Result.Error.Message)
	}
	if !strings.Contains(response.Result.Error.Message, "[redacted]") {
		t.Fatalf("message = %q, want the redaction marked", response.Result.Error.Message)
	}
}
