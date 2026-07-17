package builtin

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// Feature: text-annotation-platform, Property 4: 导出序列化 round-trip
// Validates: Requirements 15.5

// TestJSONLExportRoundTrip_Property verifies that serializing ExportDocuments to JSONL
// and then deserializing produces equivalent data.
func TestJSONLExportRoundTrip_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < 100; i++ {
		docs := generateRandomExportDocs(rng, rng.Intn(5)+1)

		// Serialize
		var buf bytes.Buffer
		p := &JSONLExportPlugin{}
		if err := p.Serialize(docs, &buf); err != nil {
			t.Fatalf("iteration %d: serialize failed: %v", i, err)
		}

		// Deserialize
		scanner := bufio.NewScanner(&buf)
		var parsed []jsonlRecord
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var rec jsonlRecord
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("iteration %d: deserialize failed: %v", i, err)
			}
			parsed = append(parsed, rec)
		}

		if len(parsed) != len(docs) {
			t.Fatalf("iteration %d: expected %d records, got %d", i, len(docs), len(parsed))
		}
		for j, doc := range docs {
			if parsed[j].DocKey != doc.DocKey {
				t.Errorf("iteration %d, record %d: doc_key mismatch", i, j)
			}
			if parsed[j].Version != doc.Version {
				t.Errorf("iteration %d, record %d: version mismatch", i, j)
			}
			if parsed[j].CreatedBy != doc.CreatedBy {
				t.Errorf("iteration %d, record %d: created_by mismatch", i, j)
			}
		}
	}
}

// TestCSVExportRoundTrip_Property verifies CSV export round-trip.
func TestCSVExportRoundTrip_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(99))

	for i := 0; i < 100; i++ {
		docs := generateRandomExportDocs(rng, rng.Intn(5)+1)

		// Serialize
		var buf bytes.Buffer
		p := &CSVExportPlugin{}
		if err := p.Serialize(docs, &buf); err != nil {
			t.Fatalf("iteration %d: serialize failed: %v", i, err)
		}

		// Deserialize
		r := csv.NewReader(&buf)
		records, err := r.ReadAll()
		if err != nil {
			t.Fatalf("iteration %d: CSV read failed: %v", i, err)
		}

		// First row is header
		if len(records) != len(docs)+1 {
			t.Fatalf("iteration %d: expected %d rows (header+data), got %d", i, len(docs)+1, len(records))
		}
		for j, doc := range docs {
			row := records[j+1]
			if row[0] != doc.DocKey {
				t.Errorf("iteration %d, record %d: doc_key mismatch: %q vs %q", i, j, row[0], doc.DocKey)
			}
			// Verify data column is valid JSON
			var dataMap map[string]interface{}
			if err := json.Unmarshal([]byte(row[2]), &dataMap); err != nil {
				t.Errorf("iteration %d, record %d: data column is not valid JSON: %v", i, j, err)
			}
		}
	}
}

func TestJSONExportRoundTrip_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(123))

	for i := 0; i < 100; i++ {
		docs := generateRandomExportDocs(rng, rng.Intn(5)+1)

		var buf bytes.Buffer
		p := &JSONExportPlugin{}
		if err := p.Serialize(docs, &buf); err != nil {
			t.Fatalf("iteration %d: serialize failed: %v", i, err)
		}

		var parsed []jsonlRecord
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("iteration %d: deserialize failed: %v", i, err)
		}
		if len(parsed) != len(docs) {
			t.Fatalf("iteration %d: expected %d records, got %d", i, len(docs), len(parsed))
		}
		for j, doc := range docs {
			if parsed[j].DocKey != doc.DocKey {
				t.Errorf("iteration %d, record %d: doc_key mismatch", i, j)
			}
			if parsed[j].Version != doc.Version {
				t.Errorf("iteration %d, record %d: version mismatch", i, j)
			}
			if parsed[j].CreatedBy != doc.CreatedBy {
				t.Errorf("iteration %d, record %d: created_by mismatch", i, j)
			}
		}
	}
}

func TestJSONLExportPlugin_EmptyDocs(t *testing.T) {
	p := &JSONLExportPlugin{}
	var buf bytes.Buffer
	if err := p.Serialize(nil, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output for nil docs, got %q", buf.String())
	}
}

func TestJSONExportPlugin_EmptyDocs(t *testing.T) {
	p := &JSONExportPlugin{}
	var buf bytes.Buffer
	if err := p.Serialize(nil, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed []jsonlRecord
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected empty array for nil docs, got %d records", len(parsed))
	}
}

func TestCSVExportPlugin_EmptyDocs(t *testing.T) {
	p := &CSVExportPlugin{}
	var buf bytes.Buffer
	if err := p.Serialize(nil, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still have header
	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 row (header only), got %d", len(records))
	}
}

func TestJSONExportPlugin_MetaFields(t *testing.T) {
	p := &JSONExportPlugin{}
	if p.FormatID() != "json" {
		t.Errorf("expected 'json', got %q", p.FormatID())
	}
	if p.FileExtension() != ".json" {
		t.Errorf("expected '.json', got %q", p.FileExtension())
	}
}

func TestJSONLExportPlugin_MetaFields(t *testing.T) {
	p := &JSONLExportPlugin{}
	if p.FormatID() != "jsonl" {
		t.Errorf("expected 'jsonl', got %q", p.FormatID())
	}
	if p.FileExtension() != ".jsonl" {
		t.Errorf("expected '.jsonl', got %q", p.FileExtension())
	}
}

func TestCSVExportPlugin_MetaFields(t *testing.T) {
	p := &CSVExportPlugin{}
	if p.FormatID() != "csv" {
		t.Errorf("expected 'csv', got %q", p.FormatID())
	}
	if p.FileExtension() != ".csv" {
		t.Errorf("expected '.csv', got %q", p.FileExtension())
	}
}

// generateRandomExportDocs creates random ExportDocument slices for property testing.
func generateRandomExportDocs(rng *rand.Rand, count int) []paymodel.ExportDocument {
	docs := make([]paymodel.ExportDocument, count)
	for i := range docs {
		docs[i] = paymodel.ExportDocument{
			DocKey:    fmt.Sprintf("doc_%d_%d", i, rng.Intn(10000)),
			Version:   rng.Intn(100) + 1,
			Data:      map[string]interface{}{"field": fmt.Sprintf("value_%d", rng.Intn(1000))},
			CreatedBy: uint(rng.Intn(100) + 1),
			UpdatedAt: time.Now(),
		}
	}
	return docs
}

// Ensure interfaces are satisfied (compile-time check).
var _ io.Writer = &bytes.Buffer{}
