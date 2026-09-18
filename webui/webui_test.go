package webui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The console is compiled into the binary, so every asset it asks for has to be
// one this package ships: a missing file would be a broken console in an
// otherwise healthy release, and the content types are asserted because a
// mislabelled script is silently refused by the browser.
func TestConsoleServesItsShellAndAssetsWithTheirMediaTypes(t *testing.T) {
	handler := Handler()
	cases := []struct {
		path     string
		wantType string
	}{
		{"/", "text/html; charset=utf-8"},
		{"/index.html", "text/html; charset=utf-8"},
		{"/assets/app.js", "text/javascript; charset=utf-8"},
		{"/assets/style.css", "text/css; charset=utf-8"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, recorder.Code)
			continue
		}
		if got := recorder.Header().Get("Content-Type"); got != tc.wantType {
			t.Errorf("GET %s content type = %q, want %q", tc.path, got, tc.wantType)
		}
		if recorder.Body.Len() == 0 {
			t.Errorf("GET %s served an empty body", tc.path)
		}
	}
}

// A console that reached for a CDN would depend on the network it exists to
// point at a model endpoint, so an external asset reference is a defect even
// when it would load. Every local reference must resolve through this handler.
func TestConsoleReferencesOnlyAssetsItShips(t *testing.T) {
	data, err := assets.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	page := string(data)
	handler := Handler()
	for _, match := range regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		ref := match[1]
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "//") {
			t.Errorf("index.html references the external asset %q; the console must ship what it needs", ref)
			continue
		}
		if strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "data:") {
			continue
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ref, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("index.html references %q, which the console answers with %d", ref, recorder.Code)
		}
	}
}

// The console owns three paths and nothing else: a catch-all would shadow the
// API routes it is served beside, and a write to it is never meaningful.
func TestConsoleOwnsOnlyItsOwnPaths(t *testing.T) {
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("GET /api/settings through the console handler = %d, want 404", recorder.Code)
	}

	post := httptest.NewRecorder()
	Handler().ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", post.Code)
	}
}
