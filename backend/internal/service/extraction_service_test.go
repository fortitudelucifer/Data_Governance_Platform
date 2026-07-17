package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/plugin/builtin"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

func newExtractionTestSetup(t *testing.T) (*ExtractionService, *repository.RelationalDocRepo, *repository.DB, *plugin.PluginRegistry[plugin.ExtractionFilter]) {
	t.Helper()

	db := testutil.DB(t, repository.RunMigrations)
	// 外键是真的：extraction_results.dataset_id → datasets(id)。种一行数据集，
	// 新 schema 的 identity 从 1 起，与各测试里的 DatasetID: 1 对齐。
	if err := db.Create(&dbmodel.Dataset{Name: "extraction-fixture", Modality: dbmodel.ModalityText}).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	dbRepo := &repository.DB{DB: db}
	docDB := repository.NewRelationalDocRepo(db)

	reg := plugin.NewPluginRegistry[plugin.ExtractionFilter]()
	reg.Register("import_time", plugin.ExtractionFilter(&builtin.ImportTimeFilter{}))
	reg.Register("keyword", plugin.ExtractionFilter(&builtin.KeywordFilter{}))
	reg.Register("case_occurrence_time", plugin.ExtractionFilter(&builtin.CaseTimeFilter{}))

	svc := NewExtractionService(reg, docDB, dbRepo)
	return svc, docDB, dbRepo, reg
}

func insertExtractionTestDocs(t *testing.T, docDB *repository.RelationalDocRepo, rng *rand.Rand, n int) []paymodel.Document {
	t.Helper()
	ctx := context.Background()
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	keywords := []string{"criminal", "civil", "administrative"}

	docs := make([]paymodel.Document, n)
	for i := 0; i < n; i++ {
		dayOffset := rng.Intn(365)
		docTime := baseTime.AddDate(0, 0, dayOffset)
		keyword := keywords[rng.Intn(len(keywords))]

		docs[i] = paymodel.Document{

			DatasetID: 1,
			DocKey:    fmt.Sprintf("ext-doc-%d", i),
			Version:   1,
			IsActive:  true,
			Data: map[string]interface{}{
				"raw_text":  fmt.Sprintf("This is a %s case document %d", keyword, i),
				"case_time": docTime.Format(time.RFC3339),
			},
			CreatedAt: paymodel.JSONTime{Time: docTime},
			UpdatedAt: paymodel.JSONTime{Time: docTime},
		}
		docDB.InsertDocument(ctx, docs[i])
	}
	return docs
}

// TestFilterCommutativity_Property verifies that applying filter A then B
// produces the same result set as applying B then A.
//
// Feature: annotation-workflow-v2, Property 16: 杩囨护鍣ㄧ粍鍚堜氦鎹㈠緥
// **Validates: Requirements 6.6**
func TestFilterCommutativity_Property(t *testing.T) {
	ctx := context.Background()
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	importTimeFilter := &builtin.ImportTimeFilter{}
	keywordFilter := &builtin.KeywordFilter{}
	keywords := []string{"criminal", "civil", "administrative"}

	for i := 0; i < iterations; i++ {
		numDocs := rng.Intn(15) + 5
		baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

		// Generate docs
		docs := make([]paymodel.Document, numDocs)
		for j := 0; j < numDocs; j++ {
			dayOffset := rng.Intn(365)
			docTime := baseTime.AddDate(0, 0, dayOffset)
			keyword := keywords[rng.Intn(len(keywords))]
			docs[j] = paymodel.Document{

				DatasetID: 1,
				DocKey:    fmt.Sprintf("comm-%d-%d", i, j),
				IsActive:  true,
				Data:      map[string]interface{}{"raw_text": fmt.Sprintf("This is a %s case", keyword)},
				CreatedAt: paymodel.JSONTime{Time: docTime},
			}
		}

		// Random time range
		startDay := rng.Intn(180)
		endDay := startDay + rng.Intn(180) + 1
		start := baseTime.AddDate(0, 0, startDay)
		end := baseTime.AddDate(0, 0, endDay)
		keyword := keywords[rng.Intn(len(keywords))]

		timeParams := map[string]interface{}{
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		}
		kwParams := map[string]interface{}{"keyword": keyword}

		// A then B
		resultAB, _ := importTimeFilter.Apply(ctx, docs, timeParams)
		resultAB, _ = keywordFilter.Apply(ctx, resultAB, kwParams)

		// B then A
		resultBA, _ := keywordFilter.Apply(ctx, docs, kwParams)
		resultBA, _ = importTimeFilter.Apply(ctx, resultBA, timeParams)

		// Compare sets
		setAB := docKeySet(resultAB)
		setBA := docKeySet(resultBA)

		if !equalSets(setAB, setBA) {
			t.Errorf("iteration %d: A->B result differs from B->A result", i)
		}
	}
}

func docKeySet(docs []paymodel.Document) []string {
	keys := make([]string, len(docs))
	for i, d := range docs {
		keys[i] = d.DocKey
	}
	sort.Strings(keys)
	return keys
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestExtractionResultCompleteness_Property verifies:
// (a) matched_count equals doc_keys length
// (b) total_count equals dataset document count
// (c) round-trip: query by ID returns same filter_config and doc_keys
//
// Feature: annotation-workflow-v2, Property 17: 鎶藉彇缁撴灉瀹屾暣鎬?
// **Validates: Requirements 6.8, 6.9**
func TestExtractionResultCompleteness_Property(t *testing.T) {
	svc, docDB, _, _ := newExtractionTestSetup(t)

	ctx := context.Background()
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		numDocs := rng.Intn(15) + 3
		insertExtractionTestDocs(t, docDB, rng, numDocs)

		keyword := []string{"criminal", "civil", "administrative"}[rng.Intn(3)]

		req := ExtractionRequest{
			DatasetID: 1,
			Name:      fmt.Sprintf("test-extraction-%d", i),
			Filters: []ExtractionFilterConfig{
				{Type: "keyword", Params: map[string]interface{}{"keyword": keyword}},
			},
		}

		result, err := svc.Execute(ctx, req)
		if err != nil {
			t.Errorf("iteration %d: Execute failed: %v", i, err)
			goto cleanup
		}

		// (a) matched_count == doc_keys length
		{
			var docKeys []string
			json.Unmarshal([]byte(result.DocKeys), &docKeys)
			if result.MatchedCount != len(docKeys) {
				t.Errorf("iteration %d: matched_count %d != doc_keys length %d", i, result.MatchedCount, len(docKeys))
			}
		}

		// (b) total_count == dataset document count
		if result.TotalCount != numDocs {
			t.Errorf("iteration %d: total_count %d != actual docs %d", i, result.TotalCount, numDocs)
		}

		// (c) round-trip
		{
			fetched, err := svc.GetResultByID(ctx, result.ID)
			if err != nil {
				t.Errorf("iteration %d: GetResultByID failed: %v", i, err)
				goto cleanup
			}
			if fetched.FilterConfig != result.FilterConfig {
				t.Errorf("iteration %d: filter_config mismatch", i)
			}
			if fetched.DocKeys != result.DocKeys {
				t.Errorf("iteration %d: doc_keys mismatch", i)
			}
		}

	cleanup:
		docDB.DB.Exec(`DELETE FROM documents`)
	}
}
