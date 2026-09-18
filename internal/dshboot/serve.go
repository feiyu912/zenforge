package dshboot

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Bundle is a composed boot graph plus the exact bytes it advertises. It is
// immutable after New, which is what lets the route answer every advertised URL
// from an in-memory table instead of touching the filesystem per request.
type Bundle struct {
	graph     *Graph
	resources map[string][]byte
}

// New composes and validates the graph and resolves every advertised bundle, so
// a roster with a missing bundle or an unreadable chunk fails here rather than
// at the first boot request, where the console would only report a plugin that
// never activated.
func New(entries []Entry) (*Bundle, error) {
	graph, err := BuildGraph(entries)
	if err != nil {
		return nil, err
	}
	bodies := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		body, err := readClientBundle(entry)
		if err != nil {
			return nil, err
		}
		bodies[entry.ID] = body
	}

	resources := make(map[string][]byte, len(graph.Batches)+len(graph.Entries))
	urlByID := make(map[string]string, len(graph.Entries))
	for _, row := range graph.Entries {
		urlByID[row.ID] = row.URL
	}
	// The single-package endpoint is part of the wire (the console uses it after
	// an HMR invalidation), so it is served from the same immutable table as the
	// batches rather than being left to 404.
	for _, entry := range entries {
		resources[urlByID[entry.ID]] = scriptBody([][]byte{bodies[entry.ID]})
	}
	for _, batch := range graph.Batches {
		sources := make([][]byte, 0, len(batch.Entries))
		for _, id := range batch.Entries {
			sources = append(sources, bodies[id])
		}
		resources[batch.URL] = scriptBody(sources)
	}
	// Chunk URLs are not on the wire: the console derives a sibling chunk from
	// the combination URL shape, so the host advertises them by making exactly
	// those derived URLs resolvable.
	for _, entry := range entries {
		for _, name := range entry.Chunks {
			if entry.FS == nil {
				return nil, fmt.Errorf("dshboot: entry %q declares chunk %q but has no fs.FS", entry.ID, name)
			}
			data, err := fs.ReadFile(entry.FS, name)
			if err != nil {
				return nil, fmt.Errorf("dshboot: read entry %q chunk %q: %w", entry.ID, name, err)
			}
			resources[ChunkURL(entry.ID, name, entry.Rev)] = scriptBody([][]byte{data})
		}
	}
	return &Bundle{graph: graph, resources: resources}, nil
}

// Graph returns the composed wire graph the bundle serves. It is safe to embed
// in an index and to read for diagnostics; the bundle never mutates it.
func (b *Bundle) Graph() *Graph {
	return b.graph
}

// Inject renders the boot rows into an index.html document for this bundle's
// graph.
func (b *Bundle) Inject(html string) string {
	return RenderInjections(html, b.graph)
}

// Handler serves exactly the URLs the graph advertises. Lookup is an exact
// path-and-query match against the table built by New, so an unadvertised
// combination, a stale rev, a directory-shaped path, or a traversal attempt has
// no entry and is a 404 — no filesystem path is ever joined or walked. GET and
// HEAD share the immutable headers; other methods are 405.
func (b *Bundle) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, ok := b.resources[pluginRequestKey(r.URL)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentTypeJavaScript)
		w.Header().Set("Cache-Control", immutableCacheControl)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(body)
	})
}

// contentTypeJavaScript is the type the console expects for every bundle; served
// as anything else the classic script element would be rejected outright.
const contentTypeJavaScript = "text/javascript; charset=utf-8"

// immutableCacheControl matches upstream: a revisioned URL never changes, so the
// browser may keep it for a year and skip revalidation.
const immutableCacheControl = "public, max-age=31536000, immutable"

// pluginRequestKey reconstructs the exact request target New keyed resources by.
// The combination route spells its query as ??<id>/client.js…, so the leading
// question mark of the raw query is preserved rather than normalized away.
func pluginRequestKey(requestURL *url.URL) string {
	if requestURL == nil {
		return ""
	}
	if requestURL.RawQuery == "" {
		return requestURL.Path
	}
	return requestURL.Path + "?" + requestURL.RawQuery
}

// readClientBundle resolves one entry's client.js bytes, preferring the explicit
// in-memory bundle and otherwise reading the entry-rooted file set.
func readClientBundle(entry Entry) ([]byte, error) {
	if entry.Bundle != nil {
		return entry.Bundle, nil
	}
	if entry.FS == nil {
		return nil, fmt.Errorf("dshboot: entry %q has no bundle bytes and no fs.FS", entry.ID)
	}
	data, err := fs.ReadFile(entry.FS, clientBundleFileInEntryFS)
	if err != nil {
		return nil, fmt.Errorf("dshboot: read entry %q %s: %w", entry.ID, clientBundleFileInEntryFS, err)
	}
	return data, nil
}

// scriptBody concatenates one or more factory registrations the way the host
// renderer does: strip bundle-local debug trailers, ensure each source ends with
// a newline, and separate registrations with a terminated empty statement so a
// bundle missing a trailing semicolon cannot swallow the next registration.
func scriptBody(sources [][]byte) []byte {
	var out []byte
	for _, source := range sources {
		out = append(out, prepareSource(source)...)
		out = append(out, ";\n"...)
	}
	return out
}

// Trailer directives name the bundle's own file, which is wrong once several
// bundles share a combination URL (and the last one would win); they are removed
// before concatenation.
var (
	sourceMapTrailer = regexp.MustCompile(`(?:\r?\n)?//# sourceMappingURL=[^\r\n]*(?:\r?\n)?$`)
	sourceURLTrailer = regexp.MustCompile(`(?:\r?\n)?//# sourceURL=[^\r\n]+(?:\r?\n)?$`)
)

func prepareSource(source []byte) []byte {
	text := sourceURLTrailer.ReplaceAllString(string(source), "")
	text = sourceMapTrailer.ReplaceAllString(text, "")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text)
}
