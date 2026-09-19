package dshconsole

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
)

// The rebrand is the reason this artifact tree exists. Upstream's console is
// served under this project's name, so a staged file that still carried an
// upstream product name would hand the operator another product's identity —
// and it would be invisible in a browser test, because a title is not the only
// place the name appears.
func TestStagedConsoleCarriesNoUpstreamProductName(t *testing.T) {
	for _, name := range stagedTextFiles(t) {
		data, err := artifacts.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		for _, forbidden := range []string{"DeepSeek Harness", "DSH Local Build", "HARNESS"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s still contains the upstream string %q", name, forbidden)
			}
		}
	}
}

// The shell has to arrive as HTML with this project's title, and each staged
// script and stylesheet has to arrive with a type the browser will execute
// rather than sniff into something it refuses.
func TestStagedConsoleServesTheShellAndItsAssets(t *testing.T) {
	handler := Handler()

	shell := httptest.NewRecorder()
	handler.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/", nil))
	if shell.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", shell.Code)
	}
	if got := shell.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("GET / content type = %q, want HTML", got)
	}
	if !strings.Contains(shell.Body.String(), "<title>zenforge</title>") {
		t.Error("the served shell does not carry the zenforge title")
	}

	types := map[string]string{
		".js":  "text/javascript; charset=utf-8",
		".mjs": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8",
	}
	for _, name := range stagedTextFiles(t) {
		want, ok := types[strings.ToLower(path.Ext(name))]
		if !ok {
			continue
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/"+name, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET /%s = %d, want 200", name, recorder.Code)
			continue
		}
		if got := recorder.Header().Get("Content-Type"); got != want {
			t.Errorf("GET /%s content type = %q, want %q", name, got, want)
		}
	}
}

// A console that reached for a CDN would depend on the network it exists to
// point at a model endpoint; every local reference must resolve from the staged
// tree instead.
func TestStagedConsoleReferencesOnlyWhatItShips(t *testing.T) {
	shell, err := artifacts.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	handler := Handler()
	for _, ref := range localReferences(string(shell)) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, ref, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("index.html references %q, which is answered with %d", ref, recorder.Code)
		}
	}
}

// The handler serves an embedded tree, so anything it does not hold is a 404 —
// including a path that tries to climb out of it — and a write is never
// meaningful.
func TestStagedConsoleRefusesWhatItDoesNotHold(t *testing.T) {
	handler := Handler()
	for _, target := range []string{"/nope.js", "/assets/../secrets.js", "/../console.go"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, recorder.Code)
		}
	}

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", post.Code)
	}
}

// stagedTextFiles lists the staged files whose bytes are text. Fonts are
// skipped: the brand check reads documents and scripts, and decoding a woff2 as
// a string would only produce noise.
func stagedTextFiles(t *testing.T) []string {
	t.Helper()
	var names []string
	err := fs.WalkDir(artifacts, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".html", ".js", ".mjs", ".css", ".json", ".webmanifest", ".svg", ".txt", ".md":
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk staged artifacts: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the staged console holds no text files; the embed is empty")
	}
	return names
}

// localReferences extracts the local asset references from a document,
// resolving each against the document root the way a browser would.
func localReferences(document string) []string {
	var refs []string
	for _, field := range strings.FieldsFunc(document, func(r rune) bool { return r == '"' || r == '\'' }) {
		switch {
		case strings.HasPrefix(field, "http://"), strings.HasPrefix(field, "https://"), strings.HasPrefix(field, "//"):
			continue
		case strings.HasPrefix(field, "/assets/"), strings.HasPrefix(field, "/plugins/"), strings.HasPrefix(field, "./assets/"):
			refs = append(refs, path.Clean("/"+strings.TrimPrefix(field, "./")))
		}
	}
	return refs
}
