package service

import (
	"context"
	"testing"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

func newCounterTestRepos(t *testing.T) (*repository.DB, repository.DocumentDB) {
	t.Helper()
	db := testutil.DB(t, repository.RunMigrations)
	dbRepo := &repository.DB{DB: db}
	docDB := repository.NewRelationalDocRepo(db)
	return dbRepo, docDB
}

func TestDashboardCountersSyncAndRead(t *testing.T) {
	dbRepo, docDB := newCounterTestRepos(t)
	svc := NewDashboardService(dbRepo, docDB, false, time.Minute)
	ctx := context.Background()

	// Create two datasets
	ds1 := dbmodel.Dataset{Name: "counter-ds-1"}
	ds2 := dbmodel.Dataset{Name: "counter-ds-2"}
	if err := dbRepo.DB.Create(&ds1).Error; err != nil {
		t.Fatalf("create ds1 failed: %v", err)
	}
	if err := dbRepo.DB.Create(&ds2).Error; err != nil {
		t.Fatalf("create ds2 failed: %v", err)
	}

	// Insert documents with known stage distribution
	now := time.Now()
	docs := []paymodel.Document{
		{DatasetID: ds1.ID, DocKey: "ds1-a1", Version: 1, IsActive: true, AnnotationStage: StageAutoAnnotated, Data: map[string]interface{}{"qa_pairs": []interface{}{}}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
		{DatasetID: ds1.ID, DocKey: "ds1-r1", Version: 1, IsActive: true, AnnotationStage: StageRefining, Data: map[string]interface{}{"qa_pairs": []interface{}{}}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
		{DatasetID: ds1.ID, DocKey: "ds1-r2", Version: 1, IsActive: true, AnnotationStage: StageRefined, Data: map[string]interface{}{"qa_pairs": []interface{}{map[string]interface{}{"question": "q", "answer": "a"}}}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
		{DatasetID: ds2.ID, DocKey: "ds2-n1", Version: 1, IsActive: true, AnnotationStage: StageNotAnnotated, Data: map[string]interface{}{}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
	}
	if err := docDB.InsertDocuments(ctx, docs); err != nil {
		t.Fatalf("insert documents failed: %v", err)
	}

	// Sync counters
	if err := dbRepo.SyncAllDatasetCounters(ctx, docDB); err != nil {
		t.Fatalf("sync all counters failed: %v", err)
	}

	// Verify global stats read from counters
	stats, err := svc.GetStats(ctx, nil, false)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.DatasetCount != 2 {
		t.Errorf("dataset_count = %d, want 2", stats.DatasetCount)
	}
	if stats.DocCount != 4 {
		t.Errorf("doc_count = %d, want 4", stats.DocCount)
	}
	wantStages := map[string]int{
		StageNotAnnotated:  1,
		StageAutoAnnotated: 1,
		StageRefining:      1,
		StageRefined:       1,
	}
	for stage, want := range wantStages {
		if got := stats.StageDistribution[stage]; got != want {
			t.Errorf("stage_distribution[%s] = %d, want %d", stage, got, want)
		}
	}
	wantAuto := 3 // auto_annotated + refining + refined
	if stats.AutoAnnotatedCount != wantAuto {
		t.Errorf("auto_annotated_count = %d, want %d", stats.AutoAnnotatedCount, wantAuto)
	}
	wantRefined := 1 // refined (no reviewed docs)
	if stats.RefinedCount != wantRefined {
		t.Errorf("refined_count = %d, want %d", stats.RefinedCount, wantRefined)
	}
	if stats.QATotal != 1 {
		t.Errorf("qa_total = %d, want 1", stats.QATotal)
	}

	// Verify per-dataset stats
	ds1ID := ds1.ID
	ds1Stats, err := svc.GetStats(ctx, &ds1ID, false)
	if err != nil {
		t.Fatalf("GetStats(ds1) failed: %v", err)
	}
	if ds1Stats.DocCount != 3 {
		t.Errorf("ds1 doc_count = %d, want 3", ds1Stats.DocCount)
	}

	ds2ID := ds2.ID
	ds2Stats, err := svc.GetStats(ctx, &ds2ID, false)
	if err != nil {
		t.Fatalf("GetStats(ds2) failed: %v", err)
	}
	if ds2Stats.DocCount != 1 {
		t.Errorf("ds2 doc_count = %d, want 1", ds2Stats.DocCount)
	}
}

func TestDashboardCountersAfterDelete(t *testing.T) {
	dbRepo, docDB := newCounterTestRepos(t)
	svc := NewDashboardService(dbRepo, docDB, false, time.Minute)
	ctx := context.Background()

	ds := dbmodel.Dataset{Name: "delete-ds"}
	if err := dbRepo.DB.Create(&ds).Error; err != nil {
		t.Fatalf("create ds failed: %v", err)
	}

	now := time.Now()
	docs := []paymodel.Document{
		{DatasetID: ds.ID, DocKey: "d1", Version: 1, IsActive: true, AnnotationStage: StageRefined, Data: map[string]interface{}{}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
		{DatasetID: ds.ID, DocKey: "d2", Version: 1, IsActive: true, AnnotationStage: StageRefined, Data: map[string]interface{}{}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
		{DatasetID: ds.ID, DocKey: "d3", Version: 1, IsActive: true, AnnotationStage: StageRefined, Data: map[string]interface{}{}, CreatedAt: paymodel.NewJSONTime(now), UpdatedAt: paymodel.NewJSONTime(now)},
	}
	if err := docDB.InsertDocuments(ctx, docs); err != nil {
		t.Fatalf("insert documents failed: %v", err)
	}

	// Sync after insert
	if err := dbRepo.SyncAllDatasetCounters(ctx, docDB); err != nil {
		t.Fatalf("sync counters failed: %v", err)
	}

	// Delete one document
	if _, err := docDB.DeleteDocumentByKey(ctx, ds.ID, "d2"); err != nil {
		t.Fatalf("delete document failed: %v", err)
	}
	if err := dbRepo.SyncDatasetCounters(ctx, docDB, ds.ID); err != nil {
		t.Fatalf("sync counters after delete failed: %v", err)
	}

	dsID := ds.ID
	stats, err := svc.GetStats(ctx, &dsID, false)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	if stats.DocCount != 2 {
		t.Errorf("doc_count after delete = %d, want 2", stats.DocCount)
	}
	if stats.RefinedCount != 2 {
		t.Errorf("refined_count after delete = %d, want 2", stats.RefinedCount)
	}
}
