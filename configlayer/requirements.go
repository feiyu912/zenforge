package configlayer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Requirements are the managed, administrator-supplied constraints a
// session cannot escape, mirroring codex's requirements/constraint
// model: `allowed` rejects a candidate outside a permitted set
// (validator), and `enforce` overwrites a value outright (normalizer).
//
// The document shape is:
//
//	{
//	  "allowed": { "approval.policy": ["never", "on_request"] },
//	  "enforce": { "shell.enabled": false }
//	}
type Requirements struct {
	source   string
	allowed  map[string][]any
	enforced map[string]any
	order    []string
}

// ParseRequirements builds the requirements from a parsed document.
// source labels every violation, so an operator can find the file that
// imposed the constraint.
func ParseRequirements(source string, document map[string]any) (*Requirements, error) {
	requirements := &Requirements{
		source:   source,
		allowed:  map[string][]any{},
		enforced: map[string]any{},
	}
	for _, key := range []string{"allowed", "enforce"} {
		raw, ok := document[key]
		if !ok {
			continue
		}
		section, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("requirements %s: %s must be an object", source, key)
		}
		switch key {
		case "allowed":
			for path, value := range section {
				list, ok := value.([]any)
				if !ok || len(list) == 0 {
					return nil, fmt.Errorf("requirements %s: allowed.%s must be a non-empty list", source, path)
				}
				requirements.allowed[path] = list
			}
		case "enforce":
			for path, value := range section {
				if value == nil {
					return nil, fmt.Errorf("requirements %s: enforce.%s must not be null", source, path)
				}
				requirements.enforced[path] = value
				requirements.order = append(requirements.order, path)
			}
		}
	}
	sort.Strings(requirements.order)
	if len(requirements.allowed) == 0 && len(requirements.enforced) == 0 {
		return nil, fmt.Errorf("requirements %s: neither allowed nor enforce is present", source)
	}
	return requirements, nil
}

// Source names where the requirements came from.
func (r *Requirements) Source() string { return r.source }

// Enforced lists the enforced key paths in sorted order.
func (r *Requirements) Enforced() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Apply validates a candidate document against the allowed sets and
// then applies the enforced values, returning a new document. The input
// is never modified.
func (r *Requirements) Apply(candidate map[string]any) (map[string]any, error) {
	if r == nil {
		return candidate, nil
	}
	for path, allowed := range r.allowed {
		value, present := Get(candidate, path)
		if !present {
			continue
		}
		if !containsJSON(allowed, value) {
			return nil, fmt.Errorf(
				"invalid value for %s: %s is not in the allowed set %s (set by %s)",
				path, renderJSON(value), renderJSON(allowed), r.source,
			)
		}
	}
	merged := cloneDocument(candidate)
	for _, path := range r.order {
		if err := Set(merged, path, r.enforced[path]); err != nil {
			return nil, fmt.Errorf("requirements %s: enforce.%s: %w", r.source, path, err)
		}
	}
	return merged, nil
}

func containsJSON(allowed []any, value any) bool {
	want, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, candidate := range allowed {
		have, err := json.Marshal(candidate)
		if err != nil {
			continue
		}
		if string(have) == string(want) {
			return true
		}
	}
	return false
}

func renderJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

// cloneDocument deep-copies a JSON document so callers keep their input.
func cloneDocument(document map[string]any) map[string]any {
	out := make(map[string]any, len(document))
	for key, value := range document {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneDocument(typed)
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = cloneValue(entry)
		}
		return out
	default:
		return value
	}
}

// UnknownFields reports the keys of a document that a typed target does
// not declare, which is how `--strict-config` (codex's strict-config)
// turns typo-prone settings into startup errors instead of silent no-ops.
func UnknownFields(document map[string]any, target any) ([]string, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		message := err.Error()
		const marker = "unknown field "
		if index := strings.Index(message, marker); index >= 0 {
			field := strings.Trim(message[index+len(marker):], "\"")
			return []string{field}, nil
		}
		return nil, err
	}
	return nil, nil
}
