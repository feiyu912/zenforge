package prompt

import (
	"strings"
	"testing"
)

func TestSectionsRenderInOrderWithNameTiebreak(t *testing.T) {
	registry := New()
	if err := registry.Variables(map[string]string{"workspace": "/tmp/ws", "platform": "darwin/arm64"}); err != nil {
		t.Fatalf("Variables returned error: %v", err)
	}
	for _, section := range []Section{
		{Name: "runtime:environment", Order: OrderRuntimeContext, Text: "<environment_context/>"},
		{Name: "deployment:persona-prefix", Order: OrderPersonaPrefix, Text: "You are a reviewer in {{workspace}} on {{platform}}.", Interpolate: true},
		{Name: "deployment:instructions", Order: OrderDeploymentPolicy, Text: "Be concise."},
		{Name: "zeta", Order: 999, Text: "z"},
		{Name: "alpha", Order: 999, Text: "a"},
		{Name: "empty", Order: 5, Text: ""},
	} {
		if err := registry.AddSection(section); err != nil {
			t.Fatalf("AddSection(%s) returned error: %v", section.Name, err)
		}
	}
	sections, err := registry.Sections()
	if err != nil {
		t.Fatalf("Sections returned error: %v", err)
	}
	got := make([]string, 0, len(sections))
	for _, section := range sections {
		got = append(got, section.Name)
	}
	want := []string{"deployment:persona-prefix", "deployment:instructions", "runtime:environment", "alpha", "zeta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if sections[0].Text != "You are a reviewer in /tmp/ws on darwin/arm64." {
		t.Fatalf("interpolated text = %q", sections[0].Text)
	}
	joined, err := registry.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if !strings.Contains(joined, "\n\n") || strings.HasPrefix(joined, "\n") {
		t.Fatalf("Render joined text = %q", joined)
	}
}

func TestRegistryRejectsDuplicateAndConflictingRegistrations(t *testing.T) {
	registry := New()
	if err := registry.AddSection(Section{Name: "dup", Order: 1, Text: "x"}); err != nil {
		t.Fatalf("AddSection returned error: %v", err)
	}
	if err := registry.AddSection(Section{Name: "dup", Order: 2, Text: "y"}); err == nil {
		t.Fatal("duplicate section accepted")
	}
	if err := registry.AddContext(Context{Name: "dup", Order: 1, Text: "x"}); err != nil {
		t.Fatalf("AddContext returned error: %v", err)
	}
	if err := registry.AddContext(Context{Name: "dup", Order: 2, Text: "y"}); err == nil {
		t.Fatal("duplicate context accepted")
	}
	if err := registry.Variable("var", "1"); err != nil {
		t.Fatalf("Variable returned error: %v", err)
	}
	if err := registry.Variable("var", "2"); err == nil {
		t.Fatal("duplicate variable accepted")
	}
	if err := registry.Variable("Bad-Name", "1"); err == nil {
		t.Fatal("invalid variable name accepted")
	}
	if err := registry.AddSection(Section{Name: "", Order: 1}); err == nil {
		t.Fatal("nameless section accepted")
	}
}

func TestCompleteSectionSuppressesEverythingElse(t *testing.T) {
	registry := New()
	for _, section := range []Section{
		{Name: "a", Order: 1, Text: "first"},
		{Name: "whole", Order: 2, Text: "complete", Complete: true},
		{Name: "z", Order: 3, Text: "last"},
	} {
		if err := registry.AddSection(section); err != nil {
			t.Fatalf("AddSection returned error: %v", err)
		}
	}
	rendered, err := registry.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if rendered != "complete" {
		t.Fatalf("Render = %q, want the complete section only", rendered)
	}

	conflict := New()
	for _, section := range []Section{
		{Name: "one", Order: 1, Text: "one", Complete: true},
		{Name: "two", Order: 2, Text: "two", Complete: true},
	} {
		if err := conflict.AddSection(section); err != nil {
			t.Fatalf("AddSection returned error: %v", err)
		}
	}
	if _, err := conflict.Render(); err == nil {
		t.Fatal("two complete sections were accepted")
	}
}

func TestInterpolateStrictReferenceGrammar(t *testing.T) {
	variables := map[string]string{"name": "world", "empty": ""}

	got, err := Interpolate("hello {{name}}!", variables, "section", "s")
	if err != nil || got != "hello world!" {
		t.Fatalf("Interpolate = %q err=%v", got, err)
	}

	// An empty registered value substitutes as empty rather than failing.
	got, err = Interpolate("[{{empty}}]", variables, "section", "s")
	if err != nil || got != "[]" {
		t.Fatalf("empty value = %q err=%v", got, err)
	}

	// A lone "{{" with no later "}}" is literal prose.
	got, err = Interpolate("use {{ to open", variables, "section", "s")
	if err != nil || got != "use {{ to open" {
		t.Fatalf("literal braces = %q err=%v", got, err)
	}

	// Braces inside the group are malformed when a "}}" follows later.
	if _, err := Interpolate("{{a {b} }}", variables, "section", "s"); err == nil {
		t.Fatal("malformed reference accepted")
	}
	// Invalid variable names are malformed, not unknown.
	if _, err := Interpolate("{{Name}}", variables, "section", "s"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("invalid name error = %v", err)
	}
	// Unknown names fail loudly and list what is registered.
	_, err = Interpolate("{{missing}}", variables, "section", "s")
	if err == nil || !strings.Contains(err.Error(), "unknown prompt variable") || !strings.Contains(err.Error(), "empty, name") {
		t.Fatalf("unknown variable error = %v", err)
	}

	// Substituted values are never rescanned.
	replay := map[string]string{"a": "{{b}}", "b": "boom"}
	got, err = Interpolate("{{a}}", replay, "section", "s")
	if err != nil || got != "{{b}}" {
		t.Fatalf("rescan check = %q err=%v", got, err)
	}
}

func TestRenderContextSnapshotMatchesDSHPreamble(t *testing.T) {
	registry := New()
	if err := registry.AddContext(Context{Name: "policy", Order: OrderContextSandboxPolicy, Text: "confined"}); err != nil {
		t.Fatalf("AddContext returned error: %v", err)
	}
	if err := registry.AddContext(Context{Name: "empty", Order: 1, Text: ""}); err != nil {
		t.Fatalf("AddContext returned error: %v", err)
	}
	got, err := registry.RenderContext()
	if err != nil {
		t.Fatalf("RenderContext returned error: %v", err)
	}
	want := ContextSnapshotPrefix + "\n\nconfined"
	if got != want {
		t.Fatalf("RenderContext = %q, want %q", got, want)
	}
	sections, err := registry.ContextSections()
	if err != nil {
		t.Fatalf("ContextSections returned error: %v", err)
	}
	if len(sections) != 1 || sections[0].Name != "policy" {
		t.Fatalf("context sections = %#v", sections)
	}

	empty, err := New().RenderContext()
	if err != nil || empty != "" {
		t.Fatalf("empty RenderContext = %q err=%v", empty, err)
	}
}

func TestNonInterpolatedSectionsKeepBraces(t *testing.T) {
	registry := New()
	if err := registry.AddSection(Section{Name: "instructions", Order: 1, Text: "Run {{.Task}} now"}); err != nil {
		t.Fatalf("AddSection returned error: %v", err)
	}
	rendered, err := registry.Render()
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	if rendered != "Run {{.Task}} now" {
		t.Fatalf("verbatim section = %q", rendered)
	}
}
