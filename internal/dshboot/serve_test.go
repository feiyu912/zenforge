package dshboot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// serveEntries keeps one entry with a package-local chunk (so the derived chunk
// URL must resolve) and one entry whose client.js comes from the in-memory bytes.
func serveEntries() []Entry {
	return []Entry{
		{
			ID:     "@x/a",
			Rev:    "rev-a",
			Bundle: []byte("bundle-a"),
			FS: fstest.MapFS{
				"client.abc123.js": &fstest.MapFile{Data: []byte("chunk-a")},
			},
			Chunks: []string{"client.abc123.js"},
		},
		{ID: "@x/b", Rev: "rev-b", Bundle: []byte("bundle-b")},
	}
}

func newTestBundle(t *testing.T) *Bundle {
	t.Helper()
	bundle, err := New(serveEntries())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return bundle
}

func serve(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func TestHandlerServesCombinationURLInOrder(t *testing.T) {
	bundle := newTestBundle(t)
	graph := bundle.Graph()
	batch := graph.Batches[0]
	if batch.Phase != PhaseApplication {
		t.Fatalf("batch phase = %q, want %q", batch.Phase, PhaseApplication)
	}
	if len(batch.Entries) != 2 || batch.Entries[0] != "@x/a" || batch.Entries[1] != "@x/b" {
		t.Fatalf("batch entries = %v, want [@x/a @x/b]", batch.Entries)
	}

	recorder := serve(t, bundle.Handler(), http.MethodGet, batch.URL)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != contentTypeJavaScript {
		t.Fatalf("content-type = %q, want %q", got, contentTypeJavaScript)
	}
	if got := recorder.Header().Get("Cache-Control"); got != immutableCacheControl {
		t.Fatalf("cache-control = %q, want %q", got, immutableCacheControl)
	}
	if got, want := recorder.Body.String(), "bundle-a\n;\nbundle-b\n;\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerServesEntryURL(t *testing.T) {
	bundle := newTestBundle(t)
	entryURL := bundle.Graph().Entries[0].URL
	recorder := serve(t, bundle.Handler(), http.MethodGet, entryURL)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for %s", recorder.Code, entryURL)
	}
	if got, want := recorder.Body.String(), "bundle-a\n;\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerServesChunkURL(t *testing.T) {
	bundle := newTestBundle(t)
	chunkURL := ChunkURL("@x/a", "client.abc123.js", "rev-a")
	recorder := serve(t, bundle.Handler(), http.MethodGet, chunkURL)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for %s", recorder.Code, chunkURL)
	}
	if got := recorder.Header().Get("Content-Type"); got != contentTypeJavaScript {
		t.Fatalf("content-type = %q, want %q", got, contentTypeJavaScript)
	}
	if got, want := recorder.Body.String(), "chunk-a\n;\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestHandlerServesHeadWithoutBody(t *testing.T) {
	bundle := newTestBundle(t)
	batchURL := bundle.Graph().Batches[0].URL
	recorder := serve(t, bundle.Handler(), http.MethodHead, batchURL)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != contentTypeJavaScript {
		t.Fatalf("content-type = %q, want %q", got, contentTypeJavaScript)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD returned a %d-byte body", recorder.Body.Len())
	}
}

func TestHandlerNotFoundForUnadvertisedPaths(t *testing.T) {
	bundle := newTestBundle(t)
	handler := bundle.Handler()
	targets := []string{
		"/plugins/??@x/nope/client.js&rev=rev-a",
		"/plugins/../x",
		"/plugins/",
		"/plugins",
		"/",
	}
	for _, target := range targets {
		recorder := serve(t, handler, http.MethodGet, target)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, recorder.Code)
		}
	}
}

func TestHandlerRefusesStaleRevision(t *testing.T) {
	bundle := newTestBundle(t)
	batchURL := bundle.Graph().Batches[0].URL
	revision := strings.Index(batchURL, "&rev=")
	if revision < 0 {
		t.Fatalf("batch URL %q has no rev=", batchURL)
	}
	stale := batchURL[:revision] + "&rev=stale-revision"
	recorder := serve(t, bundle.Handler(), http.MethodGet, stale)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("stale rev status = %d, want 404 for %s", recorder.Code, stale)
	}
}

func TestHandlerRejectsUnsupportedMethod(t *testing.T) {
	bundle := newTestBundle(t)
	batchURL := bundle.Graph().Batches[0].URL
	recorder := serve(t, bundle.Handler(), http.MethodPost, batchURL)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", recorder.Code)
	}
}

func TestNewRejectsEntryWithoutBundleSource(t *testing.T) {
	_, err := New([]Entry{{ID: "@x/a", Rev: "rev-a"}})
	if err == nil {
		t.Fatal("New accepted an entry with no bundle bytes and no fs.FS")
	}
	if !strings.Contains(err.Error(), `entry "@x/a" has no bundle bytes and no fs.FS`) {
		t.Fatalf("error does not name the entry: %v", err)
	}
}

func TestNewRejectsMissingChunkFile(t *testing.T) {
	_, err := New([]Entry{{
		ID:     "@x/a",
		Rev:    "rev-a",
		Bundle: []byte("bundle-a"),
		FS:     fstest.MapFS{},
		Chunks: []string{"client.abc123.js"},
	}})
	if err == nil {
		t.Fatal("New accepted a declared chunk with no file")
	}
	if !strings.Contains(err.Error(), `entry "@x/a" chunk "client.abc123.js"`) {
		t.Fatalf("error does not name the entry and chunk: %v", err)
	}
}

func TestNewReadsClientBundleFromEntryFS(t *testing.T) {
	bundle, err := New([]Entry{{
		ID:  "@x/fs",
		Rev: "rev-fs",
		FS:  fstest.MapFS{"client.js": &fstest.MapFile{Data: []byte("from-fs")}},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	entryURL := bundle.Graph().Entries[0].URL
	recorder := serve(t, bundle.Handler(), http.MethodGet, entryURL)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got, want := recorder.Body.String(), "from-fs\n;\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
