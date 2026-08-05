package builtin

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

func generateTestDocs(rng *rand.Rand, n int) []paymodel.Document {
	docs := make([]paymodel.Document, n)
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	keywords := []string{"criminal", "civil", "administrative", "fraud", "theft"}

	for i := 0; i < n; i++ {
		dayOffset := rng.Intn(365)
		docTime := baseTime.AddDate(0, 0, dayOffset)
		keyword := keywords[rng.Intn(len(keywords))]

		docs[i] = paymodel.Document{
			ID:        fmt.Sprintf("doc-id-%d", i),
			DatasetID: 1,
			DocKey:    fmt.Sprintf("doc-%d", i),
			Version:   1,
			IsActive:  true,
			Data: map[string]interface{}{
				"raw_text":      fmt.Sprintf("This is a %s case document number %d", keyword, i),
				"case_time":     docTime.Format(time.RFC3339),
				"judgment_time": docTime.AddDate(0, 1, 0).Format(time.RFC3339),
			},
			CreatedAt: paymodel.JSONTime{Time: docTime},
			UpdatedAt: paymodel.JSONTime{Time: docTime},
		}
	}
	return docs
}

// TestExtractionFilterCorrectness_Property verifies:
// (a) ratio filter returns count in [0, ceil(ratio*total)]
// (b) import_time filter returns docs with created_at in range
// (c) keyword filter returns docs containing the keyword
//
// Feature: annotation-workflow-v2, Property 15: 抽取过滤器正确性
// **Validates: Requirements 6.2, 6.3, 6.4, 6.5**
func TestExtractionFilterCorrectness_Property(t *testing.T) {
	ctx := context.Background()
	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	ratioFilter := &RatioFilter{}
	importTimeFilter := &ImportTimeFilter{}
	keywordFilter := &KeywordFilter{}
	caseTimeFilter := &CaseTimeFilter{}
	judgmentTimeFilter := &JudgmentTimeFilter{}

	for i := 0; i < iterations; i++ {
		numDocs := rng.Intn(20) + 5
		docs := generateTestDocs(rng, numDocs)

		// (a) Ratio filter
		ratio := rng.Float64()
		result, err := ratioFilter.Apply(ctx, docs, map[string]interface{}{"ratio": ratio})
		if err != nil {
			t.Errorf("iteration %d: ratio filter failed: %v", i, err)
			continue
		}
		maxExpected := int(math.Ceil(ratio * float64(numDocs)))
		if len(result) > maxExpected+1 { // +1 for rounding
			t.Errorf("iteration %d: ratio=%.2f, got %d docs, max expected %d", i, ratio, len(result), maxExpected)
		}

		// (b) Import time filter
		startDay := rng.Intn(180)
		endDay := startDay + rng.Intn(180) + 1
		baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		start := baseTime.AddDate(0, 0, startDay)
		end := baseTime.AddDate(0, 0, endDay)

		result, err = importTimeFilter.Apply(ctx, docs, map[string]interface{}{
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		})
		if err != nil {
			t.Errorf("iteration %d: import_time filter failed: %v", i, err)
			continue
		}
		for _, doc := range result {
			if doc.CreatedAt.Before(start) || doc.CreatedAt.After(end) {
				t.Errorf("iteration %d: import_time returned doc with created_at=%v outside [%v, %v]", i, doc.CreatedAt, start, end)
			}
		}

		// (c) Keyword filter
		keywords := []string{"criminal", "civil", "administrative", "fraud", "theft"}
		keyword := keywords[rng.Intn(len(keywords))]
		result, err = keywordFilter.Apply(ctx, docs, map[string]interface{}{"keyword": keyword})
		if err != nil {
			t.Errorf("iteration %d: keyword filter failed: %v", i, err)
			continue
		}
		for _, doc := range result {
			text, _ := doc.Data["raw_text"].(string)
			if !strings.Contains(text, keyword) {
				t.Errorf("iteration %d: keyword filter returned doc without keyword '%s'", i, keyword)
			}
		}

		// (d) Case time filter
		result, err = caseTimeFilter.Apply(ctx, docs, map[string]interface{}{
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		})
		if err != nil {
			t.Errorf("iteration %d: case_time filter failed: %v", i, err)
			continue
		}
		for _, doc := range result {
			timeStr, _ := doc.Data["case_time"].(string)
			ct, _ := time.Parse(time.RFC3339, timeStr)
			if ct.Before(start) || ct.After(end) {
				t.Errorf("iteration %d: case_time returned doc outside range", i)
			}
		}

		// (e) Judgment time filter
		result, err = judgmentTimeFilter.Apply(ctx, docs, map[string]interface{}{
			"start": start.Format(time.RFC3339),
			"end":   end.AddDate(0, 2, 0).Format(time.RFC3339),
		})
		if err != nil {
			t.Errorf("iteration %d: judgment_time filter failed: %v", i, err)
			continue
		}
		for _, doc := range result {
			timeStr, _ := doc.Data["judgment_time"].(string)
			jt, _ := time.Parse(time.RFC3339, timeStr)
			if jt.Before(start) || jt.After(end.AddDate(0, 2, 0)) {
				t.Errorf("iteration %d: judgment_time returned doc outside range", i)
			}
		}
	}
}
