// Package dshconsole serves the staged ZenForge build of the DeepSeek Harness
// browser console.
//
// The artifacts live beside this file and are embedded, so the console travels
// inside the binary and needs no bundler, package registry, or CDN at run time.
// The bytes are the upstream console with its branding patched to ZenForge by
// scripts/build-console.sh; webui/dsh/PROVENANCE.md records the revision, the
// patch list, and what was deliberately left out of the staged tree.
//
// This package is deliberately separate from package webui. That package owns
// the first-party console; this one owns the vendored, rebranded console. The
// host protocol that will choose between them (and feed the loader its plugin
// roster) is not implemented yet, so this package is only the artifacts, a
// handler for them, and their tests.
package dshconsole

import (
	"embed"
	"net/http"
	"path"
	"strings"
)

// The staged artifact set. Maps and the worker preview bundle are not staged,
// so they are not embedded either.
//
//go:embed index.html manifest.webmanifest favicon.svg assets plugins
var artifacts embed.FS

// Handler serves the console shell, its assets, and its client plugin bundles.
// "/" and "/index.html" answer the shell; every other request maps to the
// embedded file at the same path, and a path with no embedded file is a 404 so
// the console never claims a path the API owns.
func Handler() http.Handler {
	return http.HandlerFunc(serve)
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := embeddedName(r.URL.Path)
	data, err := artifacts.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	header := w.Header()
	header.Set("Content-Type", mediaType(name))
	header.Set("Cache-Control", cacheControl(name))
	header.Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// embeddedName maps a request path to an embedded file name. The path is
// cleaned against "/" first, so "../" segments collapse instead of escaping the
// embedded tree; the console root is the shell.
func embeddedName(urlPath string) string {
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" || name == "index.html" {
		return "index.html"
	}
	return name
}

// mediaType assigns the content type by extension. The staged tree has a closed
// extension set, and an explicit mapping keeps a script or stylesheet from being
// sniffed into a type the browser refuses to execute.
func mediaType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".webmanifest":
		return "application/manifest+json"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".ttf":
		return "font/ttf"
	default:
		return "application/octet-stream"
	}
}

// cacheControl keeps the shell revalidated, because it is the file that names
// the fingerprinted assets, while the assets and plugin bundles may be cached.
func cacheControl(name string) string {
	if name == "index.html" || name == "manifest.webmanifest" {
		return "no-store"
	}
	return "public, max-age=3600"
}
