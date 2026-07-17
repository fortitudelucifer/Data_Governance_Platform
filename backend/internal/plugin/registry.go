package plugin

import (
	"fmt"
	"sync"
)

// PluginRegistry is a generic, thread-safe registry for plugins keyed by string ID.
type PluginRegistry[T any] struct {
	mu      sync.RWMutex
	plugins map[string]T
}

// NewPluginRegistry creates a new empty PluginRegistry.
func NewPluginRegistry[T any]() *PluginRegistry[T] {
	return &PluginRegistry[T]{
		plugins: make(map[string]T),
	}
}

// Register adds a plugin to the registry. Returns an error if the ID is already registered.
func (r *PluginRegistry[T]) Register(id string, plugin T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[id]; exists {
		return fmt.Errorf("plugin with ID %q is already registered", id)
	}
	r.plugins[id] = plugin
	return nil
}

// Get retrieves a plugin by ID. Returns an error if not found.
func (r *PluginRegistry[T]) Get(id string) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[id]
	if !ok {
		var zero T
		return zero, fmt.Errorf("plugin with ID %q not found", id)
	}
	return p, nil
}

// List returns a copy of all registered plugins.
func (r *PluginRegistry[T]) List() map[string]T {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]T, len(r.plugins))
	for k, v := range r.plugins {
		result[k] = v
	}
	return result
}

// Has checks whether a plugin with the given ID is registered.
func (r *PluginRegistry[T]) Has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.plugins[id]
	return ok
}
