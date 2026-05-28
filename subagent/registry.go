package subagent

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Registry interface {
	Register(spec SubAgentSpec) error
	Lookup(name string) (SubAgentSpec, bool)
	List() []SubAgentSpec
}

type MemoryRegistry struct {
	mu     sync.RWMutex
	agents map[string]SubAgentSpec
}

func NewRegistry(specs ...SubAgentSpec) (*MemoryRegistry, error) {
	registry := &MemoryRegistry{agents: map[string]SubAgentSpec{}}
	for _, spec := range specs {
		if err := registry.Register(spec); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func MustRegistry(specs ...SubAgentSpec) *MemoryRegistry {
	registry, err := NewRegistry(specs...)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *MemoryRegistry) Register(spec SubAgentSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	key := normalizeName(spec.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agents == nil {
		r.agents = map[string]SubAgentSpec{}
	}
	if _, ok := r.agents[key]; ok {
		return fmt.Errorf("duplicate subagent: %s", spec.Name)
	}
	r.agents[key] = spec
	return nil
}

func (r *MemoryRegistry) Lookup(name string) (SubAgentSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.agents[normalizeName(name)]
	return spec, ok
}

func (r *MemoryRegistry) List() []SubAgentSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SubAgentSpec, 0, len(r.agents))
	for _, spec := range r.agents {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
