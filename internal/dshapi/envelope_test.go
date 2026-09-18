package dshapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestEnvelopeSuccessEchoesRPCID(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/list", rpcBody(t, "rpc-success-1", "session/list", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	envelope := decodeResponse(t, recorder)
	if envelope.Type != "server-response" {
		t.Fatalf("type = %q, want server-response", envelope.Type)
	}
	if envelope.RPCID != "rpc-success-1" {
		t.Fatalf("rpcId = %q, want rpc-success-1", envelope.RPCID)
	}
	if !envelope.Result.OK {
		t.Fatalf("result.ok = false: %s", recorder.Body.String())
	}
}

func TestEnvelopeEchoesUnusualRPCIDByteForByte(t *testing.T) {
	f := newFixture(t, Config{})
	rpcID := "rpc-<&>-\u2028/😀\"quote\\-" + strings.Repeat("x", 256)
	encoded, err := json.Marshal(rpcID)
	if err != nil {
		t.Fatalf("marshal rpcId: %v", err)
	}
	body := fmt.Sprintf(`{"type":"client-request","rpcId":%s,"method":"session/list","payload":{"args":{}}}`, encoded)
	recorder := f.post(t, "/api/session/list", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"rpcId":`+string(encoded)+`,"result":`) {
		t.Fatalf("rpcId was not echoed byte-for-byte:\nsent %s\ngot  %s", body, recorder.Body.String())
	}
	var decoded struct {
		RPCID string `json:"rpcId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.RPCID != rpcID {
		t.Fatalf("decoded rpcId = %q, want %q", decoded.RPCID, rpcID)
	}
}

func TestEnvelopeUnknownMethodIs404(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct {
		endpoint string
		method   string
	}{
		{"/api/session/nope", "session/nope"},
		{"/api/tools/list", "tools/list"},
		{"/api/$events/result", "$events/result"},
		{"/api/session", "session"},
		{"/api/a/b/c", "a/b/c"},
		{"/api/", "x"},
	}
	for _, testCase := range cases {
		recorder := f.post(t, testCase.endpoint, rpcBody(t, "rpc-404", testCase.method, ""))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", testCase.endpoint, recorder.Code)
		}
	}
}

func TestEnvelopeMethodPathMismatchIs400(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/list", rpcBody(t, "rpc-mismatch", "session/create", ""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "does not match") {
		t.Fatalf("400 body does not explain the mismatch: %s", recorder.Body.String())
	}
}

func TestEnvelopeRejectsMalformedRequests(t *testing.T) {
	f := newFixture(t, Config{})
	cases := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"type":"client-request"`},
		{"not an object", `[]`},
		{"wrong type", `{"type":"server-response","rpcId":"r","method":"session/list","payload":{"args":{}}}`},
		{"missing type", `{"rpcId":"r","method":"session/list","payload":{"args":{}}}`},
		{"missing rpcId", `{"type":"client-request","method":"session/list","payload":{"args":{}}}`},
		{"empty rpcId", `{"type":"client-request","rpcId":"","method":"session/list","payload":{"args":{}}}`},
		{"null rpcId", `{"type":"client-request","rpcId":null,"method":"session/list","payload":{"args":{}}}`},
		{"missing method", `{"type":"client-request","rpcId":"r","payload":{"args":{}}}`},
		{"missing payload", `{"type":"client-request","rpcId":"r","method":"session/list"}`},
		{"payload not an object", `{"type":"client-request","rpcId":"r","method":"session/list","payload":[]}`},
		{"missing args", `{"type":"client-request","rpcId":"r","method":"session/list","payload":{}}`},
		{"extra payload key", `{"type":"client-request","rpcId":"r","method":"session/list","payload":{"args":{},"extra":1}}`},
		{"duplicated args", `{"type":"client-request","rpcId":"r","method":"session/list","payload":{"args":{},"args":{}}}`},
		{"args not an object", `{"type":"client-request","rpcId":"r","method":"session/list","payload":{"args":[]}}`},
		{"args is a string", `{"type":"client-request","rpcId":"r","method":"session/list","payload":{"args":"x"}}`},
		{"duplicated top-level field", `{"type":"client-request","type":"client-request","rpcId":"r","method":"session/list","payload":{"args":{}}}`},
		{"trailing data", `{"type":"client-request","rpcId":"r","method":"session/list","payload":{"args":{}}} extra`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := f.post(t, "/api/session/list", testCase.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			// A protocol error must not look like a result envelope; otherwise
			// the client would treat the 4xx as a transport failure anyway.
			var probe map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &probe); err != nil {
				t.Fatalf("400 body is not JSON: %s", recorder.Body.String())
			}
			if _, isResult := probe["result"]; isResult {
				t.Fatalf("protocol error was written as a result envelope: %s", recorder.Body.String())
			}
		})
	}
}

func TestEnvelopeRejectsNonPost(t *testing.T) {
	f := newFixture(t, Config{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		recorder := f.request(t, method, "/api/session/list", "", nil)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, recorder.Code)
		}
	}
}

func TestEnvelopeMalformedRequestStillEchoesUsableRPCID(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/list",
		`{"type":"client-request","rpcId":"rpc-known","method":"session/list","payload":{"args":{},"extra":1}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	var probe struct {
		RPCID string `json:"rpcId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &probe); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if probe.RPCID != "rpc-known" {
		t.Fatalf("rpcId = %q, want rpc-known", probe.RPCID)
	}
}

func TestMethodFailureIsWellFormedEnvelope(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/prompt",
		rpcBody(t, "rpc-failure", "session/prompt",
			`{"requestId":"req-1","sessionId":"run-missing","mode":"queue","content":[{"type":"text","text":"hi"}]}`))
	envelope := assertMethodFailure(t, recorder, codeSessionNotFound)
	if envelope.RPCID != "rpc-failure" {
		t.Fatalf("rpcId = %q, want rpc-failure", envelope.RPCID)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if strings.Contains(recorder.Body.String(), "http: ") {
		t.Fatalf("method failure leaked a bare http.Error body: %s", recorder.Body.String())
	}
}

func TestMethodFailureContainsNoSuccessValue(t *testing.T) {
	f := newFixture(t, Config{})
	recorder := f.post(t, "/api/session/cancel", rpcBody(t, "rpc-failure", "session/cancel",
		`{"sessionId":"run-missing"}`))
	if !strings.Contains(recorder.Body.String(), `"ok":false`) {
		t.Fatalf("body is not a failure result: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), `"value"`) {
		t.Fatalf("failure result carries a value: %s", recorder.Body.String())
	}
}
