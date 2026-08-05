package service

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"text-annotation-platform/internal/model"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

func newDashboardTestRepos(t *testing.T) (*repository.DB, repository.DocumentDB) {
	t.Helper()
	db := testutil.DB(t, repository.RunMigrations)
	dbRepo := &repository.DB{DB: db}
	docDB := repository.NewRelationalDocRepo(db)
	return dbRepo, docDB
}

func makeQAPairs(n int) []interface{} {
	pairs := make([]interface{}, n)
	for i := 0; i < n; i++ {
		pairs[i] = map[string]interface{}{
			"question": fmt.Sprintf("q%d", i),
			"answer":   fmt.Sprintf("a%d", i),
			"source":   "llm",
		}
	}
	return pairs
}

// TestDashboardStatsConsistency_Property verifies:
// (a) stage_distribution sum equals doc_count
// (b) auto_annotated_count = auto_annotated + refining + refined
// (c) refined_count = refined stage count (reviewed is not generated)
// (d) dataset_id filter works correctly
//
// Feature: annotation-workflow-v2, Property 13: Dashboard 统计一致性
// **Validates: Requirements 5.3, 5.4, 5.5, 5.8**
func TestDashboardStatsConsistency_Property(t *testing.T) {
	dbRepo, docDB := newDashboardTestRepos(t)
	svc := NewDashboardService(dbRepo, docDB, false, time.Minute)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		// Create 1-3 datasets
		numDatasets := rng.Intn(3) + 1
		datasetIDs := make([]uint, numDatasets)
		for d := 0; d < numDatasets; d++ {
			ds := dbmodel.Dataset{Name: fmt.Sprintf("ds-%d-%d", i, d)}
			if err := dbRepo.DB.Create(&ds).Error; err != nil {
				t.Fatalf("create dataset failed: %v", err)
			}
			datasetIDs[d] = ds.ID
		}

		// Create random documents across datasets
		numDocs := rng.Intn(15) + 3
		expectedStages := make(map[string]int)
		expectedByDataset := make(map[uint]map[string]int)

		docs := make([]paymodel.Document, 0, numDocs)
		now := time.Now()
		for j := 0; j < numDocs; j++ {
			dsID := datasetIDs[rng.Intn(numDatasets)]
			stage := AllStages[rng.Intn(len(AllStages))]

			numQA := rng.Intn(4)
			doc := paymodel.Document{
				DatasetID:       dsID,
				DocKey:          fmt.Sprintf("dash-%d-%d", i, j),
				Version:         1,
				IsActive:        true,
				AnnotationStage: stage,
				Data:            map[string]interface{}{"qa_pairs": makeQAPairs(numQA)},
				CreatedAt:       paymodel.NewJSONTime(now),
				UpdatedAt:       paymodel.NewJSONTime(now),
			}
			docs = append(docs, doc)

			expectedStages[stage]++
			if expectedByDataset[dsID] == nil {
				expectedByDataset[dsID] = make(map[string]int)
			}
			expectedByDataset[dsID][stage]++
		}
		if err := docDB.InsertDocuments(ctx, docs); err != nil {
			t.Fatalf("insert documents failed: %v", err)
		}

		var stats *model.DashboardStats
		var err error

		// Sync counter columns before reading dashboard stats.
		if err = dbRepo.SyncAllDatasetCounters(ctx, docDB); err != nil {
			t.Errorf("iteration %d: sync counters failed: %v", i, err)
			goto cleanup
		}

		// Test global stats
		stats, err = svc.GetStats(ctx, nil, false)
		if err != nil {
			t.Errorf("iteration %d: GetStats failed: %v", i, err)
			goto cleanup
		}

		// (a) stage_distribution sum == doc_count
		{
			sum := 0
			for _, count := range stats.StageDistribution {
				sum += count
			}
			if sum != stats.DocCount {
				t.Errorf("iteration %d: stage sum %d != doc_count %d", i, sum, stats.DocCount)
			}
		}

		// (b) auto_annotated_count = auto_annotated + refining + refined
		{
			expected := stats.StageDistribution[StageAutoAnnotated] +
				stats.StageDistribution[StageRefining] +
				stats.StageDistribution[StageRefined]
			if stats.AutoAnnotatedCount != expected {
				t.Errorf("iteration %d: auto_annotated_count %d != expected %d", i, stats.AutoAnnotatedCount, expected)
			}
		}

		// (c) refined_count = refined stage count (reviewed not generated)
		if stats.RefinedCount != stats.StageDistribution[StageRefined] {
			t.Errorf("iteration %d: refined_count %d != stage[refined] %d", i, stats.RefinedCount, stats.StageDistribution[StageRefined])
		}

		// (d) Test dataset_id filter
		for _, dsID := range datasetIDs {
			dsIDCopy := dsID
			filteredStats, err := svc.GetStats(ctx, &dsIDCopy, false)
			if err != nil {
				t.Errorf("iteration %d: GetStats(dataset=%d) failed: %v", i, dsID, err)
				continue
			}
			expectedDocCount := 0
			for _, c := range expectedByDataset[dsID] {
				expectedDocCount += c
			}
			if filteredStats.DocCount != expectedDocCount {
				t.Errorf("iteration %d: filtered doc_count %d != expected %d for dataset %d", i, filteredStats.DocCount, expectedDocCount, dsID)
			}
		}

	cleanup:
		dbRepo.DB.Exec("DELETE FROM documents")
		dbRepo.DB.Exec("DELETE FROM datasets")
	}
}

// TestDailyTrendCorrectness_Property verifies:
// (a) returned entries count equals days
// (b) dates are within the last N days
// (c) refined_count matches actual refined documents for each day
//
// Feature: annotation-workflow-v2, Property 14: 每日趋势数据正确性
// **Validates: Requirements 5.7**
func TestDailyTrendCorrectness_Property(t *testing.T) {
	dbRepo, docDB := newDashboardTestRepos(t)
	svc := NewDashboardService(dbRepo, docDB, false, time.Minute)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		days := rng.Intn(10) + 1
		now := time.Now()

		ds := dbmodel.Dataset{Name: fmt.Sprintf("trend-ds-%d", i)}
		if err := dbRepo.DB.Create(&ds).Error; err != nil {
			t.Fatalf("create dataset failed: %v", err)
		}

		// Create refined documents spread across the last N days
		expectedByDate := make(map[string]int)
		numDocs := rng.Intn(10) + 1
		docs := make([]paymodel.Document, 0, numDocs)
		for j := 0; j < numDocs; j++ {
			dayOffset := rng.Intn(days)
			docTime := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location()).AddDate(0, 0, -dayOffset)
			dateStr := docTime.Format("2006-01-02")

			doc := paymodel.Document{
				DatasetID:       ds.ID,
				DocKey:          fmt.Sprintf("trend-%d-%d", i, j),
				Version:         1,
				IsActive:        true,
				AnnotationStage: StageRefined,
				Data:            map[string]interface{}{},
				CreatedAt:       paymodel.NewJSONTime(docTime),
				UpdatedAt:       paymodel.NewJSONTime(docTime),
			}
			docs = append(docs, doc)
			expectedByDate[dateStr]++
		}
		if err := docDB.InsertDocuments(ctx, docs); err != nil {
			t.Fatalf("insert documents failed: %v", err)
		}

		trends, err := svc.GetDailyTrend(ctx, days, nil, true)
		if err != nil {
			t.Errorf("iteration %d: GetDailyTrend failed: %v", i, err)
			goto cleanup
		}

		// (a) entries count equals days
		if len(trends) != days {
			t.Errorf("iteration %d: expected %d entries, got %d", i, days, len(trends))
			goto cleanup
		}

		// (b) dates are within range and (c) counts match
		{
			startDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
			for idx, trend := range trends {
				expectedDate := startDate.AddDate(0, 0, idx).Format("2006-01-02")
				if trend.Date != expectedDate {
					t.Errorf("iteration %d: trend[%d].date=%s, expected=%s", i, idx, trend.Date, expectedDate)
				}
				if trend.RefinedCount != expectedByDate[trend.Date] {
					t.Errorf("iteration %d: trend[%s].refined_count=%d, expected=%d", i, trend.Date, trend.RefinedCount, expectedByDate[trend.Date])
				}
			}
		}

	cleanup:
		dbRepo.DB.Exec("DELETE FROM documents")
		dbRepo.DB.Exec("DELETE FROM datasets")
	}
}
