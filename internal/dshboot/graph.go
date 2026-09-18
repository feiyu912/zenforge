package dshboot

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// ClientModulesID is the bootstrap package whose client bundle supplies the
// module-system implementation. Both the inline queue script and the bootstrap
// batch are pinned to it, so it is a protocol constant rather than a knob.
const ClientModulesID = "@deepseek-ai/dsh-client-modules"

// Initial-load scheduling phases. A bootstrap batch is a parser-blocking script
// in the head; an application batch is an advisory preload that the module
// system fetches when a row materializes.
const (
	PhaseBootstrap   = "bootstrap"
	PhaseApplication = "application"
)

// MaxComboURLBytes bounds one generated combination URL. The console composes
// the batch URL out of package ids, so a roster long enough would overflow
// conservative request-target limits; upstream partitions instead of emitting
// an over-long URL and refuses a single id that cannot fit on its own.
const MaxComboURLBytes = 3 * 1024

// hashRevisionLength is the sha1 prefix length upstream uses for combo, artifact,
// and graph revisions; comboRevisionPlaceholder stands in for the real revision
// while partitioning, because the URL shape is what must fit, not the hash value.
const (
	hashRevisionLength        = 12
	comboRevisionPlaceholder  = "000000000000"
	clientBundleFileInEntryFS = "client.js"
)

// chunkNamePattern is upstream's accepted package-local chunk filename. Anything
// else under an advertised entry path is not a chunk the graph built.
var chunkNamePattern = regexp.MustCompile(`^client\.[A-Za-z0-9][A-Za-z0-9._-]*\.js$`)

// Entry is one client plugin the host advertises in the boot graph. Rev is the
// opaque artifact revision the console uses to key its module cache; pinning one
// revision per built bundle is what lets the host promise immutable bytes for a
// URL it has already sent.
//
// The client.js bytes come from Bundle when it is non-nil, otherwise from FS.
// FS is rooted at the entry directory itself (so a shared tree is addressed with
// fs.Sub), and is the only source for Chunks: Bundle overrides the entry script
// file, not the chunks that file may load.
type Entry struct {
	ID          string
	Rev         string
	Inject      []string
	External    []string
	Immediately bool
	Bundle      []byte
	FS          fs.FS
	Chunks      []string
}

// Graph is the window.__DSH_BOOT__ payload exactly as the console parses it.
// Field order and optional-field omission matter only as JSON stability, since
// the wire is validated by shape, but the graph revision is derived from this
// encoding so it must stay deterministic.
type Graph struct {
	Rev     string       `json:"rev"`
	Entries []GraphEntry `json:"entries"`
	Batches []Batch      `json:"batches"`
}

// GraphEntry is one composed plugin row. URL is the single-package combination
// endpoint the console uses after an HMR invalidation; Inject carries rows whose
// factories must arrive first, Immediately marks the stage-one prefetch tier, and
// External names module-table specifiers this row requests.
type GraphEntry struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Rev         string   `json:"rev"`
	Inject      []string `json:"inject,omitempty"`
	Immediately bool     `json:"immediately,omitempty"`
	External    []string `json:"external,omitempty"`
}

// Batch is one initial combination script. Every graph entry belongs to exactly
// one batch, so the console can fetch every factory from the URL that batch
// advertises before the shell reads the graph.
type Batch struct {
	Phase   string   `json:"phase"`
	URL     string   `json:"url"`
	Rev     string   `json:"rev"`
	Entries []string `json:"entries"`
}

// ComboURL addresses an ordered plugin-file list through the shared combination
// route. The rev= parameter is never omitted: the console throws on a bundle URL
// with no revision, so the builder emits it even for an empty revision, while
// BuildGraph refuses an empty entry revision before it can reach a URL.
func ComboURL(ids []string, rev string) string {
	return comboURL(ids, rev, false)
}

// ChunkURL addresses one package-local chunk under the same revision as its
// entry. The revision query is mandatory here for the same reason as ComboURL.
func ChunkURL(id, fileName, rev string) string {
	return "/plugins/" + id + "/" + fileName + "?rev=" + rev
}

// comboURL renders the combination route, optionally in the .map spelling used
// to project the longer form of the URL when partitioning.
func comboURL(ids []string, rev string, sourceMap bool) string {
	resources := make([]string, len(ids))
	for i, id := range ids {
		name := id + "/client.js"
		if sourceMap {
			name += ".map"
		}
		resources[i] = name
	}
	return "/plugins/??" + strings.Join(resources, ",") + "&rev=" + rev
}

// BuildGraph composes the boot graph from entries and validates it before
// returning, so a caller can never serve a graph the console would reject at
// boot. Entries are ordered so a requested dynamic package precedes its
// consumers; the bootstrap package, when present, is scheduled as its own
// parser-blocking batch and every other entry is partitioned into application
// batches that fit the combination URL limit.
func BuildGraph(entries []Entry) (*Graph, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("dshboot: boot graph requires at least one entry")
	}
	rows := make([]GraphEntry, 0, len(entries))
	revs := make(map[string]string, len(entries))
	var bootstrapIDs []string
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			return nil, fmt.Errorf("dshboot: boot graph entry is missing an id")
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return nil, fmt.Errorf("dshboot: duplicate entry id %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		if entry.Rev == "" {
			return nil, fmt.Errorf("dshboot: entry %q is missing a revision", entry.ID)
		}
		chunks := make(map[string]struct{}, len(entry.Chunks))
		for _, name := range entry.Chunks {
			if !chunkNamePattern.MatchString(name) {
				return nil, fmt.Errorf("dshboot: entry %q chunk %q is not a client.<hash>.js name", entry.ID, name)
			}
			if _, duplicate := chunks[name]; duplicate {
				return nil, fmt.Errorf("dshboot: entry %q declares chunk %q twice", entry.ID, name)
			}
			chunks[name] = struct{}{}
		}
		revs[entry.ID] = entry.Rev
		rows = append(rows, GraphEntry{
			ID:          entry.ID,
			URL:         ComboURL([]string{entry.ID}, entry.Rev),
			Rev:         entry.Rev,
			Inject:      cloneStrings(entry.Inject),
			Immediately: entry.Immediately,
			External:    cloneStrings(entry.External),
		})
		if entry.ID == ClientModulesID {
			bootstrapIDs = append(bootstrapIDs, entry.ID)
		}
	}

	ordered, err := orderByModuleGraph(rows)
	if err != nil {
		return nil, err
	}
	application := make([]string, 0, len(ordered))
	for _, row := range ordered {
		if row.ID == ClientModulesID {
			continue
		}
		application = append(application, row.ID)
	}

	bootstrapBatches, err := partitionEntryIDs(bootstrapIDs)
	if err != nil {
		return nil, err
	}
	applicationBatches, err := partitionEntryIDs(application)
	if err != nil {
		return nil, err
	}
	batches := make([]Batch, 0, len(bootstrapBatches)+len(applicationBatches))
	for _, ids := range bootstrapBatches {
		batches = append(batches, newBatch(PhaseBootstrap, ids, revs))
	}
	for _, ids := range applicationBatches {
		batches = append(batches, newBatch(PhaseApplication, ids, revs))
	}

	graph := &Graph{Rev: graphRevision(ordered, batches), Entries: ordered, Batches: batches}
	if err := ValidateGraph(graph); err != nil {
		// BuildGraph composes only valid graphs, so a failure here is a composer
		// regression. Surfacing it anyway keeps the package's promise — never
		// emit a graph the console would reject — true even then.
		return nil, err
	}
	return graph, nil
}

// ValidateGraph applies the console's own strict boot-manifest rules before a
// graph is serialized: unique entry ids, a known phase and a unique URL per
// batch, every batch naming known entries, and every entry landing in exactly
// one batch. Failures name the offending id, URL, or phase so a bad roster is
// diagnosable from the error alone.
func ValidateGraph(graph *Graph) error {
	if graph == nil {
		return fmt.Errorf("dshboot: boot graph is nil")
	}
	if graph.Rev == "" {
		return fmt.Errorf("dshboot: boot graph rev must be a non-empty string")
	}
	known := make(map[string]struct{}, len(graph.Entries))
	for _, entry := range graph.Entries {
		if entry.ID == "" {
			return fmt.Errorf("dshboot: boot graph entry is missing an id")
		}
		if _, duplicate := known[entry.ID]; duplicate {
			return fmt.Errorf("dshboot: duplicate entry id %q", entry.ID)
		}
		known[entry.ID] = struct{}{}
		if entry.URL == "" {
			return fmt.Errorf("dshboot: entry %q must carry a url", entry.ID)
		}
		if entry.Rev == "" {
			return fmt.Errorf("dshboot: entry %q must carry a rev", entry.ID)
		}
	}

	batched := make(map[string]string, len(graph.Entries))
	batchURLs := make(map[string]struct{}, len(graph.Batches))
	for _, batch := range graph.Batches {
		if batch.Phase != PhaseBootstrap && batch.Phase != PhaseApplication {
			return fmt.Errorf("dshboot: batch phase %q must be %q or %q", batch.Phase, PhaseBootstrap, PhaseApplication)
		}
		if batch.URL == "" {
			return fmt.Errorf("dshboot: %s batch must carry a url", batch.Phase)
		}
		if batch.Rev == "" {
			return fmt.Errorf("dshboot: %s batch must carry a rev", batch.Phase)
		}
		if _, duplicate := batchURLs[batch.URL]; duplicate {
			return fmt.Errorf("dshboot: duplicate batch URL %q", batch.URL)
		}
		batchURLs[batch.URL] = struct{}{}
		if len(batch.Entries) == 0 {
			return fmt.Errorf("dshboot: %s batch entries must be non-empty", batch.Phase)
		}
		for _, id := range batch.Entries {
			if _, ok := known[id]; !ok {
				return fmt.Errorf("dshboot: %s batch names unknown entry %q", batch.Phase, id)
			}
			previous, duplicate := batched[id]
			if !duplicate {
				batched[id] = batch.URL
				continue
			}
			if previous == batch.URL {
				return fmt.Errorf("dshboot: %s batch lists entry %q twice", batch.Phase, id)
			}
			return fmt.Errorf("dshboot: entry %q belongs to more than one batch (%q and %q)", id, previous, batch.URL)
		}
	}
	for _, entry := range graph.Entries {
		if _, ok := batched[entry.ID]; !ok {
			return fmt.Errorf("dshboot: entry %q belongs to no initial-load batch", entry.ID)
		}
	}
	return nil
}

// orderByModuleGraph orders rows so every requested dynamic package precedes its
// consumers. An External specifier either names a graph row (the <pkg>/client
// spelling aliases the bare package) or a static-table module that adds no edge.
// Scan order breaks every tie, which is what keeps composition deterministic.
func orderByModuleGraph(rows []GraphEntry) ([]GraphEntry, error) {
	byID := make(map[string]GraphEntry, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]GraphEntry, 0, len(rows))
	placed := make(map[string]struct{}, len(rows))
	var open []string

	var visit func(row GraphEntry) error
	visit = func(row GraphEntry) error {
		if _, done := placed[row.ID]; done {
			return nil
		}
		for i, id := range open {
			if id != row.ID {
				continue
			}
			cycle := append(append([]string(nil), open[i:]...), row.ID)
			return fmt.Errorf(
				"dshboot: module graph cycle %s: a requested package row must precede its consumers",
				strings.Join(cycle, " -> "),
			)
		}
		open = append(open, row.ID)
		for _, name := range row.External {
			dependency, ok := byID[name]
			if !ok {
				dependency, ok = byID[stripClientSuffix(name)]
			}
			if !ok {
				continue
			}
			if dependency.ID == row.ID {
				return fmt.Errorf("dshboot: entry %q requests module %q that it answers itself", row.ID, name)
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		open = open[:len(open)-1]
		placed[row.ID] = struct{}{}
		ordered = append(ordered, row)
		return nil
	}

	for _, row := range rows {
		if err := visit(row); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// partitionEntryIDs splits one phase in graph order without emitting a URL above
// the protocol limit. An entry that cannot fit alone is an error rather than a
// silently oversized URL.
func partitionEntryIDs(ids []string) ([][]string, error) {
	var batches [][]string
	current := make([]string, 0, len(ids))
	for _, id := range ids {
		candidate := append(append([]string(nil), current...), id)
		if len(comboURL(candidate, comboRevisionPlaceholder, true)) <= MaxComboURLBytes {
			current = candidate
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("dshboot: entry %q exceeds the %d-byte combo URL limit", id, MaxComboURLBytes)
		}
		batches = append(batches, current)
		current = []string{id}
		if len(comboURL(current, comboRevisionPlaceholder, true)) > MaxComboURLBytes {
			return nil, fmt.Errorf("dshboot: entry %q exceeds the %d-byte combo URL limit", id, MaxComboURLBytes)
		}
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches, nil
}

// newBatch derives one batch revision from its ordered member revisions, so two
// batches with different members never share a URL even in the same phase.
func newBatch(phase string, ids []string, revs map[string]string) Batch {
	rev := comboRevision(ids, revs)
	return Batch{
		Phase:   phase,
		URL:     ComboURL(ids, rev),
		Rev:     rev,
		Entries: append([]string(nil), ids...),
	}
}

// comboRevision hashes the ordered id/revision pairs of one batch. It is derived,
// never supplied: a caller-supplied batch revision would let two different byte
// sets share a URL the console caches immutably.
func comboRevision(ids []string, revs map[string]string) string {
	parts := make([]string, 0, len(ids)*2)
	for _, id := range ids {
		parts = append(parts, id, revs[id])
	}
	return framedHash("combo", parts...)
}

// graphRevision anchors the whole wire. The console treats it as opaque, so it
// only has to change whenever the entries or batches change; hashing the same
// two fields upstream hashes keeps it stable across recomposition with identical
// inputs.
func graphRevision(entries []GraphEntry, batches []Batch) string {
	payload, err := json.Marshal(struct {
		Entries []GraphEntry `json:"entries"`
		Batches []Batch      `json:"batches"`
	}{Entries: entries, Batches: batches})
	if err != nil {
		// Unreachable: the wire graph is plain strings, bools, and slices. An
		// empty revision fails ValidateGraph, so a composer regression is loud
		// rather than a graph the console silently rejects.
		return ""
	}
	return shortHash(payload)
}

// framedHash hashes several values without letting bytes move across field
// boundaries, mirroring upstream's length-prefixed digest.
func framedHash(domain string, parts ...string) string {
	digest := sha1.New()
	digest.Write([]byte(domain))
	digest.Write([]byte{0})
	for _, part := range parts {
		fmt.Fprintf(digest, "%d:", len(part))
		digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))[:hashRevisionLength]
}

func shortHash(payload []byte) string {
	sum := sha1.Sum(payload)
	return hex.EncodeToString(sum[:])[:hashRevisionLength]
}

// stripClientSuffix normalizes the <pkg>/client specifier plugin bundles emit
// onto the bare package row that owns it.
func stripClientSuffix(specifier string) string {
	return strings.TrimSuffix(specifier, "/client")
}

// cloneStrings copies a declaration so the returned graph cannot alias, and
// therefore mutate, the caller's slice.
func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
