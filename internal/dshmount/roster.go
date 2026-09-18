package dshmount

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"

	"github.com/feiyu912/zenforge/internal/dshboot"
)

// rosterJSON is the checked-in graph roster. It is generated, never hand-edited:
// scripts/gen-dsh-roster.py reads the staged plugin tree, the staged shell
// assets, and the pinned upstream package manifests at revision
// ddefc45fbc7f8e46dd73185e68295696d1297887 (0.1.6-alpha.2) and writes every
// field the boot graph needs.
//
// A client bundle is only a classic script that registers
// `window.__ModuleLoader__.load({id, factory})`. The package id, the `inject`
// package edges, and the `immediately` prefetch mark come from the owning
// package's `package.json` under `dsh.client`, read by upstream's resolveMeta
// (packages/client/modules/src/index.ts:781-816); graphRow copies them onto the
// wire verbatim (:438-448) and orderByModuleGraph orders rows by `external`
// (:460-492). `external` cannot come from the manifest for most packages — the
// declaration omits a package's inject-covered requests — so it is scanned from
// the shipped bundle bytes, exactly the `require("<specifier>")` calls the
// factory makes at activation. shellProvided names the specifiers no graph row
// provides and the staged shell asset that seeds each. A rebuild reproduces this
// file by re-running the generator against the same revision and staged tree.
//
//go:embed roster.json
var rosterJSON []byte

// rosterManifest is the on-disk shape. It records the entries that are
// advertised, the shell-seeded module specifiers, the staged bundles withheld
// from the graph, and the upstream web packages the staging recipe omitted
// entirely, so a reviewer can see both the decision and its input set without
// checking out upstream.
type rosterManifest struct {
	Source        rosterSource      `json:"source"`
	ShellProvided map[string]string `json:"shellProvided"`
	Entries       []rosterEntry     `json:"entries"`
	Blocked       []rosterBlocked   `json:"blocked"`
	Omitted       []rosterOmitted   `json:"omitted"`
}

type rosterSource struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Version    string `json:"version"`
	Derivation string `json:"derivation"`
}

// rosterEntry is one advertised client entry. Dir is the staged tree path
// (`plugins/<dir>/client.js`) used to resolve the bundle bytes; ID/Inject/
// Immediately are the upstream `dsh.client` declaration and External is the
// scanned set of module requests the factory makes.
type rosterEntry struct {
	Dir         string   `json:"dir"`
	ID          string   `json:"id"`
	Inject      []string `json:"inject"`
	External    []string `json:"external"`
	Immediately bool     `json:"immediately"`
}

// rosterBlocked is a staged entry withheld from the graph. The reason cites the
// evidence, because the only honest way to drop a bundle is to name what the
// graph gains by dropping it.
type rosterBlocked struct {
	Dir    string `json:"dir"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type rosterOmitted struct {
	Dir string `json:"dir"`
	ID  string `json:"id"`
}

// BlockedEntry is the exported view of one withheld bundle.
type BlockedEntry struct {
	Dir    string
	ID     string
	Reason string
}

// bundleRevisionLength matches upstream's shortened sha1: combo, artifact, and
// graph revisions are the first 12 hex characters of a digest
// (packages/client/modules/src/index.ts:199).
const bundleRevisionLength = 12

// parseRoster validates the roster header and returns the decoded manifest.
// plugins and raw are parameters rather than the embedded values so a test can
// drive a synthetic roster against a synthetic tree.
func parseRoster(raw []byte) (*rosterManifest, error) {
	var manifest rosterManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("dshmount: embedded roster is not valid JSON: %w", err)
	}
	if manifest.Source.Revision == "" {
		return nil, fmt.Errorf("dshmount: embedded roster records no upstream revision")
	}
	if len(manifest.Entries) == 0 {
		return nil, fmt.Errorf("dshmount: embedded roster has no client entries")
	}
	if len(manifest.ShellProvided) == 0 {
		return nil, fmt.Errorf("dshmount: embedded roster records no shell-provided modules")
	}
	return &manifest, nil
}

// buildEntries turns a parsed roster into the entries dshboot composes,
// resolving each bundle from the embedded plugin tree and deriving its revision
// from the staged bytes. It fails loudly on any roster the served bytes cannot
// satisfy: this is the "do not serve a shell that cannot boot" gate, and a
// partial roster is exactly the failure it exists to catch.
func buildEntries(plugins fs.FS, manifest *rosterManifest) ([]dshboot.Entry, []BlockedEntry, error) {
	if manifest == nil {
		return nil, nil, fmt.Errorf("dshmount: roster is nil")
	}
	blocked := make(map[string]BlockedEntry, len(manifest.Blocked))
	for _, row := range manifest.Blocked {
		if row.Dir == "" || row.ID == "" || row.Reason == "" {
			return nil, nil, fmt.Errorf("dshmount: blocked roster entry %q needs a dir, an id, and a reason", row.Dir)
		}
		blocked[row.Dir] = BlockedEntry{Dir: row.Dir, ID: row.ID, Reason: row.Reason}
	}

	entries := make([]dshboot.Entry, 0, len(manifest.Entries))
	seenDir := make(map[string]struct{}, len(manifest.Entries))
	seenID := make(map[string]struct{}, len(manifest.Entries))
	for _, row := range manifest.Entries {
		if row.Dir == "" || row.ID == "" {
			return nil, nil, fmt.Errorf("dshmount: roster entry %q needs both a dir and an id", row.Dir)
		}
		if _, duplicate := seenDir[row.Dir]; duplicate {
			return nil, nil, fmt.Errorf("dshmount: roster lists dir %q twice", row.Dir)
		}
		seenDir[row.Dir] = struct{}{}
		if _, duplicate := seenID[row.ID]; duplicate {
			return nil, nil, fmt.Errorf("dshmount: roster lists id %q twice", row.ID)
		}
		seenID[row.ID] = struct{}{}

		if entry, withheld := blocked[row.Dir]; withheld {
			if entry.ID != row.ID {
				return nil, nil, fmt.Errorf("dshmount: blocked entry %q names id %q but the roster entry is %q", row.Dir, entry.ID, row.ID)
			}
			continue
		}

		dir, err := fs.Sub(plugins, row.Dir)
		if err != nil {
			return nil, nil, fmt.Errorf("dshmount: roster entry %q: staged directory %q is not in the embedded plugin tree: %w", row.ID, row.Dir, err)
		}
		bundle, err := fs.ReadFile(dir, "client.js")
		if err != nil {
			return nil, nil, fmt.Errorf("dshmount: roster entry %q: staged plugins/%s/client.js could not be read: %w", row.ID, row.Dir, err)
		}
		entries = append(entries, dshboot.Entry{
			ID:          row.ID,
			Rev:         bundleRevision(bundle),
			Inject:      row.Inject,
			External:    row.External,
			Immediately: row.Immediately,
			Bundle:      bundle,
			FS:          dir,
		})
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("dshmount: every roster entry is blocked; there is nothing to boot")
	}

	// A blocked dir that is not in the advertised list is a roster bug: the
	// exclusion would silently do nothing.
	for dir := range blocked {
		if _, ok := seenDir[dir]; !ok {
			return nil, nil, fmt.Errorf("dshmount: blocked entry %q is not a roster entry", dir)
		}
	}

	// Collect in roster order so the exported list is deterministic.
	blockedList := make([]BlockedEntry, 0, len(manifest.Blocked))
	for _, row := range manifest.Entries {
		if entry, withheld := blocked[row.Dir]; withheld {
			blockedList = append(blockedList, entry)
		}
	}
	return entries, blockedList, nil
}

// loadEntries parses and builds in one step, which is the shape the mount uses.
func loadEntries(plugins fs.FS, raw []byte) ([]dshboot.Entry, []BlockedEntry, error) {
	manifest, err := parseRoster(raw)
	if err != nil {
		return nil, nil, err
	}
	return buildEntries(plugins, manifest)
}

// shellProvidedModules returns the shell-seeded module specifiers in sorted
// order, for diagnostics and for the accounting test that proves every external
// request has a provider.
func shellProvidedModules(manifest *rosterManifest) []string {
	if manifest == nil {
		return nil
	}
	names := make([]string, 0, len(manifest.ShellProvided))
	for name := range manifest.ShellProvided {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// bundleRevision derives one entry's opaque artifact revision from its exact
// bytes. It is a content hash rather than a counter so the advertised URL is
// stable across restarts while still changing whenever the staged bundle does;
// the console treats the value as opaque and only requires it to be present.
func bundleRevision(bundle []byte) string {
	sum := sha1.Sum(bundle)
	return hex.EncodeToString(sum[:])[:bundleRevisionLength]
}
