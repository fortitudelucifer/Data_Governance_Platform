package plugin

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"
)

// Feature: text-annotation-platform, Property 1: 插件注册 round-trip
// Validates: Requirements 14.2, 15.2, 16.3, 17.3

// TestRegistryRoundTrip_Property verifies that for any plugin registered with a
// unique ID, Get returns that same plugin and List includes it.
func TestRegistryRoundTrip_Property(t *testing.T) {
	f := func(id string) bool {
		if id == "" {
			return true // skip empty IDs
		}
		reg := NewPluginRegistry[string]()
		value := "plugin_" + id

		err := reg.Register(id, value)
		if err != nil {
			return false
		}

		// Get should return the registered value
		got, err := reg.Get(id)
		if err != nil || got != value {
			return false
		}

		// Has should return true
		if !reg.Has(id) {
			return false
		}

		// List should contain the plugin
		all := reg.List()
		v, ok := all[id]
		if !ok || v != value {
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("Property 1 failed: %v", err)
	}
}

// TestRegistryDuplicateID verifies that registering the same ID twice returns an error.
func TestRegistryDuplicateID(t *testing.T) {
	reg := NewPluginRegistry[int]()
	if err := reg.Register("dup", 1); err != nil {
		t.Fatalf("first register should succeed: %v", err)
	}
	if err := reg.Register("dup", 2); err == nil {
		t.Fatal("second register with same ID should return error")
	}
}

// TestRegistryGetNotFound verifies that Get returns an error for unregistered IDs.
func TestRegistryGetNotFound(t *testing.T) {
	reg := NewPluginRegistry[string]()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("Get on unregistered ID should return error")
	}
}

// TestRegistryMultiplePlugins verifies registering and retrieving multiple plugins.
func TestRegistryMultiplePlugins(t *testing.T) {
	f := func(count uint8) bool {
		n := int(count)%20 + 1 // 1..20
		reg := NewPluginRegistry[int]()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("plugin_%d", i)
			if err := reg.Register(id, i); err != nil {
				return false
			}
		}
		all := reg.List()
		if len(all) != n {
			return false
		}
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("plugin_%d", i)
			got, err := reg.Get(id)
			if err != nil || got != i {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 100, Rand: rand.New(rand.NewSource(42))}); err != nil {
		t.Errorf("Multiple plugins property failed: %v", err)
	}
}
