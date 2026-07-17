package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/testutil"
)

func TestRelationalUpdateDocumentRefinementCursorSyncsStageIntoData(t *testing.T) {
	db := testutil.DB(t, RunMigrations)
	// documents.dataset_id 的外键是真的：先种父行（identity 从 1 起）。
	if err := db.Create(&dbmodel.Dataset{Name: "doc-fixture", Modality: dbmodel.ModalityText}).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}

	data := map[string]interface{}{"annotation_stage": "refining", "text": "case text"}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal fixture data: %v", err)
	}
	now := time.Now()
	doc := dbmodel.Document{
		DatasetID:        1,
		DocKey:           "doc-stage-sync",
		Version:          1,
		IsActive:         true,
		AnnotationStage:  "refining",
		RefinementCursor: 0,
		ETag:             "etag-1",
		Data:             dbmodel.JSON(raw),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatalf("create fixture document: %v", err)
	}

	repo := NewRelationalDocRepo(db)
	datasetID := uint(1)
	if err := repo.UpdateDocumentRefinementCursor(context.Background(), &datasetID, "doc-stage-sync", "etag-1", 0, "etag-2", "refined"); err != nil {
		t.Fatalf("UpdateDocumentRefinementCursor: %v", err)
	}

	var stored dbmodel.Document
	if err := db.Where("dataset_id = ? AND doc_key = ? AND is_active = ?", datasetID, "doc-stage-sync", true).First(&stored).Error; err != nil {
		t.Fatalf("load stored document: %v", err)
	}
	if stored.AnnotationStage != "refined" {
		t.Fatalf("expected annotation_stage column refined, got %q", stored.AnnotationStage)
	}
	var storedData map[string]interface{}
	if err := json.Unmarshal(stored.Data, &storedData); err != nil {
		t.Fatalf("unmarshal stored data: %v", err)
	}
	if storedData["annotation_stage"] != "refined" {
		t.Fatalf("expected data.annotation_stage refined, got %#v", storedData["annotation_stage"])
	}
}
