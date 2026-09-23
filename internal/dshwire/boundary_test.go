package dshwire

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"testing"
)

// consoleClientPath is the vendored console's session controller. The wire rules
// this package implements are its rules, so they are read from the bytes the
// browser runs rather than restated here: a console upgrade that changes the
// vocabulary or the surface set must fail these tests instead of silently
// mis-marking every record the host serves.
const consoleClientPath = "../../webui/dsh/plugins/api/session-controller/client.js"

// hostEventsPath is the host's own event vocabulary.
const hostEventsPath = "../../events.go"

var (
	knownTypesPattern   = regexp.MustCompile(`(?s)const KNOWN_SESSION_EVENT_TYPES = new Set\(\[(.*?)\]\);`)
	surfaceTypesPattern = regexp.MustCompile(`(?s)const SURFACE_EVENT_TYPES = new Set\(\[(.*?)\]\);`)
	quotedNamePattern   = regexp.MustCompile(`"([^"]+)"`)
	eventNamePattern    = regexp.MustCompile(`Event\w+\s+EventType = "([^"]+)"`)
)

// TestKnownEventTypesMatchTheVendoredConsole fails when this package's copy of
// the console's vocabulary drifts from the console's own list. The filter depends
// on it: a record is served only when its type is in this table, so a type the
// console adds must be served and one it drops must not be (ADR 0117).
func TestKnownEventTypesMatchTheVendoredConsole(t *testing.T) {
	source := readFile(t, consoleClientPath)
	names := extractNames(t, knownTypesPattern, source, "KNOWN_SESSION_EVENT_TYPES")
	if len(names) < 40 {
		t.Fatalf("extracted %d console event types; the pattern is stale", len(names))
	}
	for _, name := range names {
		if !KnownEventTypes[name] {
			t.Errorf("the console knows %q and this table does not", name)
		}
	}
	for name := range KnownEventTypes {
		if !containsSorted(names, name) {
			t.Errorf("this table knows %q and the console does not", name)
		}
	}
}

// TestSurfaceEligibleTypesMatchTheVendoredConsole fails when the four
// message-producing types change: `surfaceOp` is legal on exactly those, and the
// client throws on any other type that carries it.
func TestSurfaceEligibleTypesMatchTheVendoredConsole(t *testing.T) {
	source := readFile(t, consoleClientPath)
	names := extractNames(t, surfaceTypesPattern, source, "SURFACE_EVENT_TYPES")
	if len(names) != len(SurfaceEligibleTypes) {
		t.Fatalf("console surface types = %v, this table = %v", names, SurfaceEligibleTypes)
	}
	for _, name := range names {
		if !SurfaceEligibleTypes[name] {
			t.Errorf("the console treats %q as surface-eligible and this table does not", name)
		}
	}
}

// TestProjectionIsLegalForTheVendoredConsole re-implements the client's own
// acceptance check over a projected turn: the envelope allows exactly these
// fields, every type is one the console knows, and `surfaceOp` is present exactly
// on the surface-eligible types. A record that fails this would make the page
// report "Failed to load history" exactly as it did before this package existed.
func TestProjectionIsLegalForTheVendoredConsole(t *testing.T) {
	allowed := map[string]bool{
		"type": true, "seq": true, "time": true, "data": true,
		"ignorable": true, "surfaceOp": true, "sourceEventSeqs": true,
	}
	projection := Project(aTurn(), Identity{Provider: "openai", Model: "qwen-plus"}, nil)
	for _, record := range projection.Events {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal %s: %v", record.Type, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatalf("unmarshal %s: %v", record.Type, err)
		}
		for field := range fields {
			if !allowed[field] {
				t.Fatalf("%s carries the unexpected field %q", record.Type, field)
			}
		}
		if raw, ok := fields["ignorable"]; ok && string(raw) != "true" {
			t.Fatalf("%s carries ignorable %s, want literal true or absent", record.Type, raw)
		}
		_, hasSurfaceOp := fields["surfaceOp"]
		if hasSurfaceOp != SurfaceEligibleTypes[record.Type] {
			t.Fatalf("%s carries surfaceOp=%v, want %v", record.Type, hasSurfaceOp, SurfaceEligibleTypes[record.Type])
		}
		if !KnownEventTypes[record.Type] {
			t.Fatalf("%s is outside the console's vocabulary and was served anyway", record.Type)
		}
	}
}

func extractNames(t *testing.T, pattern *regexp.Regexp, source, label string) []string {
	t.Helper()
	match := pattern.FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("the console bundle no longer declares %s", label)
	}
	names := quotedNamePattern.FindAllStringSubmatch(match[1], -1)
	extracted := make([]string, 0, len(names))
	for _, name := range names {
		extracted = append(extracted, name[1])
	}
	sort.Strings(extracted)
	return extracted
}

func containsSorted(sorted []string, value string) bool {
	index := sort.SearchStrings(sorted, value)
	return index < len(sorted) && sorted[index] == value
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}
