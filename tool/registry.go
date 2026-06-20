package tool

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

type MemoryRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) (*MemoryRegistry, error) {
	registry := &MemoryRegistry{tools: make(map[string]Tool)}
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func MustRegistry(tools ...Tool) *MemoryRegistry {
	registry, err := NewRegistry(tools...)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *MemoryRegistry) Register(tool Tool) error {
	if tool == nil || isNilTool(tool) {
		return fmt.Errorf("%w: nil tool", ErrInvalidTool)
	}
	name := strings.TrimSpace(tool.Name())
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrInvalidTool)
	}
	key := normalizeName(name)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}
	if _, exists := r.tools[key]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, name)
	}
	r.tools[key] = tool
	return nil
}

func isNilTool(value Tool) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (r *MemoryRegistry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[normalizeName(name)]
	return tool, ok
}

func (r *MemoryRegistry) Definitions() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]Definition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, Definition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Schema:      cloneMap(tool.Schema()),
		})
	}
	sort.Slice(definitions, func(i, j int) bool {
		return strings.ToLower(definitions[i].Name) < strings.ToLower(definitions[j].Name)
	})
	return definitions
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
