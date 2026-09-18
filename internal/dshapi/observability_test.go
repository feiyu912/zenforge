package dshapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// nonEmptyLines splits a log buffer into the lines a reader would count.
func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestUnservedEndpointLogsNamespaceAndMethod proves the diagnostic records the
// one thing an implementer needs — which namespace and method the console
// asked for — and nothing a credential could be read out of.
func TestUnservedEndpointLogsNamespaceAndMethod(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   string
		method     string
		namespace  string
		methodName string
	}{
		{"unknown namespace", "/api/tools/list", "tools/list", "tools", "list"},
		{"unknown method", "/api/session/nope", "session/nope", "session", "nope"},
	}
	// Distinctive sentinels: each must be absent from the recorded line.
	const (
		rpcID     = "rpc-correlation-SECRET-7f3a"
		secretArg = "ARGUMENT-VALUE-DO-NOT-LOG"
		secretKey = "sk-live-APIKEY-DO-NOT-LOG"
	)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var buffer bytes.Buffer
			f := newFixture(t, Config{Logger: slog.New(slog.NewTextHandler(&buffer, nil))})
			args := `{"cursor":"` + secretArg + `","apiKey":"` + secretKey + `","nested":{"token":"` + secretArg + `"}}`
			recorder := f.post(t, testCase.endpoint, rpcBody(t, rpcID, testCase.method, args))

			// The response is unchanged: a bare 404 with today's exact body.
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Body.String(); got != "not found\n" {
				t.Fatalf("404 body = %q, want the unchanged %q", got, "not found\n")
			}

			lines := nonEmptyLines(buffer.String())
			if len(lines) != 1 {
				t.Fatalf("want exactly one log line, got %d: %q", len(lines), buffer.String())
			}
			line := lines[0]
			if !strings.Contains(line, "namespace="+testCase.namespace) {
				t.Fatalf("log line does not name namespace %q: %s", testCase.namespace, line)
			}
			if !strings.Contains(line, "method="+testCase.methodName) {
				t.Fatalf("log line does not name method %q: %s", testCase.methodName, line)
			}
			for _, secret := range []string{rpcID, secretArg, secretKey, "apiKey", "nested", "payload", "cursor"} {
				if strings.Contains(line, secret) {
					t.Fatalf("log line leaked %q: %s", secret, line)
				}
			}
		})
	}
}

// TestServedEndpointLogsNothing pins the "per unknown endpoint" scope: a
// served method must stay quiet even with a logger installed.
func TestServedEndpointLogsNothing(t *testing.T) {
	var buffer bytes.Buffer
	f := newFixture(t, Config{Logger: slog.New(slog.NewTextHandler(&buffer, nil))})
	recorder := f.post(t, "/api/session/list", rpcBody(t, "rpc-served", "session/list", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if buffer.Len() != 0 {
		t.Fatalf("a served endpoint wrote %q; the diagnostic is for unserved endpoints only", buffer.String())
	}
}

// TestNilLoggerWritesNothing is the zero-value guard: Config's nil Logger must
// not fall back to slog.Default. The buffer is installed as the process
// default, so any fallback would land in it.
func TestNilLoggerWritesNothing(t *testing.T) {
	f := newFixture(t, Config{})
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	recorder := f.post(t, "/api/tools/list",
		rpcBody(t, "rpc-nil-logger", "tools/list", `{"cursor":"x"}`))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if buffer.Len() != 0 {
		t.Fatalf("nil Logger wrote %q; the zero Config must be silent, not use slog.Default", buffer.String())
	}
}
