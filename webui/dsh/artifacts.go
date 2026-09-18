package dshconsole

import (
	"io/fs"
)

// Index returns the staged shell's bytes. Package webui normally serves this
// file through Handler, but the host has to inject the boot rows into it
// before it is sent, so the bytes are exposed here rather than only as a
// response body. The embedded read cannot fail for the committed tree; the
// error is returned so a corrupted embed is diagnosable instead of silent.
func Index() ([]byte, error) {
	return artifacts.ReadFile("index.html")
}

// Plugins returns the staged client plugin tree, rooted at the plugins
// directory. A host composing the boot graph reads each entry's client.js
// through this view (fs.Sub by entry directory), so the graph and the served
// bytes come from the same embedded snapshot.
func Plugins() fs.FS {
	sub, err := fs.Sub(artifacts, "plugins")
	if err != nil {
		// Unreachable: "plugins" is a directory in the //go:embed pattern, and
		// fs.Sub only rejects an invalid path. Panicking keeps a broken embed a
		// programming error rather than a silently empty plugin tree.
		panic("dshconsole: plugins is not an embedded directory: " + err.Error())
	}
	return sub
}
