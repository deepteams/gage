// Package tools provides the built-in gage.Tool set (filesystem, shell, search,
// web) and a concurrency-safe ToolRegistry implementation.
package tools

import (
	"fmt"
	"sort"
	"sync"

	"github.com/deepteams/gage"
)

// MapRegistry is a concurrency-safe gage.ToolRegistry backed by a map.
type MapRegistry struct {
	mu    sync.RWMutex
	tools map[string]gage.Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *MapRegistry {
	return &MapRegistry{tools: map[string]gage.Tool{}}
}

// Register adds a tool, erroring on a duplicate name.
func (r *MapRegistry) Register(t gage.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tools: tool has empty name")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tools: tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

// MustRegister registers tools, panicking on error. Handy at startup.
func (r *MapRegistry) MustRegister(ts ...gage.Tool) {
	for _, t := range ts {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
}

// Unregister removes a tool by name, reporting whether it was present.
func (r *MapRegistry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.tools[name]
	delete(r.tools, name)
	return ok
}

// Get returns the tool with the given name.
func (r *MapRegistry) Get(name string) (gage.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all tools, sorted by name for determinism.
func (r *MapRegistry) List() []gage.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]gage.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Schemas returns the ToolSchema of every tool.
func (r *MapRegistry) Schemas() []gage.ToolSchema {
	tools := r.List()
	out := make([]gage.ToolSchema, 0, len(tools))
	for _, t := range tools {
		out = append(out, gage.SchemaOf(t))
	}
	return out
}
