package cli

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/feiyu912/zenforge/internal/dshapi"
)

// The console's `@` picker asks for path candidates as the operator types. The
// reference resolves them from a per-workspace provider
// (`@deepseek-ai/dsh-file-reference-local`) whose behavior is fully specified, and
// this file is that behavior over the one directory this host serves (ADR 0134):
// a query with no slash searches the whole tree for ranked matches, a query with
// a slash (or the empty query) lists one directory's entries, and both answers are
// bounded and deterministic.
const (
	// fileReferenceMaxResults and fileReferenceMaxEntries are the reference
	// provider's own defaults (`maxResults: 20`, `maxEntries: 50000`).
	fileReferenceMaxResults = 20
	fileReferenceMaxEntries = 50000
	// fileReferenceExcludeDepth bounds how deep the walk goes. The reference walks
	// its whole tree under the entry budget alone; this host also caps depth so a
	// pathological tree cannot make a picker request linger.
	fileReferenceExcludeDepth = 64
)

// fileReferenceExcludedDirectories is the reference provider's default skip list:
// build and dependency trees a person never means to reference.
var fileReferenceExcludedDirectories = []string{
	".git", "node_modules", "dist", "build", "out", "coverage", "target",
	".next", ".nuxt", ".turbo", ".venv", "__pycache__", ".cache", ".idea",
	".gradle", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".tox", ".yarn",
}

// consoleFileReferences builds the `@` candidate source over the directory this
// server serves. A workspace that cannot be opened is nil, so the namespace
// answers `unimplemented` with the dependency named instead of an empty menu that
// looks like an empty directory.
func consoleFileReferences(root string) dshapi.FileReferenceSource {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &consoleFileReferenceSource{
		root:     filepath.ToSlash(absolute),
		excluded: fileReferenceExcludedSet(),
	}
}

// consoleFileReferenceSource answers candidate queries from one workspace root.
type consoleFileReferenceSource struct {
	root     string
	excluded map[string]bool
}

// FileReferenceCandidates implements dshapi.FileReferenceSource. A read that
// fails answers no candidates, which is the reference's own behavior for an
// unreadable directory: the picker shows nothing rather than an error the
// operator cannot act on.
func (s *consoleFileReferenceSource) FileReferenceCandidates(ctx context.Context, _ string, query string) ([]dshapi.FileReference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Windows-style separators are normalized the way the reference does, so a
	// caller that types backslashes sees the same candidates as one that types
	// slashes.
	query = strings.ReplaceAll(query, "\\", "/")
	if slash := strings.LastIndex(query, "/"); query == "" || slash >= 0 {
		directory, fragment := "", ""
		if slash >= 0 {
			directory, fragment = query[:slash+1], query[slash+1:]
		}
		return s.listDirectory(ctx, directory, fragment)
	}
	return s.searchTree(ctx, query)
}

// listDirectory lists one directory's immediate entries, ranked by the fragment
// that follows the last slash. Hidden entries appear only when the fragment itself
// asks for them, and a path that escapes the workspace or traverses a symlink
// answers nothing -- the reference refuses both rather than following them out.
func (s *consoleFileReferenceSource) listDirectory(ctx context.Context, directory, fragment string) ([]dshapi.FileReference, error) {
	for _, segment := range strings.Split(strings.TrimSuffix(directory, "/"), "/") {
		if s.excluded[segment] {
			return []dshapi.FileReference{}, nil
		}
	}
	absolute, ok := s.resolveDirectory(directory)
	if !ok {
		return []dshapi.FileReference{}, nil
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return []dshapi.FileReference{}, nil
	}
	candidates := make([]dshapi.FileReference, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(fragment, ".") {
			continue
		}
		switch {
		case entry.IsDir():
			if s.excluded[name] {
				continue
			}
			candidates = append(candidates, dshapi.FileReference{Path: directory + name, Kind: "directory"})
		case entry.Type().IsRegular():
			candidates = append(candidates, dshapi.FileReference{Path: directory + name, Kind: "file"})
		}
	}
	return rankFileReferences(candidates, fragment), nil
}

// searchTree ranks a whole-tree match for a query with no slash. Hidden files are
// out of the index unless the query itself mentions a dot (the reference's
// `visibleForGlobalQuery`), and the walk stops at the entry budget so one request
// cannot traverse an enormous tree.
func (s *consoleFileReferenceSource) searchTree(ctx context.Context, query string) ([]dshapi.FileReference, error) {
	candidates := make([]dshapi.FileReference, 0, fileReferenceMaxResults*4)
	type frame struct {
		absolute string
		relative string
		depth    int
	}
	queue := []frame{{absolute: s.root, relative: "", depth: 0}}
	for cursor := 0; cursor < len(queue) && len(candidates) < fileReferenceMaxEntries; cursor++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[cursor]
		if current.depth >= fileReferenceExcludeDepth {
			continue
		}
		entries, err := os.ReadDir(current.absolute)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if len(candidates) >= fileReferenceMaxEntries {
				break
			}
			name := entry.Name()
			relative := name
			if current.relative != "" {
				relative = current.relative + "/" + name
			}
			switch {
			case entry.IsDir():
				if s.excluded[name] {
					continue
				}
				candidates = append(candidates, dshapi.FileReference{Path: relative, Kind: "directory"})
				queue = append(queue, frame{absolute: filepath.Join(current.absolute, name), relative: relative, depth: current.depth + 1})
			case entry.Type().IsRegular():
				candidates = append(candidates, dshapi.FileReference{Path: relative, Kind: "file"})
			}
		}
	}
	visible := candidates[:0]
	for _, candidate := range candidates {
		if visibleForGlobalQuery(candidate.Path, query) {
			visible = append(visible, candidate)
		}
	}
	return rankFileReferences(visible, query), nil
}

// resolveDirectory maps a display directory to an absolute one, refusing anything
// that leaves the root and any component that is not a real directory: a symlink
// is not followed, because a picker that shows a path outside the workspace would
// promise a reference this host cannot read.
func (s *consoleFileReferenceSource) resolveDirectory(directory string) (string, bool) {
	trimmed := strings.Trim(directory, "/")
	if trimmed == "" {
		return s.root, true
	}
	if trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.Contains(trimmed, "/../") ||
		strings.HasSuffix(trimmed, "/..") || path.IsAbs(trimmed) {
		return "", false
	}
	current := s.root
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", false
		}
	}
	return current, true
}

// rankFileReferences ports the reference provider's ranking, which is what makes
// the first row the one the operator meant: an exact name beats a prefix, a prefix
// beats a name substring, a name substring beats a path substring, and a
// subsequence match comes last. Directories win ties, then shorter paths, then
// lexicographic order -- so the answer is deterministic and stable between
// keystrokes.
func rankFileReferences(candidates []dshapi.FileReference, query string) []dshapi.FileReference {
	ranked := make([]dshapi.FileReference, 0, len(candidates))
	scores := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		score, ok := scoreFileReference(candidate, query)
		if !ok {
			continue
		}
		ranked = append(ranked, candidate)
		scores[candidate.Path] = score
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		first, second := ranked[left], ranked[right]
		if scores[first.Path] != scores[second.Path] {
			return scores[first.Path] > scores[second.Path]
		}
		if first.Kind != second.Kind {
			return first.Kind == "directory"
		}
		if query != "" && len(first.Path) != len(second.Path) {
			return len(first.Path) < len(second.Path)
		}
		return first.Path < second.Path
	})
	if len(ranked) > fileReferenceMaxResults {
		ranked = ranked[:fileReferenceMaxResults]
	}
	return ranked
}

// scoreFileReference is the reference's `scoreCandidate`: the same five bands and
// the same 25-point directory bonus, so a directory and a file that match equally
// well are ordered the way the reference orders them.
func scoreFileReference(candidate dshapi.FileReference, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	lowered := strings.ToLower(candidate.Path)
	name := lowered
	if slash := strings.LastIndex(lowered, "/"); slash >= 0 {
		name = lowered[slash+1:]
	}
	needle := strings.ToLower(query)
	bonus := 0
	if candidate.Kind == "directory" {
		bonus = 25
	}
	switch {
	case name == needle:
		return 1000 + bonus, true
	case strings.HasPrefix(name, needle):
		return 900 + bonus, true
	case strings.Contains(name, needle):
		return 700 + bonus, true
	case strings.Contains(lowered, needle):
		return 500 + bonus, true
	}
	if subsequence, ok := subsequenceScore(lowered, needle); ok {
		return 300 + subsequence + bonus, true
	}
	return 0, false
}

// subsequenceScore scores a fuzzy match: every character of the query appears in
// order, and the fewer characters skipped between them, the better. File names are
// often typed as initials, which is why this band exists at all.
func subsequenceScore(target, query string) (int, bool) {
	index, gap := 0, 0
	for _, character := range query {
		found := strings.IndexRune(target[index:], character)
		if found < 0 {
			return 0, false
		}
		gap += found
		index += found + 1
	}
	if gap > 100 {
		gap = 100
	}
	return 100 - gap, true
}

// visibleForGlobalQuery hides any path with a hidden segment unless the query
// itself mentions a dot, which is how a person reaches `.github` by asking for it.
func visibleForGlobalQuery(candidate, query string) bool {
	if strings.HasPrefix(query, ".") || strings.Contains(query, "/.") {
		return true
	}
	for _, segment := range strings.Split(candidate, "/") {
		if strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}

// fileReferenceExcludedSet is the skip list as a set.
func fileReferenceExcludedSet() map[string]bool {
	excluded := make(map[string]bool, len(fileReferenceExcludedDirectories))
	for _, name := range fileReferenceExcludedDirectories {
		excluded[name] = true
	}
	return excluded
}

// fileReferenceDebug renders a candidate list for a log line or a test failure.
func fileReferenceDebug(candidates []dshapi.FileReference) string {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		parts = append(parts, fmt.Sprintf("%s (%s)", candidate.Path, candidate.Kind))
	}
	return strings.Join(parts, ", ")
}
