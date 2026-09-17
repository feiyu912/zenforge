package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	workspacepkg "github.com/feiyu912/zenforge/workspace"
)

// Search discovery limits follow the DSH tool-fs-search suite: glob
// keeps the first 100 paths inline in modification-time order, grep
// keeps the first 250 matches with 2000-byte line previews, and an
// over-cap result carries a footer that points at the complete saved
// list when a spill store is configured.
const (
	DefaultGlobMaxResults   = 100
	DefaultGrepMaxMatches   = 250
	DefaultGrepMaxLineBytes = 2000

	defaultGlobMaxVisited = 20000
	defaultGlobMaxDepth   = 64
)

// globVCSExcludes are directory names glob never descends into,
// matching DSH GLOB_VCS_EXCLUDES.
var globVCSExcludes = map[string]struct{}{
	".git": {}, ".svn": {}, ".hg": {}, ".bzr": {}, ".jj": {}, ".sl": {},
}

// SearchSpill saves complete formatted search results when an inline cap
// is exceeded. *tool.SpillStore satisfies this interface; the CLI wires
// the same store the tool.Spill middleware uses.
type SearchSpill interface {
	SaveText(suggestedName, content string) (string, error)
}

type globInput struct {
	Pattern string `json:"pattern" jsonschema:"required,description=Glob pattern matched against workspace-relative file paths (e.g. **/*.go). A pattern without a slash matches basenames at any depth so *.go searches the whole tree"`
	Path    string `json:"path,omitempty" jsonschema:"description=Workspace-relative directory to search in; defaults to the workspace root"`
}

type globOutput struct {
	Paths  []string `json:"paths"`
	Total  int      `json:"total"`
	Footer string   `json:"footer,omitempty"`
}

// Glob returns the workspace_glob discovery tool: recursive pattern
// matching over workspace-relative paths, newest-first, capped with a
// DSH-style footer and optional full-list spill.
func Glob(config Config) (tool.Tool, error) {
	if config.Workspace == nil {
		return nil, fmt.Errorf("%w: workspace is nil", tool.ErrInvalidTool)
	}
	base, err := tools.New("workspace_glob", "Find files in the configured workspace whose paths match a glob pattern. Returns matching file paths — never directories — including hidden files, newest first.", func(ctx context.Context, in globInput) (globOutput, error) {
		matcher, err := compileGlob(in.Pattern)
		if err != nil {
			return globOutput{}, err
		}
		start := strings.TrimSpace(in.Path)
		if start == "" {
			start = "."
		}
		maxVisited := config.GlobMaxVisited
		if maxVisited <= 0 {
			maxVisited = defaultGlobMaxVisited
		}
		budget := globBudget{maxVisited: maxVisited}
		var files []workspacepkg.FileInfo
		if err := globWalk(ctx, config.Workspace, start, 0, &budget, matcher, &files); err != nil {
			return globOutput{}, err
		}
		sort.Slice(files, func(i, j int) bool {
			if files[i].ModTime != files[j].ModTime {
				return files[i].ModTime > files[j].ModTime
			}
			return files[i].Path < files[j].Path
		})
		capResults := config.GlobMaxResults
		if capResults <= 0 {
			capResults = DefaultGlobMaxResults
		}
		total := len(files)
		shown := files
		if total > capResults {
			shown = files[:capResults]
		}
		paths := make([]string, 0, len(shown))
		for _, file := range shown {
			paths = append(paths, file.Path)
		}
		out := globOutput{Paths: paths, Total: total}
		if total <= capResults {
			return out, nil
		}
		if config.SearchSpill != nil {
			var formatted strings.Builder
			for _, file := range files {
				fmt.Fprintf(&formatted, "%s\n", file.Path)
			}
			sum := sha256.Sum256([]byte(in.Pattern + "\x00" + start))
			name := fmt.Sprintf("glob-%s.txt", hex.EncodeToString(sum[:8]))
			if spillPath, saveErr := config.SearchSpill.SaveText(name, formatted.String()); saveErr == nil {
				out.Footer = fmt.Sprintf("Found %d files; returning the first %d in modification-time order (newest first). The complete sorted list was saved to %s.", total, capResults, spillPath)
				return out, nil
			}
			// A failed spill degrades to the plain footer, never to a
			// failed discovery call.
		}
		out.Footer = fmt.Sprintf("Found %d files; returning the first %d in modification-time order (newest first); %d more omitted — narrow the pattern or path.", total, capResults, total-capResults)
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return withFilePolicy(base, config.Policy, policy.FileList), nil
}

type globBudget struct {
	maxVisited int
	visited    int
}

func globWalk(ctx context.Context, ws workspacepkg.Workspace, dir string, depth int, budget *globBudget, matcher func(string) bool, out *[]workspacepkg.FileInfo) error {
	if depth >= defaultGlobMaxDepth || budget.visited >= budget.maxVisited {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := ws.List(ctx, dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if budget.visited >= budget.maxVisited {
			return nil
		}
		budget.visited++
		if entry.IsDir {
			if _, skip := globVCSExcludes[path.Base(entry.Path)]; skip {
				continue
			}
			if err := globWalk(ctx, ws, entry.Path, depth+1, budget, matcher, out); err != nil {
				return err
			}
			continue
		}
		if matcher(entry.Path) {
			*out = append(*out, entry)
		}
	}
	return nil
}

// compileGlob validates a model-supplied pattern and returns a matcher
// over workspace-relative slash paths. Patterns without a slash match
// basenames at any depth, following the DSH glob contract; "**" spans
// directory boundaries everywhere else. Path syntax is rejected before
// any filesystem access.
func compileGlob(pattern string) (func(string) bool, error) {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: pattern is required", tool.ErrInvalidArguments)
	}
	if strings.HasPrefix(trimmed, "/") || strings.ContainsRune(trimmed, '\\') || strings.ContainsRune(trimmed, 0) {
		return nil, fmt.Errorf("%w: pattern must be a workspace-relative slash pattern", tool.ErrInvalidArguments)
	}
	segments := strings.Split(trimmed, "/")
	for _, segment := range segments {
		if segment == ".." || segment == "." {
			return nil, fmt.Errorf("%w: pattern may not contain path traversal", tool.ErrInvalidArguments)
		}
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, "probe"); err != nil {
			return nil, fmt.Errorf("%w: bad glob pattern: %v", tool.ErrInvalidArguments, err)
		}
	}
	if len(segments) == 1 {
		segment := segments[0]
		return func(candidate string) bool {
			if segment == "**" {
				return true
			}
			ok, err := path.Match(segment, path.Base(candidate))
			return err == nil && ok
		}, nil
	}
	return func(candidate string) bool {
		return globMatchSegments(segments, strings.Split(candidate, "/"))
	}, nil
}

func globMatchSegments(pattern, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(parts); i++ {
			if globMatchSegments(pattern[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], parts[0])
	if err != nil || !ok {
		return false
	}
	return globMatchSegments(pattern[1:], parts[1:])
}

// truncatePreview cuts a matched line to max bytes on a rune boundary,
// mirroring DSH previewLine.
func truncatePreview(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	out := text[:max]
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out
}
