// Package webui serves the built-in single-page console for `zenforge serve`.
//
// The console is compiled into the binary: an operator configures the server
// from the same server, so a page that needed a bundler, a package registry,
// or a CDN would make the console depend on the network it exists to point at
// a model endpoint. The three assets are therefore plain files embedded with
// go:embed and served with explicit content types — an extensionless or
// renamed asset must not silently become text/plain in the browser.
package webui

import (
	"embed"
	"net/http"
)

//go:embed index.html app.js style.css
var assets embed.FS

// Handler serves the console. index.html answers "/" (and "/index.html"), and
// the two assets answer /assets/app.js and /assets/style.css. Everything else
// is a 404, so the console never claims a path the API owns.
func Handler() http.Handler {
	return http.HandlerFunc(serve)
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, contentType := assetFor(r.URL.Path)
	if name == "" {
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, "console asset unavailable", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// assetFor maps a request path to the embedded file and its media type. The
// mapping is a closed set rather than a directory walk: the console ships
// exactly three files, and a request for anything else is a 404 the router
// can be reasoned about from this function alone.
func assetFor(path string) (name, contentType string) {
	switch path {
	case "/", "/index.html":
		return "index.html", "text/html; charset=utf-8"
	case "/assets/app.js":
		return "app.js", "text/javascript; charset=utf-8"
	case "/assets/style.css":
		return "style.css", "text/css; charset=utf-8"
	default:
		return "", ""
	}
}
