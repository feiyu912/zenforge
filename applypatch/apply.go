package applypatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FS is the slice of the filesystem the applier needs. The workspace
// tool supplies a path-confined implementation; tests use a map.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Remove(path string) error
	MkdirAll(path string) error
	IsDir(path string) (bool, error)
}

// Result summarizes what a patch changed.
type Result struct {
	Added    []string
	Modified []string
	Deleted  []string
}

// Summary renders the git-style summary the reference tool returns.
func (r Result) Summary() string {
	var builder strings.Builder
	builder.WriteString("Success. Updated the following files:\n")
	for _, path := range r.Added {
		fmt.Fprintf(&builder, "A %s\n", path)
	}
	for _, path := range r.Modified {
		fmt.Fprintf(&builder, "M %s\n", path)
	}
	for _, path := range r.Deleted {
		fmt.Fprintf(&builder, "D %s\n", path)
	}
	return builder.String()
}

// FirstPath reports the path the patch touches first, so a caller can
// attribute the change to a workspace-relative target.
func (r Result) FirstPath() string {
	for _, group := range [][]string{r.Added, r.Modified, r.Deleted} {
		if len(group) > 0 {
			return group[0]
		}
	}
	return ""
}

// Apply applies parsed hunks in order. Paths are resolved by the caller
// (through the FS implementation), so the engine never interprets them
// itself.
func Apply(hunks []Hunk, filesystem FS) (Result, error) {
	var result Result
	if len(hunks) == 0 {
		return result, errors.New("No files were modified.")
	}
	for _, hunk := range hunks {
		switch hunk.Kind {
		case "add":
			if err := filesystem.MkdirAll(filepath.Dir(hunk.Path)); err != nil {
				return result, fmt.Errorf("Failed to write file %s: %w", hunk.Path, err)
			}
			// The reference tool overwrites an existing file rather than
			// refusing the add.
			if err := filesystem.WriteFile(hunk.Path, []byte(hunk.Contents)); err != nil {
				return result, fmt.Errorf("Failed to write file %s: %w", hunk.Path, err)
			}
			result.Added = append(result.Added, hunk.Path)
		case "delete":
			isDir, err := filesystem.IsDir(hunk.Path)
			if err != nil {
				return result, fmt.Errorf("Failed to delete file %s: %w", hunk.Path, err)
			}
			if isDir {
				return result, fmt.Errorf("Failed to delete file %s: path is a directory", hunk.Path)
			}
			if err := filesystem.Remove(hunk.Path); err != nil {
				return result, fmt.Errorf("Failed to delete file %s: %w", hunk.Path, err)
			}
			result.Deleted = append(result.Deleted, hunk.Path)
		case "update":
			original, err := filesystem.ReadFile(hunk.Path)
			if err != nil {
				return result, fmt.Errorf("Failed to read file to update %s: %w", hunk.Path, err)
			}
			updated, err := deriveNewContents(hunk.Path, string(original), hunk.Chunks)
			if err != nil {
				return result, err
			}
			if hunk.MovePath != "" {
				if err := filesystem.MkdirAll(filepath.Dir(hunk.MovePath)); err != nil {
					return result, fmt.Errorf("Failed to write file %s: %w", hunk.MovePath, err)
				}
				if err := filesystem.WriteFile(hunk.MovePath, []byte(updated)); err != nil {
					return result, fmt.Errorf("Failed to write file %s: %w", hunk.MovePath, err)
				}
				if err := filesystem.Remove(hunk.Path); err != nil {
					return result, fmt.Errorf("Failed to remove original %s: %w", hunk.Path, err)
				}
				// The reference summary reports the source path.
				result.Modified = append(result.Modified, hunk.Path)
				continue
			}
			if err := filesystem.WriteFile(hunk.Path, []byte(updated)); err != nil {
				return result, fmt.Errorf("Failed to write file %s: %w", hunk.Path, err)
			}
			result.Modified = append(result.Modified, hunk.Path)
		default:
			return result, fmt.Errorf("unknown patch hunk kind %q", hunk.Kind)
		}
	}
	return result, nil
}

// deriveNewContents applies the chunks to the file body. Line endings
// normalize to LF and the result always ends with a newline, matching
// the reference default mode.
func deriveNewContents(path, contents string, chunks []Chunk) (string, error) {
	originalLines := strings.Split(contents, "\n")
	if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}
	replacements, err := computeReplacements(originalLines, path, chunks)
	if err != nil {
		return "", err
	}
	newLines := applyReplacements(originalLines, replacements)
	if len(newLines) == 0 || newLines[len(newLines)-1] != "" {
		newLines = append(newLines, "")
	}
	return strings.Join(newLines, "\n"), nil
}

type replacement struct {
	start  int
	oldLen int
	lines  []string
}

// computeReplacements mirrors the reference algorithm: a context line
// advances the search cursor, an insert-only chunk appends after the
// final line, and a matched chunk advances past its pattern.
func computeReplacements(originalLines []string, path string, chunks []Chunk) ([]replacement, error) {
	var replacements []replacement
	lineIndex := 0

	for _, chunk := range chunks {
		if chunk.Context != "" {
			if index := seekSequence(originalLines, []string{chunk.Context}, lineIndex, false); index >= 0 {
				lineIndex = index + 1
			} else {
				return nil, fmt.Errorf("Failed to find context '%s' in %s", chunk.Context, path)
			}
		}

		if len(chunk.OldLines) == 0 {
			insertion := len(originalLines)
			if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
				insertion = len(originalLines) - 1
			}
			if chunk.EndOfFile && len(originalLines) > 0 {
				insertion = len(originalLines)
			}
			replacements = append(replacements, replacement{start: insertion, lines: chunk.NewLines})
			continue
		}

		pattern := chunk.OldLines
		newSlice := chunk.NewLines
		found := seekSequence(originalLines, pattern, lineIndex, chunk.EndOfFile)
		if found < 0 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			// The trailing empty element represents the file's final
			// newline; retry without it so end-of-file edits locate.
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}
			found = seekSequence(originalLines, pattern, lineIndex, chunk.EndOfFile)
		}
		if found < 0 {
			return nil, fmt.Errorf("Failed to find expected lines in %s:\n%s", path, strings.Join(chunk.OldLines, "\n"))
		}
		replacementLines := make([]string, len(newSlice))
		copy(replacementLines, newSlice)
		replacements = append(replacements, replacement{start: found, oldLen: len(pattern), lines: replacementLines})
		lineIndex = found + len(pattern)
	}

	sort.SliceStable(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })
	return replacements, nil
}

// applyReplacements applies replacements back to front so earlier edits
// do not shift later positions.
func applyReplacements(lines []string, replacements []replacement) []string {
	for i := len(replacements) - 1; i >= 0; i-- {
		current := replacements[i]
		for removed := 0; removed < current.oldLen; removed++ {
			if current.start < len(lines) {
				lines = append(lines[:current.start], lines[current.start+1:]...)
			}
		}
		tail := append([]string(nil), lines[current.start:]...)
		lines = append(lines[:current.start], current.lines...)
		lines = append(lines, tail...)
	}
	return lines
}

// seekSequence finds pattern within lines at or after start, trying
// progressively looser matches: exact, trailing whitespace, surrounding
// whitespace, then Unicode punctuation normalization. When eof is set
// the search begins at the end of the file so an end-anchored pattern
// matches there.
func seekSequence(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}
	searchStart := start
	if eof && len(lines) >= len(pattern) {
		searchStart = len(lines) - len(pattern)
	}
	last := len(lines) - len(pattern)

	for index := searchStart; index <= last; index++ {
		if equalLines(lines[index:index+len(pattern)], pattern, compareExact) {
			return index
		}
	}
	for index := searchStart; index <= last; index++ {
		if equalLines(lines[index:index+len(pattern)], pattern, compareTrimRight) {
			return index
		}
	}
	for index := searchStart; index <= last; index++ {
		if equalLines(lines[index:index+len(pattern)], pattern, compareTrim) {
			return index
		}
	}
	for index := searchStart; index <= last; index++ {
		if equalLines(lines[index:index+len(pattern)], pattern, compareNormalized) {
			return index
		}
	}
	return -1
}

type compareMode int

const (
	compareExact compareMode = iota
	compareTrimRight
	compareTrim
	compareNormalized
)

func equalLines(window, pattern []string, mode compareMode) bool {
	for index, want := range pattern {
		have := window[index]
		switch mode {
		case compareExact:
			if have != want {
				return false
			}
		case compareTrimRight:
			if strings.TrimRight(have, " \t") != strings.TrimRight(want, " \t") {
				return false
			}
		case compareTrim:
			if strings.TrimSpace(have) != strings.TrimSpace(want) {
				return false
			}
		case compareNormalized:
			if normalizePunctuation(have) != normalizePunctuation(want) {
				return false
			}
		}
	}
	return true
}

// normalizePunctuation maps typographic dashes, quotes, and spaces to
// their ASCII equivalents, mirroring `git apply`'s tolerance.
func normalizePunctuation(line string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			return '-'
		case '\u2018', '\u2019', '\u201a', '\u201b':
			return '\''
		case '\u201c', '\u201d', '\u201e', '\u201f':
			return '"'
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007',
			'\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
			return ' '
		default:
			return r
		}
	}, strings.TrimSpace(line))
}

// LocalFS is the OS-backed FS used when a patch runs without the
// workspace abstraction.
type LocalFS struct{}

func (LocalFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (LocalFS) WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func (LocalFS) Remove(path string) error { return os.Remove(path) }

func (LocalFS) MkdirAll(path string) error {
	if path == "" || path == "." {
		return nil
	}
	return os.MkdirAll(path, 0o755)
}

func (LocalFS) IsDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return false, err
	}
	return info.IsDir(), nil
}
