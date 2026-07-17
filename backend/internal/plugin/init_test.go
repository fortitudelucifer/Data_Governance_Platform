package plugin

import (
	"io"
	"sync"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// dummyImportPlugin satisfies ImportPlugin minimally for these tests.
type dummyImportPlugin struct{ id string }

func (d *dummyImportPlugin) FormatID() string             { return d.id }
func (d *dummyImportPlugin) SupportedExtensions() []string { return []string{".dummy"} }
func (d *dummyImportPlugin) Validate(_ io.Reader) error    { return nil }
func (d *dummyImportPlugin) Parse(_ io.Reader) ([]paymodel.ParsedDocument, error) {
	return nil, nil
}

func TestInitRegistries_DoubleCallPreservesRegistrations(t *testing.T) {
	resetRegistriesForTest()
	defer resetRegistriesForTest()

	InitRegistries()
	if err := ImportRegistry.Register("a", ImportPlugin(&dummyImportPlugin{id: "a"})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Second InitRegistries must be a no-op: the prior registration survives.
	InitRegistries()
	if _, err := ImportRegistry.Get("a"); err != nil {
		t.Errorf("registration lost after second InitRegistries: %v", err)
	}
}

func TestInitRegistries_AllRegistriesInitialisedExactlyOnce(t *testing.T) {
	resetRegistriesForTest()
	defer resetRegistriesForTest()

	InitRegistries()
	importP := ImportRegistry
	exportP := ExportRegistry
	taskP := TaskRegistry
	samplingP := SamplingRegistry
	extractionP := ExtractionRegistry

	// Second call must not allocate new registry instances.
	InitRegistries()
	if ImportRegistry != importP {
		t.Error("ImportRegistry pointer changed across InitRegistries calls")
	}
	if ExportRegistry != exportP {
		t.Error("ExportRegistry pointer changed across InitRegistries calls")
	}
	if TaskRegistry != taskP {
		t.Error("TaskRegistry pointer changed across InitRegistries calls")
	}
	if SamplingRegistry != samplingP {
		t.Error("SamplingRegistry pointer changed across InitRegistries calls")
	}
	if ExtractionRegistry != extractionP {
		t.Error("ExtractionRegistry pointer changed across InitRegistries calls")
	}
}

func TestInitRegistries_ConcurrentCallsAreSafe(t *testing.T) {
	resetRegistriesForTest()
	defer resetRegistriesForTest()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			InitRegistries()
		}()
	}
	wg.Wait()

	if ImportRegistry == nil || TaskRegistry == nil {
		t.Fatal("registries not initialised after concurrent calls")
	}
}
