// Package instructions discovers hierarchical agent instruction files
// (AGENTS.md and compatible names) from the project root down to the
// working directory and merges them into one model-visible block.
//
// The design merges the two reference harnesses:
//
//   - openai/codex agents_md.rs: walk up to the nearest project root
//     marker (default ".git"), search every directory root-first, take the
//     first existing candidate filename per directory, concatenate under a
//     byte budget, and reject fallback filenames containing path syntax
//     before any filesystem probe.
//   - deepseek-harness dsh-agent-instructions: broad-to-specific
//     precedence where more specific files win conflicts, an optional
//     user-level global file, a maximum source size that ignores oversized
//     files instead of truncating them blindly, and a budget policy that
//     drops whole broader files before truncating the most specific one,
//     with explicit warnings naming what was omitted.
//
// Discovery is read-only and fail-open per file: an unreadable candidate
// becomes a warning, never an error, so a permissions oddity deep in a
// repository cannot block a run. Invalid configuration fails loudly.
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Defaults follow the reference harnesses: a 32 KiB merged budget (codex
// project_doc_max_bytes) and a 1 MiB per-file source cap (DSH
// maxSourceBytes).
const (
	DefaultMaxBytes       = 32 * 1024
	DefaultMaxSourceBytes = 1024 * 1024
)

// DefaultRootMarkers locate the project root. A nil RootMarkers config
// uses these; an explicitly empty (non-nil) list disables parent
// traversal so only the working directory itself is searched.
var DefaultRootMarkers = []string{".git"}

// DefaultFileNames are the per-directory candidates in precedence order.
// AGENTS.override.md mirrors codex's override file; AGENTS.md is the
// cross-tool standard; ZENFORGE.md and CLAUDE.md are compatibility names.
var DefaultFileNames = []string{"AGENTS.override.md", "AGENTS.md", "ZENFORGE.md", "CLAUDE.md"}

// Config controls discovery. The zero value is valid and uses defaults.
type Config struct {
	// RootMarkers are file or directory names that identify the project
	// root. Nil means DefaultRootMarkers; an empty non-nil slice disables
	// traversal above the working directory.
	RootMarkers []string
	// FileNames are candidate instruction filenames per directory, first
	// existing wins. Nil means DefaultFileNames. Names containing path
	// syntax are rejected before any filesystem access.
	FileNames []string
	// GlobalPath is an optional user-level instruction file (for example
	// ~/.zenforge/AGENTS.md). It is the broadest scope and is skipped
	// silently when missing.
	GlobalPath string
	// MaxBytes bounds the merged rendered content. Zero means default.
	MaxBytes int
	// MaxSourceBytes ignores any single file larger than this. Zero means
	// default.
	MaxSourceBytes int
}

// Entry is one discovered instruction file.
type Entry struct {
	// Path is the absolute file path.
	Path string
	// Scope is "global" or "project".
	Scope string
	// Content is the file text after any budget truncation.
	Content string
	// Truncated reports whether the budget cut this entry.
	Truncated bool
}

// Loaded is a discovery result. Entries are ordered broad to specific:
// the global file first, then project root down to the working directory.
type Loaded struct {
	// ProjectRoot is the directory carrying the root marker, or the
	// working directory when no marker was found.
	ProjectRoot string
	Entries     []Entry
	Warnings    []string
}

// Empty reports whether nothing was discovered.
func (l Loaded) Empty() bool { return len(l.Entries) == 0 }

// Discover searches from dir upward for the project root, then collects
// instruction files root-first. dir is resolved to an absolute path; a
// missing dir is an error because the caller's working directory itself
// is misconfigured.
func Discover(dir string, config Config) (Loaded, error) {
	if err := validate(config); err != nil {
		return Loaded{}, err
	}
	names := config.FileNames
	if names == nil {
		names = DefaultFileNames
	}
	markers := config.RootMarkers
	if markers == nil {
		markers = DefaultRootMarkers
	}
	maxBytes := config.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	maxSource := config.MaxSourceBytes
	if maxSource == 0 {
		maxSource = DefaultMaxSourceBytes
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Loaded{}, fmt.Errorf("resolve instructions directory: %w", err)
	}
	if info, err := os.Stat(absDir); err != nil || !info.IsDir() {
		return Loaded{}, fmt.Errorf("instructions directory %q is not accessible", absDir)
	}

	var warnings []string
	var entries []Entry
	if config.GlobalPath != "" {
		entry, ok := readSource(config.GlobalPath, "global", maxSource, &warnings)
		if ok {
			entries = append(entries, entry)
		}
	}

	projectRoot, chain := searchChain(absDir, markers)
	for _, current := range chain {
		for _, name := range names {
			candidate := filepath.Join(current, name)
			entry, ok := readSource(candidate, "project", maxSource, &warnings)
			if ok {
				entries = append(entries, entry)
				break // first existing candidate wins per directory
			}
		}
	}

	entries, budgetWarnings := applyBudget(entries, maxBytes, maxSource)
	warnings = append(warnings, budgetWarnings...)
	return Loaded{ProjectRoot: projectRoot, Entries: entries, Warnings: warnings}, nil
}

func validate(config Config) error {
	for _, name := range config.FileNames {
		if err := validateNameComponent("fileNames", name); err != nil {
			return err
		}
	}
	for _, marker := range config.RootMarkers {
		if err := validateNameComponent("rootMarkers", marker); err != nil {
			return err
		}
	}
	if config.MaxBytes < 0 {
		return errors.New("instructions maxBytes must be non-negative")
	}
	if config.MaxSourceBytes < 0 {
		return errors.New("instructions maxSourceBytes must be non-negative")
	}
	if config.GlobalPath != "" && strings.ContainsRune(config.GlobalPath, 0) {
		return errors.New("instructions globalPath must not contain NUL")
	}
	return nil
}

// validateNameComponent rejects path syntax in per-directory candidate
// names before any filesystem probe, mirroring the codex fix that
// fallback filenames must be plain names, never paths.
func validateNameComponent(field, value string) error {
	if value == "" {
		return fmt.Errorf("instructions %s must not contain an empty name", field)
	}
	if strings.ContainsRune(value, 0) || strings.Contains(value, "/") ||
		strings.Contains(value, "\\") || value == "." || value == ".." ||
		strings.Contains(value, ":") {
		return fmt.Errorf("instructions %s entry %q must be a plain filename without path syntax", field, value)
	}
	return nil
}

// searchChain returns the project root and the inclusive root-first
// directory chain down to dir. With traversal disabled (empty markers) or
// no marker found, the chain is dir alone.
func searchChain(dir string, markers []string) (string, []string) {
	root := ""
	if len(markers) > 0 {
		current := dir
		for {
			for _, marker := range markers {
				if _, err := os.Stat(filepath.Join(current, marker)); err == nil {
					root = current
					break
				}
			}
			if root != "" {
				break
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	if root == "" {
		return dir, []string{dir}
	}
	var chain []string
	for current := dir; ; {
		chain = append([]string{current}, chain...)
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return root, chain
}

func readSource(path, scope string, maxSource int, warnings *[]string) (Entry, bool) {
	info, err := os.Stat(path)
	if err != nil {
		if scope == "project" && !os.IsNotExist(err) {
			*warnings = append(*warnings, fmt.Sprintf("skipped %s: %v", path, err))
		}
		return Entry{}, false
	}
	if info.IsDir() {
		return Entry{}, false
	}
	if info.Size() > int64(maxSource) {
		*warnings = append(*warnings, fmt.Sprintf("skipped %s: %d bytes exceeds maxSourceBytes %d", path, info.Size(), maxSource))
		return Entry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("skipped %s: %v", path, err))
		return Entry{}, false
	}
	return Entry{Path: path, Scope: scope, Content: string(data)}, true
}

// applyBudget keeps entries from most specific to broadest while they fit
// in maxBytes. Once an entry no longer fits, it and every broader entry
// are dropped whole — broader scopes lose before specific ones, and
// partial broad fragments are never shown. Truncation on a rune edge
// happens only when the most specific file alone exceeds the budget.
// The returned slice preserves broad-first render order.
func applyBudget(entries []Entry, maxBytes, _ int) ([]Entry, []string) {
	kept := make([]Entry, 0, len(entries))
	var warnings []string
	var omitted []string
	remaining := maxBytes
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		size := len(entry.Content)
		if size <= remaining {
			remaining -= size
			kept = append([]Entry{entry}, kept...)
			continue
		}
		if len(kept) == 0 && remaining > 0 {
			entry.Content = truncateUTF8(entry.Content, remaining)
			entry.Truncated = true
			warnings = append(warnings, fmt.Sprintf("truncated %s from %d to %d bytes", entry.Path, size, len(entry.Content)))
			kept = append(kept, entry)
			remaining = 0
			continue
		}
		for j := i; j >= 0; j-- {
			omitted = append(omitted, entries[j].Path)
		}
		break
	}
	if len(omitted) > 0 {
		warnings = append([]string{fmt.Sprintf("omitted %s: instruction budget %d bytes exhausted by more specific files", strings.Join(omitted, ", "), maxBytes)}, warnings...)
	}
	return kept, warnings
}

func truncateUTF8(content string, max int) string {
	if len(content) <= max {
		return content
	}
	cut := content[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// Render merges the discovered entries into one model-visible block. The
// framing states the precedence contract: broader scopes first, more
// specific files win conflicts, and direct system or user instructions win
// over all discovered files.
func (l Loaded) Render() string {
	if l.Empty() {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# Project instructions\n\n")
	builder.WriteString("The following instruction files were discovered from the project root down to the working directory, broader scopes first. More specific instructions take precedence over broader ones. They do not override system, developer, or direct user instructions.\n")
	for _, entry := range l.Entries {
		builder.WriteString("\n## ")
		builder.WriteString(entry.Path)
		if entry.Scope == "global" {
			builder.WriteString(" (user-global)")
		}
		if entry.Truncated {
			builder.WriteString(" (truncated to fit the instruction budget)")
		}
		builder.WriteString("\n\n")
		builder.WriteString(strings.TrimSpace(entry.Content))
		builder.WriteString("\n")
	}
	if len(l.Warnings) > 0 {
		builder.WriteString("\n(Instruction discovery warnings: ")
		builder.WriteString(strings.Join(l.Warnings, "; "))
		builder.WriteString(")\n")
	}
	return builder.String()
}
