// Package dshboot implements the boot half of the upstream console host
// protocol: composing the window.__DSH_BOOT__ module graph, rendering the
// index.html injection rows, and serving the advertised /plugins bundle URLs.
//
// It is a package of its own because boot is the one part of the console host
// that fails loudly and totally when it is wrong. The shell throws on a missing
// __ModuleLoader__, on a malformed __DSH_BOOT__, and on any bundle URL without a
// rev= parameter; a graph that validates here can still show an empty page, but
// a graph that does not validate never boots at all. Keeping the graph, the
// markup, and the byte route together lets them be checked as one contract
// before anything is served.
//
// The wire types, URL spellings, and injection order are copied from the
// upstream sources cited in docs/dsh-console-protocol-recon.md
// (client/modules/src/index.ts and client/modules/src/client/manifest.ts)
// rather than re-derived. This package deliberately does not mount itself into a
// server: the caller decides where /plugins lives and how index.html is served.
package dshboot
