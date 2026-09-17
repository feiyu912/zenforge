// Package prompt implements the DSH system-prompt registry contract:
// ordered, uniquely named sections and runtime contexts rendered with
// strict {{variable}} interpolation. References are complete simple
// groups — `{{name}}` with a name matching ^[a-z][a-z0-9_]*$ — and an
// unknown or malformed reference fails the assembly instead of
// rendering empty text. A lone `{{` with no later `}}` is literal
// prose, and substituted values are never rescanned.
package prompt

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Section order slots, ported from the DSH SECTION_ORDERS table so
// third-party sections can be allocated a stable position relative to
// first-party guidance.
const (
	OrderHarnessIdentity  = -1000
	OrderPersonaPrefix    = 0
	OrderPlanPolicy       = 500
	OrderDeploymentPolicy = 600
	OrderRuntimeContext   = 700
	OrderProjectRules     = 800
	OrderSkillCatalog     = 850
	// OrderHookContext is context injected by lifecycle hooks; it sits with
	// the project rules and above the skill catalog.
	OrderHookContext = 820
	// OrderMemoryContext is the consolidated memory summary. It sits just
	// below hook context: both are runtime-injected instructions, and a hook
	// is about this run while a memory is about earlier ones.
	OrderMemoryContext    = 815
	OrderFileReference    = 900
	OrderToolBase         = 1000
	OrderToolsSDK         = 5000
	OrderDeliverables     = 9000
	OrderStructuredOutput = 9900
	OrderHarnessSource    = 10000
	OrderWebSurface       = 10100
	OrderPersonaSuffix    = 10200
)

// Context order slots, ported from the DSH CONTEXT_ORDERS table.
const (
	OrderContextSandboxPolicy  = 110
	OrderContextApprovalPolicy = 115
	OrderContextDelegation     = 120
)

// SectionPrefix begins the joined system prompt, matching DSH's runtime
// context snapshot wording.
const ContextSnapshotPrefix = "Current runtime context. This snapshot supersedes earlier runtime-context snapshots."

var (
	groupAt      = regexp.MustCompile(`^\{\{([^{}]*)\}\}`)
	variableName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Section is one contributed part of the system prompt. Sections are
// concatenated in ascending Order; equal orders fall back to name
// order. A Complete section becomes the sole section of the rendered
// prompt, and more than one effective Complete section is an error.
type Section struct {
	Name string
	// Order positions the section; see the Order* constants.
	Order int
	// Text is the section body. When Interpolate is set, {{variable}}
	// references are resolved against the registry variables.
	Text string
	// Interpolate enables strict {{variable}} substitution.
	Interpolate bool
	// Complete replaces every other section in the rendered prompt.
	Complete bool
}

// Context is dynamic runtime context: rendered separately from the
// system prompt and re-injected as a durable snapshot.
type Context struct {
	Name  string
	Order int
	Text  string
	// Interpolate enables strict {{variable}} substitution.
	Interpolate bool
}

// RenderedSection is one assembled section with its text resolved.
type RenderedSection struct {
	Name string
	Text string
}

// Registry collects sections, contexts, and variables for one
// assembly. Duplicate names are rejected at registration.
type Registry struct {
	sections  map[string]Section
	contexts  map[string]Context
	variables map[string]string
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{
		sections:  map[string]Section{},
		contexts:  map[string]Context{},
		variables: map[string]string{},
	}
}

// Variable registers one interpolation value. Empty values are legal
// and substitute as the empty string; an unregistered name is an
// unknown-variable failure.
func (r *Registry) Variable(name, value string) error {
	if !variableName.MatchString(name) {
		return fmt.Errorf("prompt variable %q must match %s", name, variableName)
	}
	if _, exists := r.variables[name]; exists {
		return fmt.Errorf("prompt variable %q is already registered", name)
	}
	r.variables[name] = value
	return nil
}

// Variables registers a batch of interpolation values.
func (r *Registry) Variables(values map[string]string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := r.Variable(name, values[name]); err != nil {
			return err
		}
	}
	return nil
}

// AddSection registers a section. Empty text is allowed and simply
// renders nothing.
func (r *Registry) AddSection(section Section) error {
	if section.Name == "" {
		return fmt.Errorf("prompt section requires a name")
	}
	if _, exists := r.sections[section.Name]; exists {
		return fmt.Errorf("prompt section %q is already registered", section.Name)
	}
	r.sections[section.Name] = section
	return nil
}

// AddContext registers a runtime context contribution.
func (r *Registry) AddContext(context Context) error {
	if context.Name == "" {
		return fmt.Errorf("prompt context requires a name")
	}
	if _, exists := r.contexts[context.Name]; exists {
		return fmt.Errorf("prompt context %q is already registered", context.Name)
	}
	r.contexts[context.Name] = context
	return nil
}

// Sections renders the registered sections in order, dropping empty
// results. A complete section must be unique and suppresses the rest.
func (r *Registry) Sections() ([]RenderedSection, error) {
	ordered := make([]Section, 0, len(r.sections))
	for _, section := range r.sections {
		ordered = append(ordered, section)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Order != ordered[j].Order {
			return ordered[i].Order < ordered[j].Order
		}
		return ordered[i].Name < ordered[j].Name
	})

	var complete *Section
	out := make([]RenderedSection, 0, len(ordered))
	for i := range ordered {
		section := ordered[i]
		text, err := r.renderText(section.Text, section.Interpolate, "section", section.Name)
		if err != nil {
			return nil, err
		}
		if section.Complete {
			if complete != nil {
				return nil, fmt.Errorf("prompt has more than one complete section: %q and %q", complete.Name, section.Name)
			}
			copy := section
			complete = &copy
			continue
		}
		if text == "" {
			continue
		}
		out = append(out, RenderedSection{Name: section.Name, Text: text})
	}
	if complete != nil {
		text, err := r.renderText(complete.Text, complete.Interpolate, "section", complete.Name)
		if err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		return []RenderedSection{{Name: complete.Name, Text: text}}, nil
	}
	return out, nil
}

// Render joins the rendered sections with blank lines, mirroring DSH's
// renderPrompt. It returns "" when every section renders empty.
func (r *Registry) Render() (string, error) {
	sections, err := r.Sections()
	if err != nil {
		return "", err
	}
	return JoinSections(sections), nil
}

// JoinSections joins already-rendered sections.
func JoinSections(sections []RenderedSection) string {
	texts := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Text != "" {
			texts = append(texts, section.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// ContextSections renders the runtime context contributions in order,
// dropping empty results and attributing each to its contributor.
func (r *Registry) ContextSections() ([]RenderedSection, error) {
	ordered := make([]Context, 0, len(r.contexts))
	for _, context := range r.contexts {
		ordered = append(ordered, context)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Order != ordered[j].Order {
			return ordered[i].Order < ordered[j].Order
		}
		return ordered[i].Name < ordered[j].Name
	})
	out := make([]RenderedSection, 0, len(ordered))
	for _, context := range ordered {
		text, err := r.renderText(context.Text, context.Interpolate, "context", context.Name)
		if err != nil {
			return nil, err
		}
		if text == "" {
			continue
		}
		out = append(out, RenderedSection{Name: context.Name, Text: text})
	}
	return out, nil
}

// RenderContext renders the runtime context snapshot: the DSH preamble
// followed by the joined contributions, or "" when none are active.
func (r *Registry) RenderContext() (string, error) {
	sections, err := r.ContextSections()
	if err != nil {
		return "", err
	}
	body := JoinSections(sections)
	if body == "" {
		return "", nil
	}
	return ContextSnapshotPrefix + "\n\n" + body, nil
}

func (r *Registry) renderText(text string, interpolate bool, kind, name string) (string, error) {
	if !interpolate {
		return text, nil
	}
	return Interpolate(text, r.variables, kind, name)
}

// Interpolate resolves strict {{variable}} references in one section or
// context body, mirroring DSH's interpolate step. Malformed references,
// unknown names, and (because Go maps cannot distinguish them) absent
// values all fail.
func Interpolate(text string, variables map[string]string, kind, name string) (string, error) {
	var result strings.Builder
	last := 0
	for {
		open := strings.Index(text[last:], "{{")
		if open < 0 {
			break
		}
		open += last
		group := groupAt.FindStringSubmatch(text[open:])
		if group == nil {
			if strings.Contains(text[open+2:], "}}") {
				return "", fmt.Errorf("malformed prompt variable reference at %q in %s %q (references are complete simple {{name}} groups)", preview(text[open:]), kind, name)
			}
			result.WriteString(text[last : open+2])
			last = open + 2
			continue
		}
		variable := group[1]
		if !variableName.MatchString(variable) {
			return "", fmt.Errorf("malformed prompt variable reference %q in %s %q (variable names match %s)", "{{"+variable+"}}", kind, name, variableName)
		}
		value, ok := variables[variable]
		if !ok {
			return "", fmt.Errorf("unknown prompt variable %q in %s %q; registered variables: %s", "{{"+variable+"}}", kind, name, registeredNames(variables))
		}
		result.WriteString(text[last:open])
		result.WriteString(value)
		last = open + len(group[0])
	}
	result.WriteString(text[last:])
	return result.String(), nil
}

func registeredNames(variables map[string]string) string {
	if len(variables) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func preview(text string) string {
	const limit = 16
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
