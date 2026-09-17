// Package configlayer composes configuration documents from ordered
// layers, mirroring codex's ConfigLayerStack: every layer keeps its
// source, higher precedence wins per leaf key, and a managed
// requirements layer can enforce or restrict values.
package configlayer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind identifies where a layer came from.
type Kind string

const (
	// KindSystem is the host-wide configuration file.
	KindSystem Kind = "system"
	// KindUser is the user configuration file.
	KindUser Kind = "user"
	// KindProfile is the selected profile's overrides.
	KindProfile Kind = "profile"
	// KindProject is the repository-local configuration file.
	KindProject Kind = "project"
	// KindFile is the file named by --config.
	KindFile Kind = "file"
	// KindFlags are the session's command-line overrides.
	KindFlags Kind = "flags"
)

// Precedence returns the layer's precedence; a higher number overrides
// a lower one. The table follows codex's ConfigLayerSource ordering so
// the relative behavior of user, profile, project, and session layers
// matches the reference implementation.
func (k Kind) Precedence() int {
	switch k {
	case KindSystem:
		return 10
	case KindUser:
		return 20
	case KindProfile:
		return 21
	case KindProject:
		return 25
	case KindFile:
		return 27
	case KindFlags:
		return 30
	default:
		return 0
	}
}

// Source identifies one layer.
type Source struct {
	Kind    Kind
	Path    string
	Profile string
	// Rank overrides Kind.Precedence() when non-zero. Profiles use it to
	// sort immediately above the layer that defined them, so a profile
	// declared in the project file still wins over that file's base
	// values.
	Rank int
}

// Precedence returns the effective precedence of the source.
func (s Source) Precedence() int {
	if s.Rank != 0 {
		return s.Rank
	}
	return s.Kind.Precedence()
}

// Label renders the source the way error messages should name it.
func (s Source) Label() string {
	switch {
	case s.Kind == KindProfile && s.Profile != "" && s.Path != "":
		return fmt.Sprintf("profile %q (%s: %s)", s.Profile, s.Kind, s.Path)
	case s.Kind == KindProfile && s.Profile != "":
		return fmt.Sprintf("profile %q", s.Profile)
	case s.Path != "":
		return fmt.Sprintf("%s (%s)", s.Kind, s.Path)
	default:
		return string(s.Kind)
	}
}

// Layer is one configuration document with its provenance.
type Layer struct {
	Source Source
	Data   map[string]any
	// Disabled records why a discovered layer was skipped, for
	// diagnostics only.
	Disabled string
}

// Stack accumulates layers.
type Stack struct {
	layers []Layer
}

// New creates an empty stack.
func New() *Stack { return &Stack{} }

// Add appends a layer. A nil or empty document is recorded but
// contributes nothing.
func (s *Stack) Add(source Source, data map[string]any) {
	s.layers = append(s.layers, Layer{Source: source, Data: data})
}

// AddFile reads a JSON object layer. A missing file is skipped unless
// required; a file that is not a JSON object is an error.
func (s *Stack) AddFile(kind Kind, path string, required bool) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			s.layers = append(s.layers, Layer{
				Source:   Source{Kind: kind, Path: path},
				Disabled: "file not found",
			})
			return nil
		}
		return fmt.Errorf("read config %s (%s layer): %w", path, kind, err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse config %s (%s layer): %w", path, kind, err)
	}
	if document == nil {
		document = map[string]any{}
	}
	s.layers = append(s.layers, Layer{Source: Source{Kind: kind, Path: path}, Data: document})
	return nil
}

// Layers returns the recorded layers in insertion order.
func (s *Stack) Layers() []Layer {
	out := make([]Layer, len(s.layers))
	copy(out, s.layers)
	return out
}

// Merge folds the layers into one document and reports, for every leaf
// key path, the source that supplied the winning value. Layers with a
// higher precedence win regardless of insertion order.
func (s *Stack) Merge() (map[string]any, map[string]Source, error) {
	ordered := make([]Layer, len(s.layers))
	copy(ordered, s.layers)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Source.Precedence() < ordered[j].Source.Precedence()
	})
	merged := map[string]any{}
	provenance := map[string]Source{}
	for _, layer := range ordered {
		if layer.Disabled != "" {
			continue
		}
		if err := mergeInto(merged, layer.Data, "", layer.Source, provenance); err != nil {
			return nil, nil, err
		}
	}
	return merged, provenance, nil
}

// mergeInto deep-merges document into target. Objects merge key by key;
// every other value replaces the previous one.
func mergeInto(target, document map[string]any, prefix string, source Source, provenance map[string]Source) error {
	for key, value := range document {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if targetValue, ok := target[key]; ok {
			targetObject, targetIsObject := targetValue.(map[string]any)
			valueObject, valueIsObject := value.(map[string]any)
			if targetIsObject && valueIsObject {
				if err := mergeInto(targetObject, valueObject, path, source, provenance); err != nil {
					return err
				}
				continue
			}
			if targetIsObject != valueIsObject {
				return fmt.Errorf(
					"config key %s is an object in one layer and a %s in %s; layers must agree on the shape",
					path, describeJSON(value), source.Label(),
				)
			}
		}
		target[key] = value
		recordProvenance(provenance, path, value, source)
	}
	return nil
}

// recordProvenance attributes every leaf below a value to the source.
func recordProvenance(provenance map[string]Source, path string, value any, source Source) {
	provenance[path] = source
	if object, ok := value.(map[string]any); ok {
		for key, nested := range object {
			recordProvenance(provenance, path+"."+key, nested, source)
		}
	}
}

func describeJSON(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// Get reads a dotted key path.
func Get(document map[string]any, path string) (any, bool) {
	if path == "" {
		return document, true
	}
	current := any(document)
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[part]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// Set writes a dotted key path, creating intermediate objects.
func Set(document map[string]any, path string, value any) error {
	parts := strings.Split(path, ".")
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok {
			child := map[string]any{}
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("config key %s: %s is not an object", path, part)
		}
		current = child
	}
	current[parts[len(parts)-1]] = value
	return nil
}

// ExtractProfile returns the overrides a named profile declares,
// following codex's `[profiles.<name>]` selection. An unknown profile is
// an error that lists what is available, because silently ignoring
// `--profile` would run with unintended settings.
func ExtractProfile(document map[string]any, name string) (map[string]any, error) {
	raw, ok := document["profiles"]
	if !ok {
		return nil, fmt.Errorf("profile %q is not defined (no profiles are configured)", name)
	}
	profiles, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("config key profiles must be an object")
	}
	selected, ok := profiles[name]
	if !ok {
		names := make([]string, 0, len(profiles))
		for candidate := range profiles {
			names = append(names, candidate)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return nil, fmt.Errorf("profile %q is not defined (no profiles are configured)", name)
		}
		return nil, fmt.Errorf("profile %q is not defined (available: %s)", name, strings.Join(names, ", "))
	}
	overrides, ok := selected.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("profile %q must be an object", name)
	}
	return overrides, nil
}

// WithoutProfiles removes the profiles key so the merged document
// validates against the typed schema, which has no such field.
func WithoutProfiles(document map[string]any) map[string]any {
	delete(document, "profiles")
	return document
}

// UserConfigPath returns the per-user configuration path.
func UserConfigPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ZENFORGE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "zenforge.json"), nil
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "zenforge", "zenforge.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil
	}
	return filepath.Join(home, ".config", "zenforge", "zenforge.json"), nil
}

// SystemConfigPath returns the host-wide configuration path.
func SystemConfigPath() string { return "/etc/zenforge/zenforge.json" }

// RequirementsPath returns the host-wide requirements path.
func RequirementsPath() string { return "/etc/zenforge/requirements.json" }

// ProjectConfigPath walks up from dir looking for the repository-local
// configuration file.
func ProjectConfigPath(dir string) string {
	current, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(current, ".zenforge", "zenforge.json")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}
