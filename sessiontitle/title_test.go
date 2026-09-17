package sessiontitle

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanStripsControlSequencesAndCollapsesWhitespace(t *testing.T) {
	input := "\x1b]0;window title\x07Fix\x1b[31m the\x1b[0m\n\n  bug\u202e \t now\x00 "
	got := Clean(input)
	if got != "Fix the bug now" {
		t.Fatalf("Clean = %q, want %q", got, "Fix the bug now")
	}
	if strings.ContainsAny(got, "\x1b\x07\x00") {
		t.Fatalf("Clean left control bytes: %q", got)
	}
}

func TestCleanRemovesDirectionalControls(t *testing.T) {
	got := Clean("safe\u202ereversed\u2066isolate\u2069end\ufeff")
	if got != "safereversedisolateend" {
		t.Fatalf("Clean = %q", got)
	}
}

func TestFallbackCapsWordsAndBytes(t *testing.T) {
	got := Fallback("one two three four five six seven eight nine ten", 8, DefaultFallbackMaxBytes)
	if got != "one two three four five six seven eight" {
		t.Fatalf("Fallback = %q", got)
	}

	// Byte cap truncates on rune boundaries, never mid-rune.
	long := strings.Repeat("é", 100)
	got = Fallback(long, 8, 5)
	if len(got) > 5 || !utf8.ValidString(got) {
		t.Fatalf("Fallback bytes = %d (%q), want <= 5 valid UTF-8", len(got), got)
	}
	if got != "éé" {
		t.Fatalf("Fallback = %q, want two runes (4 bytes)", got)
	}
}

func TestFallbackEmptyInputYieldsEmptyTitle(t *testing.T) {
	if got := Fallback("\x1b[2J\x07   \n", DefaultFallbackMaxWords, DefaultFallbackMaxBytes); got != "" {
		t.Fatalf("Fallback = %q, want empty", got)
	}
}

func TestNormalizeRejectsEffectivelyEmptyTitles(t *testing.T) {
	if got := Normalize("\x1b]0;title\x07\u202e\u200b  ", DefaultMaxTitleBytes); got != "" {
		t.Fatalf("Normalize = %q, want empty after sanitization", got)
	}
	got := Normalize("  A real title  ", DefaultMaxTitleBytes)
	if got != "A real title" {
		t.Fatalf("Normalize = %q", got)
	}
}

func TestTruncateUTF8KeepsRuneBoundaries(t *testing.T) {
	text := "日本語テキスト"
	got := TruncateUTF8(text, 7)
	if got != "日本" {
		t.Fatalf("TruncateUTF8 = %q, want 日本 (6 bytes, next rune would exceed)", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("TruncateUTF8 produced invalid UTF-8: %q", got)
	}
	if got := TruncateUTF8("short", 100); got != "short" {
		t.Fatalf("TruncateUTF8 rewrote a short string: %q", got)
	}
}

func TestFallbackDefaultsMatchDocumentedShape(t *testing.T) {
	if DefaultFallbackMaxWords != 8 || DefaultFallbackMaxBytes != 64 || DefaultMaxTitleBytes != 200 {
		t.Fatalf("defaults drifted: %d/%d/%d",
			DefaultFallbackMaxWords, DefaultFallbackMaxBytes, DefaultMaxTitleBytes)
	}
}
