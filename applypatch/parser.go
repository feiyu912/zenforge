// Package applypatch ports codex's apply_patch envelope format: a text
// patch with `*** Begin Patch` / `*** End Patch` boundaries, `Add File`,
// `Delete File`, and `Update File` hunks, `*** Move to:` renames, `@@`
// context markers, and `*** End of File` anchoring.
//
// The parser and the line-matching algorithm follow the reference
// implementation. Location of context lines uses the same four passes
// (exact, trailing-whitespace-insensitive, whitespace-insensitive, and
// Unicode-punctuation-normalized), so a patch authored against an ASCII
// rendering still applies to typographic source.
package applypatch

import (
	"fmt"
	"strings"
)

// Markers of the patch envelope.
const (
	BeginPatchMarker   = "*** Begin Patch"
	EndPatchMarker     = "*** End Patch"
	AddFileMarker      = "*** Add File: "
	DeleteFileMarker   = "*** Delete File: "
	UpdateFileMarker   = "*** Update File: "
	MoveToMarker       = "*** Move to: "
	EndOfFileMarker    = "*** End of File"
	ChangeContext      = "@@ "
	EmptyContextMarker = "@@"
)

// Hunk is one file operation.
type Hunk struct {
	// Kind is "add", "delete", or "update".
	Kind string
	// Path is the affected path; for a rename it is the source.
	Path string
	// MovePath is set for an update hunk with a `*** Move to:` line.
	MovePath string
	// Contents is the new file body for an add hunk.
	Contents string
	// Chunks are the ordered change sections of an update hunk.
	Chunks []Chunk
}

// Chunk is one `@@`-delimited change section.
type Chunk struct {
	// Context is the optional `@@ <text>` locator.
	Context string
	// OldLines are the lines the chunk expects to find.
	OldLines []string
	// NewLines are the replacement lines.
	NewLines []string
	// ContextIndices records which old/new line pairs were context lines
	// rather than inferred equal lines.
	ContextIndices [][2]int
	// EndOfFile anchors the chunk at the end of the file.
	EndOfFile bool
}

// ParseError describes a malformed patch.
type ParseError struct {
	// Line is the 1-based line number when the failure is hunk-scoped.
	Line int
	// Message is the diagnostic.
	Message string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("invalid hunk at line %d, %s", e.Line, e.Message)
	}
	return "invalid patch: " + e.Message
}

func invalidPatch(format string, args ...any) error {
	return &ParseError{Message: fmt.Sprintf(format, args...)}
}

func invalidHunk(line int, format string, args ...any) error {
	return &ParseError{Line: line, Message: fmt.Sprintf(format, args...)}
}

const validHunkHeaders = "'*** Add File: {path}', '*** Delete File: {path}', '*** Update File: {path}'"

// Parse reads a patch document. Patches are accepted leniently: markers
// may carry surrounding whitespace, and a `<<'EOF' ... EOF` heredoc
// wrapper (which gpt-4.1 produces around shell-style invocations) is
// stripped.
func Parse(text string) ([]Hunk, error) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	lines = unwrapHeredoc(lines)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != BeginPatchMarker {
		return nil, invalidPatch("The first line of the patch must be '%s'", BeginPatchMarker)
	}
	if strings.TrimSpace(lines[len(lines)-1]) != EndPatchMarker {
		return nil, invalidPatch("The last line of the patch must be '%s'", EndPatchMarker)
	}

	parser := &parser{lines: lines[1 : len(lines)-1]}
	return parser.parse()
}

// unwrapHeredoc removes a `<<EOF` / `<<'EOF'` / `<<"EOF"` wrapper,
// matching the reference parser's lenient mode.
func unwrapHeredoc(lines []string) []string {
	if len(lines) >= 4 {
		first := lines[0]
		last := lines[len(lines)-1]
		if (first == "<<EOF" || first == "<<'EOF'" || first == `<<"EOF"`) && strings.HasSuffix(last, "EOF") {
			return lines[1 : len(lines)-1]
		}
	}
	return lines
}

// parser walks the patch body one line at a time. It mirrors the
// reference streaming parser's mode machine closely enough to produce
// the same errors and the same hunks.
type parser struct {
	lines []string
	index int
	hunks []Hunk
}

// line returns the current raw line and whether one exists.
func (p *parser) line() (string, bool) {
	if p.index >= len(p.lines) {
		return "", false
	}
	return p.lines[p.index], true
}

// number is the 1-based line number in the original patch text
// (accounting for the `*** Begin Patch` line consumed by Parse).
func (p *parser) number() int { return p.index + 2 }

func (p *parser) parse() ([]Hunk, error) {
	for p.index < len(p.lines) {
		raw := p.lines[p.index]
		trimmed := strings.TrimSpace(raw)
		switch {
		case trimmed == "":
			p.index++
		case strings.HasPrefix(trimmed, AddFileMarker):
			if err := p.ensureUpdateNotPending(trimmed); err != nil {
				return nil, err
			}
			p.hunks = append(p.hunks, Hunk{
				Kind: "add",
				Path: strings.TrimPrefix(trimmed, AddFileMarker),
			})
			p.index++
			if err := p.parseAddBody(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(trimmed, DeleteFileMarker):
			if err := p.ensureUpdateNotPending(trimmed); err != nil {
				return nil, err
			}
			p.hunks = append(p.hunks, Hunk{
				Kind: "delete",
				Path: strings.TrimPrefix(trimmed, DeleteFileMarker),
			})
			p.index++
		case strings.HasPrefix(trimmed, UpdateFileMarker):
			if err := p.ensureUpdateNotPending(trimmed); err != nil {
				return nil, err
			}
			hunk := Hunk{Kind: "update", Path: strings.TrimPrefix(trimmed, UpdateFileMarker)}
			p.index++
			if err := p.parseUpdateBody(&hunk); err != nil {
				return nil, err
			}
			if hunk.MovePath == "" && len(hunk.Chunks) == 0 {
				return nil, invalidHunk(p.number()-1,
					"Update file hunk for path '%s' is empty", hunk.Path)
			}
			p.hunks = append(p.hunks, hunk)
		default:
			return nil, invalidHunk(p.number(),
				"'%s' is not a valid hunk header. Valid hunk headers: %s", trimmed, validHunkHeaders)
		}
	}
	return p.hunks, nil
}

// ensureUpdateNotPending rejects two headers in a row when the first was
// an update hunk with no body.
func (p *parser) ensureUpdateNotPending(header string) error {
	if len(p.hunks) == 0 {
		return nil
	}
	last := p.hunks[len(p.hunks)-1]
	if last.Kind == "update" && last.MovePath == "" && len(last.Chunks) == 0 {
		return invalidHunk(p.number(),
			"Update file hunk for path '%s' is empty", last.Path)
	}
	return nil
}

// parseAddBody consumes `+` lines until the next header.
func (p *parser) parseAddBody() error {
	hunk := &p.hunks[len(p.hunks)-1]
	for {
		raw, ok := p.line()
		if !ok {
			// The body ended at the patch boundary.
			if hunk.Contents == "" {
				return invalidHunk(p.number(),
					"Add file hunk for path '%s' requires at least one line", hunk.Path)
			}
			return nil
		}
		trimmed := strings.TrimSpace(raw)
		if isHunkHeader(trimmed) {
			if hunk.Contents == "" {
				return invalidHunk(p.number(),
					"Add file hunk for path '%s' requires at least one line", hunk.Path)
			}
			return nil
		}
		if !strings.HasPrefix(raw, "+") {
			return invalidHunk(p.number(),
				"'%s' is not a valid hunk header. Valid hunk headers: %s", trimmed, validHunkHeaders)
		}
		hunk.Contents += strings.TrimPrefix(raw, "+") + "\n"
		p.index++
	}
}

// parseUpdateBody consumes an update hunk: an optional move line, then
// `@@`-delimited chunks.
func (p *parser) parseUpdateBody(hunk *Hunk) error {
	for {
		raw, ok := p.line()
		if !ok {
			// The body ended at the patch boundary; the caller validates
			// that the update hunk was not empty.
			return nil
		}
		trimmed := strings.TrimSpace(raw)
		if isHunkHeader(trimmed) {
			return nil
		}
		content := strings.TrimRight(raw, "\r")
		trimmedEnd := strings.TrimRight(content, " \t")

		// A chunk anchored at end of file only tolerates blank lines
		// until the next `@@` marker.
		if len(hunk.Chunks) > 0 && hunk.Chunks[len(hunk.Chunks)-1].EndOfFile {
			if trimmedEnd == "" {
				p.index++
				continue
			}
			if trimmedEnd != EmptyContextMarker && !strings.HasPrefix(trimmedEnd, ChangeContext) {
				return invalidHunk(p.number(),
					"Expected update hunk to start with a @@ context marker, got: '%s'", content)
			}
		}

		if len(hunk.Chunks) == 0 && hunk.MovePath == "" && strings.HasPrefix(trimmedEnd, MoveToMarker) {
			hunk.MovePath = strings.TrimPrefix(trimmedEnd, MoveToMarker)
			p.index++
			continue
		}

		if trimmedEnd == EmptyContextMarker || strings.HasPrefix(trimmedEnd, ChangeContext) {
			if len(hunk.Chunks) > 0 {
				last := hunk.Chunks[len(hunk.Chunks)-1]
				if len(last.OldLines) == 0 && len(last.NewLines) == 0 {
					return invalidHunk(p.number(),
						"Unexpected line found in update hunk: '%s'. Every line should start with ' ' (context line), '+' (added line), or '-' (removed line)",
						content)
				}
			}
			chunk := Chunk{}
			if trimmedEnd != EmptyContextMarker {
				chunk.Context = strings.TrimPrefix(trimmedEnd, ChangeContext)
			}
			hunk.Chunks = append(hunk.Chunks, chunk)
			p.index++
			continue
		}

		if trimmedEnd == EndOfFileMarker {
			if len(hunk.Chunks) == 0 {
				return invalidHunk(p.number(), "Update hunk does not contain any lines")
			}
			last := &hunk.Chunks[len(hunk.Chunks)-1]
			if len(last.OldLines) == 0 && len(last.NewLines) == 0 {
				return invalidHunk(p.number(), "Update hunk does not contain any lines")
			}
			last.EndOfFile = true
			p.index++
			continue
		}

		if len(hunk.Chunks) == 0 {
			hunk.Chunks = append(hunk.Chunks, Chunk{})
		}
		last := &hunk.Chunks[len(hunk.Chunks)-1]
		switch {
		case content == "":
			last.pushContext("")
		case strings.HasPrefix(content, " "):
			last.pushContext(strings.TrimPrefix(content, " "))
		case strings.HasPrefix(content, "+"):
			last.NewLines = append(last.NewLines, strings.TrimPrefix(content, "+"))
		case strings.HasPrefix(content, "-"):
			last.OldLines = append(last.OldLines, strings.TrimPrefix(content, "-"))
		default:
			if len(last.OldLines) > 0 || len(last.NewLines) > 0 {
				return invalidHunk(p.number(),
					"Expected update hunk to start with a @@ context marker, got: '%s'", content)
			}
			return invalidHunk(p.number(),
				"Unexpected line found in update hunk: '%s'. Every line should start with ' ' (context line), '+' (added line), or '-' (removed line)",
				content)
		}
		p.index++
	}
}

// pushContext adds a context line to both sides while recording the
// index pair, mirroring the reference chunk model.
func (c *Chunk) pushContext(line string) {
	c.ContextIndices = append(c.ContextIndices, [2]int{len(c.OldLines), len(c.NewLines)})
	c.OldLines = append(c.OldLines, line)
	c.NewLines = append(c.NewLines, line)
}

func isHunkHeader(trimmed string) bool {
	return strings.HasPrefix(trimmed, AddFileMarker) ||
		strings.HasPrefix(trimmed, DeleteFileMarker) ||
		strings.HasPrefix(trimmed, UpdateFileMarker)
}
