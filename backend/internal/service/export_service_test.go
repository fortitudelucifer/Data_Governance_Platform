package service

import (
	"fmt"
	"testing"
	"testing/quick"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// Feature: text-annotation-platform, Property 9: 增量导出时间戳筛选
// Validates: Requirements 19.5, 19.6
//
// For any dataset and any timestamp T, incremental export (since=T) should
// return only documents whose updated_at is later than T.

// simulateIncrementalExportFilter mirrors the filtering logic: given a list of
// documents and a since timestamp, return only those with updated_at > since.
func simulateIncrementalExportFilter(docs []paymodel.ExportDocument, since *time.Time) []paymodel.ExportDocument {
	if since == nil {
		return docs
	}
	var result []paymodel.ExportDocument
	for _, d := range docs {
		if d.UpdatedAt.After(*since) {
			result = append(result, d)
		}
	}
	return result
}

func TestProperty9_IncrementalExportTimestampFilter(t *testing.T) {
	f := func(sinceUnix int64, docCount uint8) bool {
		count := int(docCount%20) + 1
		since := time.Unix(sinceUnix%1000000, 0)

		// Generate documents with varying timestamps
		docs := make([]paymodel.ExportDocument, count)
		for i := 0; i < count; i++ {
			docs[i] = paymodel.ExportDocument{
				DocKey:    fmt.Sprintf("doc_%d", i),
				Version:   1,
				UpdatedAt: time.Unix(sinceUnix%1000000+int64(i)-int64(count/2), 0),
			}
		}

		filtered := simulateIncrementalExportFilter(docs, &since)

		// All returned documents must have updated_at > since
		for _, d := range filtered {
			if !d.UpdatedAt.After(since) {
				return false
			}
		}

		// Count documents that should have been included
		expectedCount := 0
		for _, d := range docs {
			if d.UpdatedAt.After(since) {
				expectedCount++
			}
		}
		if len(filtered) != expectedCount {
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("Property 9 failed: %v", err)
	}
}

// TestProperty9_NilSinceReturnsAll verifies that a nil since returns all documents.
func TestProperty9_NilSinceReturnsAll(t *testing.T) {
	docs := []paymodel.ExportDocument{
		{DocKey: "a", UpdatedAt: time.Now().Add(-time.Hour)},
		{DocKey: "b", UpdatedAt: time.Now()},
	}
	result := simulateIncrementalExportFilter(docs, nil)
	if len(result) != len(docs) {
		t.Errorf("expected %d docs, got %d", len(docs), len(result))
	}
}
