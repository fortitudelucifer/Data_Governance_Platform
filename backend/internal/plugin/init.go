package plugin

import "sync"

// Global registry instances for all plugin types.
// Written exactly once by InitRegistries; read by every consumer thereafter.
// sync.Once protects against accidental double-initialisation — a second call
// to the original (unguarded) InitRegistries would replace the registries with
// empty ones and silently drop every registration, so the body must run once.
var (
	ImportRegistry     *PluginRegistry[ImportPlugin]
	ExportRegistry     *PluginRegistry[ExportPlugin]
	TaskRegistry       *PluginRegistry[TaskPlugin]
	SamplingRegistry   *PluginRegistry[SamplingStrategy]
	ExtractionRegistry *PluginRegistry[ExtractionFilter]

	initOnce sync.Once
)

// InitRegistries creates the global registries without registering any plugins.
// Plugin registration is done in cmd/main.go (and runner/server.go) to avoid
// import cycles.
//
// Safe to call multiple times: the actual initialisation happens exactly once
// via sync.Once. Subsequent calls are no-ops and never reset the registries,
// so plugin registrations made between calls are preserved.
func InitRegistries() {
	initOnce.Do(func() {
		ImportRegistry = NewPluginRegistry[ImportPlugin]()
		ExportRegistry = NewPluginRegistry[ExportPlugin]()
		TaskRegistry = NewPluginRegistry[TaskPlugin]()
		SamplingRegistry = NewPluginRegistry[SamplingStrategy]()
		ExtractionRegistry = NewPluginRegistry[ExtractionFilter]()
	})
}

// resetRegistriesForTest clears state between test runs. It is intentionally
// lowercase so it cannot be called from outside the plugin package.
func resetRegistriesForTest() {
	ImportRegistry = nil
	ExportRegistry = nil
	TaskRegistry = nil
	SamplingRegistry = nil
	ExtractionRegistry = nil
	initOnce = sync.Once{}
}
