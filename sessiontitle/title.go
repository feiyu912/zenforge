// Package sessiontitle derives terminal-safe, UTF-8-bounded session
// titles following the DSH session-title contract: hostile control
// sequences (OSC/CSI/ESC, C0/C1 controls, bidi/directional marks) are
// stripped, whitespace collapses to single spaces, and the result is
// truncated on rune boundaries to a byte budget. Titles are log-only
// metadata — they never enter the model surface.
package sessiontitle

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Default limits match the shape of the DSH session-title configuration:
// the deterministic fallback keeps the first eight words within 64 bytes,
// and an explicit title is accepted up to 200 bytes.
const (
	DefaultFallbackMaxWords = 8
	DefaultFallbackMaxBytes = 64
	DefaultMaxTitleBytes    = 200
)

// The DSH sanitizers, ported to Go. The OSC pattern drops the negative
// lookahead (Go's regexp has no lookahead) and stops the body at either
// terminator instead, which accepts the same common BEL/ST forms.
var (
	oscSequence        = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\|$)")
	csiSequence        = regexp.MustCompile("(?:\x1b\\[|\\x{009b})[0-?]*[ -/]*[@-~]")
	escSequence        = regexp.MustCompile("\x1b[@-_]")
	controlCharacter   = regexp.MustCompile("[\\x00-\\x08\\x0b\\x0c\\x0e-\\x1f\\x{007f}-\\x{009f}]")
	directionalControl = regexp.MustCompile("[\\x{200b}\\x{200e}\\x{200f}\\x{202a}-\\x{202e}\\x{2060}-\\x{2064}\\x{2066}-\\x{206f}\\x{feff}]")
	whitespaceRun      = regexp.MustCompile("\\s+")
)

// Clean strips escape sequences, control characters, and directional
// controls, collapses whitespace to single spaces, and trims.
func Clean(input string) string {
	cleaned := oscSequence.ReplaceAllString(input, "")
	cleaned = csiSequence.ReplaceAllString(cleaned, "")
	cleaned = escSequence.ReplaceAllString(cleaned, "")
	cleaned = controlCharacter.ReplaceAllString(cleaned, "")
	cleaned = directionalControl.ReplaceAllString(cleaned, "")
	cleaned = whitespaceRun.ReplaceAllString(cleaned, " ")
	return strings.TrimSpace(cleaned)
}

// TruncateUTF8 returns the longest leading rune prefix within maxBytes.
func TruncateUTF8(input string, maxBytes int) string {
	if maxBytes <= 0 || len(input) <= maxBytes {
		return input
	}
	used := 0
	var builder strings.Builder
	for _, r := range input {
		size := utf8.RuneLen(r)
		if used+size > maxBytes {
			break
		}
		builder.WriteRune(r)
		used += size
	}
	return builder.String()
}

// Normalize returns one terminal-safe title line within maxBytes,
// mirroring normalizeSessionTitle.
func Normalize(input string, maxBytes int) string {
	return strings.TrimRight(TruncateUTF8(Clean(input), maxBytes), " ")
}

// Fallback derives the deterministic first-prompt title: the first
// maxWords whitespace-delimited words of the cleaned text, within
// maxBytes, mirroring fallbackSessionTitle.
func Fallback(input string, maxWords, maxBytes int) string {
	if maxWords <= 0 {
		maxWords = DefaultFallbackMaxWords
	}
	if maxBytes <= 0 {
		maxBytes = DefaultFallbackMaxBytes
	}
	words := strings.Fields(Clean(input))
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	return strings.TrimRight(TruncateUTF8(strings.Join(words, " "), maxBytes), " ")
}
