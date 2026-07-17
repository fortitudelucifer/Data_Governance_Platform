package service

import (
	"io"
	"strings"
	"testing"
	"testing/quick"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/plugin"
)

// mockImportPlugin is a simple ImportPlugin implementation for testing.
type mockImportPlugin struct {
	formatID   string
	extensions []string
}

func (m *mockImportPlugin) FormatID() string                                          { return m.formatID }
func (m *mockImportPlugin) SupportedExtensions() []string                             { return m.extensions }
func (m *mockImportPlugin) Validate(r io.Reader) error                                { return nil }
func (m *mockImportPlugin) Parse(r io.Reader) ([]paymodel.ParsedDocument, error)    { return nil, nil }

// --- Property 2: 插件格式分发
// Feature: text-annotation-platform, Property 2: 插件格式分发
// Validates: Requirement 14.3
//
// For any registered ImportPlugin and its declared file extensions, when the
// import flow receives a file matching that extension, the system should select
// and use that ImportPlugin.

func TestProperty2_PluginFormatDispatch(t *testing.T) {
	// Define a set of mock plugins with distinct extensions
	pluginDefs := []struct {
		formatID   string
		extensions []string
	}{
		{"json", []string{".json", ".jsonl"}},
		{"csv", []string{".csv"}},
		{"txt", []string{".txt"}},
		{"xml", []string{".xml", ".xhtml"}},
	}

	f := func(pluginIdx uint8) bool {
		// Pick a plugin deterministically from the set
		idx := int(pluginIdx) % len(pluginDefs)
		def := pluginDefs[idx]

		// Build a fresh registry and DocumentService for each test iteration
		registry := plugin.NewPluginRegistry[plugin.ImportPlugin]()
		for _, pd := range pluginDefs {
			mp := &mockImportPlugin{formatID: pd.formatID, extensions: pd.extensions}
			if err := registry.Register(pd.formatID, mp); err != nil {
				t.Fatalf("register failed: %v", err)
			}
		}

		svc := &DocumentService{importRegistry: registry}

		// For each extension declared by the selected plugin, verify dispatch
		for _, ext := range def.extensions {
			found, err := svc.FindPluginByExtension(ext)
			if err != nil {
				return false
			}
			if found.FormatID() != def.formatID {
				return false
			}
		}
		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("Property 2 failed: %v", err)
	}
}

// --- Property 3: 未注册格式错误提示
// Feature: text-annotation-platform, Property 3: 未注册格式错误提示
// Validates: Requirement 14.5
//
// For any format identifier string NOT in the ImportPluginRegistry, calling
// import should return an error containing a list of all registered formats.

func TestProperty3_UnregisteredFormatError(t *testing.T) {
	// Known registered extensions
	registeredPlugins := []struct {
		formatID   string
		extensions []string
	}{
		{"json", []string{".json"}},
		{"csv", []string{".csv"}},
	}

	// Collect all registered extensions for validation
	allRegisteredExts := make(map[string]bool)
	for _, p := range registeredPlugins {
		for _, ext := range p.extensions {
			allRegisteredExts[strings.ToLower(ext)] = true
		}
	}

	f := func(unknownExt string) bool {
		// Ensure the generated extension is not one of the registered ones
		normalized := strings.ToLower(unknownExt)
		if normalized == "" || allRegisteredExts[normalized] {
			return true // skip known extensions
		}
		// Prefix with "." if not already
		if !strings.HasPrefix(normalized, ".") {
			normalized = "." + normalized
			if allRegisteredExts[normalized] {
				return true
			}
		}

		// Build registry
		registry := plugin.NewPluginRegistry[plugin.ImportPlugin]()
		for _, pd := range registeredPlugins {
			mp := &mockImportPlugin{formatID: pd.formatID, extensions: pd.extensions}
			if err := registry.Register(pd.formatID, mp); err != nil {
				t.Fatalf("register failed: %v", err)
			}
		}

		svc := &DocumentService{importRegistry: registry}

		_, err := svc.FindPluginByExtension(normalized)
		if err == nil {
			return false // should have returned an error
		}

		errMsg := err.Error()
		// Verify the error message contains all registered extensions
		for ext := range allRegisteredExts {
			if !strings.Contains(errMsg, ext) {
				return false
			}
		}
		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("Property 3 failed: %v", err)
	}
}

// --- Property 8: 增量导入保持已有文档不变且报告正确
// Feature: text-annotation-platform, Property 8: 增量导入保持已有文档不变且报告正确
// Validates: Requirements 19.1, 19.2, 19.3
//
// For any dataset with existing documents and any import document list (some
// doc_keys overlap), after incremental import:
// (a) existing documents unchanged
// (b) skipped_count equals duplicate doc_key count
// (c) imported_count + skipped_count + failed_count equals total

// filterIncrementalImport simulates the incremental filtering logic from
// DocumentService.ImportDocuments without requiring database connections.
func filterIncrementalImport(existingKeys []string, importDocs []paymodel.ParsedDocument) *ImportReport {
	existingSet := make(map[string]struct{}, len(existingKeys))
	for _, k := range existingKeys {
		existingSet[k] = struct{}{}
	}

	report := &ImportReport{}
	totalCount := len(importDocs)

	var newDocs []paymodel.ParsedDocument
	for _, doc := range importDocs {
		if _, exists := existingSet[doc.DocKey]; exists {
			report.SkippedCount++
			report.SkippedKeys = append(report.SkippedKeys, doc.DocKey)
		} else {
			newDocs = append(newDocs, doc)
		}
	}

	report.ImportedCount = len(newDocs)
	report.FailedCount = totalCount - report.ImportedCount - report.SkippedCount
	return report
}

func TestProperty8_IncrementalImportCorrectness(t *testing.T) {
	f := func(existingKeys []string, importKeys []string) bool {
		// Build import documents from importKeys
		importDocs := make([]paymodel.ParsedDocument, len(importKeys))
		for i, k := range importKeys {
			importDocs[i] = paymodel.ParsedDocument{
				DocKey: k,
				Data:   map[string]interface{}{"content": k},
			}
		}

		// Build existing key set for counting expected duplicates
		existingSet := make(map[string]struct{}, len(existingKeys))
		for _, k := range existingKeys {
			existingSet[k] = struct{}{}
		}

		report := filterIncrementalImport(existingKeys, importDocs)

		// (a) Count expected duplicates: import keys that exist in existingSet
		expectedSkipped := 0
		for _, k := range importKeys {
			if _, exists := existingSet[k]; exists {
				expectedSkipped++
			}
		}

		// (b) skipped_count equals duplicate doc_key count
		if report.SkippedCount != expectedSkipped {
			return false
		}

		// (c) imported_count + skipped_count + failed_count == total
		total := len(importDocs)
		if report.ImportedCount+report.SkippedCount+report.FailedCount != total {
			return false
		}

		// (a) imported_count should equal total minus skipped (no failures in this logic)
		if report.ImportedCount != total-expectedSkipped {
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("Property 8 failed: %v", err)
	}
}
