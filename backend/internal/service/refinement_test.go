package service

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// newTestDocRepo opens the relational DocumentDB implementation on a fresh
// Postgres schema (07:文档线唯一实现;夹具即生产 schema)。dataset id=1 预置,
// 与各测试里的 datasetID 1 对齐(documents.dataset_id 外键是真的)。
func newTestDocRepo(t *testing.T) *repository.RelationalDocRepo {
	t.Helper()
	db := testutil.DB(t, repository.RunMigrations)
	if err := db.Exec(`INSERT INTO datasets (name, modality) VALUES ('refine-fixture', 'text')`).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	return repository.NewRelationalDocRepo(db)
}

// insertTestDocWithQAPairs inserts a test document with the given QA pairs into the documents table.
func insertTestDocWithQAPairs(t *testing.T, docDB *repository.RelationalDocRepo, docKey string, datasetID uint, stage string, qaPairs []paymodel.QAPair, cursor int) {
	t.Helper()
	ctx := context.Background()

	pairs := []interface{}{}
	for _, p := range qaPairs {
		pairs = append(pairs, map[string]interface{}{
			"question":  p.Question,
			"answer":    p.Answer,
			"source":    p.Source,
			"confirmed": p.Confirmed,
			"span_text": p.SpanText,
		})
	}

	doc := paymodel.Document{
		DatasetID:        datasetID,
		DocKey:           docKey,
		Version:          1,
		IsActive:         true,
		UserID:           1,
		AnnotationStage:  stage,
		RefinementCursor: cursor,
		ETag:             "test-etag",
		Data: map[string]interface{}{
			"raw_text": "测试文档内容",
			"qa_pairs": pairs,
		},
		CreatedBy: 1,
		CreatedAt: paymodel.JSONTime{Time: time.Now()},
		UpdatedAt: paymodel.JSONTime{Time: time.Now()},
	}

	if err := docDB.InsertDocument(ctx, doc); err != nil {
		t.Fatalf("failed to insert test doc: %v", err)
	}
}

// generateRandomQAPairs creates N random QA pairs for testing.
func generateRandomQAPairs(rng *rand.Rand, n int) []paymodel.QAPair {
	pairs := make([]paymodel.QAPair, n)
	for i := 0; i < n; i++ {
		pairs[i] = paymodel.QAPair{
			Question:  fmt.Sprintf("测试问题编号 %d 随机值 %d", i, rng.Intn(10000)),
			Answer:    fmt.Sprintf("测试答案编号 %d 随机值 %d", i, rng.Intn(10000)),
			Source:    "llm",
			Confirmed: false,
		}
	}
	return pairs
}

// TestRefinementCursorNavigation_Property verifies cursor navigation behavior:
// (a) "next" confirms current QA and moves cursor to min(i+1, N-1)
// (b) "prev" moves cursor to max(i-1, 0)
// (c) "jump" to any valid index j sets cursor to j
//
// Feature: annotation-workflow-v2, Property 11: 精标游标导航
// **Validates: Requirements 4.4, 4.5, 4.6**
func TestRefinementCursorNavigation_Property(t *testing.T) {
	docDB := newTestDocRepo(t)
	dbRepo := newTestDB(t)

	svc := NewRefinementService(docDB, dbRepo, nil)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		// Generate 2-10 QA pairs
		n := rng.Intn(9) + 2
		qaPairs := generateRandomQAPairs(rng, n)
		docKey := fmt.Sprintf("refine-nav-%d", i)

		// Start with a random cursor position
		startCursor := rng.Intn(n)

		insertTestDocWithQAPairs(t, docDB, docKey, 1, StageRefining, qaPairs, startCursor)

		// Get the current etag
		doc, err := docDB.FindActiveDocument(ctx, nil, docKey, 0)
		if err != nil || doc == nil {
			t.Fatalf("iteration %d: failed to find doc: %v", i, err)
		}
		etag := doc.ETag

		// Test "next" operation
		resultDoc, err := svc.NavigateNext(ctx, docKey, etag)
		if err != nil {
			t.Errorf("iteration %d: NavigateNext failed: %v", i, err)
			continue
		}

		// Verify cursor moved to min(startCursor+1, n-1)
		expectedCursor := startCursor + 1
		if expectedCursor >= n {
			expectedCursor = n - 1
		}
		if resultDoc.RefinementCursor != expectedCursor {
			t.Errorf("iteration %d: after next, cursor=%d, expected=%d", i, resultDoc.RefinementCursor, expectedCursor)
		}

		// Verify the original cursor position QA pair is confirmed
		updatedDoc, _ := docDB.FindActiveDocument(ctx, nil, docKey, 0)
		updatedPairs := paymodel.ParseQAPairs(updatedDoc.Data["qa_pairs"])
		if !updatedPairs[startCursor].Confirmed {
			t.Errorf("iteration %d: after next, qa_pairs[%d].confirmed should be true", i, startCursor)
		}

		// Test "prev" operation
		etag = resultDoc.ETag
		currentCursor := resultDoc.RefinementCursor
		resultDoc, err = svc.NavigatePrev(ctx, docKey, etag)
		if err != nil {
			t.Errorf("iteration %d: NavigatePrev failed: %v", i, err)
			continue
		}

		expectedPrev := currentCursor - 1
		if expectedPrev < 0 {
			expectedPrev = 0
		}
		if resultDoc.RefinementCursor != expectedPrev {
			t.Errorf("iteration %d: after prev, cursor=%d, expected=%d", i, resultDoc.RefinementCursor, expectedPrev)
		}

		// Test "jump" operation
		etag = resultDoc.ETag
		jumpTarget := rng.Intn(n)
		resultDoc, err = svc.JumpTo(ctx, docKey, etag, jumpTarget)
		if err != nil {
			t.Errorf("iteration %d: JumpTo(%d) failed: %v", i, jumpTarget, err)
			continue
		}

		if resultDoc.RefinementCursor != jumpTarget {
			t.Errorf("iteration %d: after jump to %d, cursor=%d", i, jumpTarget, resultDoc.RefinementCursor)
		}

		// Cleanup(dataset 行保留,迭代复用)
		docDB.DB.Exec(`DELETE FROM documents`)
	}
}

func TestStartRefinementKeepsRefinedStage(t *testing.T) {
	docDB := newTestDocRepo(t)
	dbRepo := newTestDB(t)

	svc := NewRefinementService(docDB, dbRepo, nil)
	ctx := context.Background()

	docKey := "refined-open-does-not-rework"
	qaPairs := generateRandomQAPairs(rand.New(rand.NewSource(7)), 3)
	insertTestDocWithQAPairs(t, docDB, docKey, 1, StageRefined, qaPairs, 1)

	doc, err := svc.StartRefinementInDataset(ctx, 1, docKey)
	if err != nil {
		t.Fatalf("StartRefinementInDataset returned error: %v", err)
	}
	if doc.AnnotationStage != StageRefined {
		t.Fatalf("expected returned stage %q, got %q", StageRefined, doc.AnnotationStage)
	}

	stored, err := docDB.FindActiveDocument(ctx, ptrUint(1), docKey, 0)
	if err != nil {
		t.Fatalf("FindActiveDocument returned error: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored document")
	}
	if stored.AnnotationStage != StageRefined {
		t.Fatalf("expected stored stage %q, got %q", StageRefined, stored.AnnotationStage)
	}
}

// TestExitRefinementPreservesConfirmed_Property verifies that exiting refinement
// does not change the confirmed status of any QA pair.
//
// Feature: annotation-workflow-v2, Property 12: 退出精标保持已确认状态
// **Validates: Requirements 4.11**
func TestExitRefinementPreservesConfirmed_Property(t *testing.T) {
	docDB := newTestDocRepo(t)
	dbRepo := newTestDB(t)

	svc := NewRefinementService(docDB, dbRepo, nil)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < iterations; i++ {
		n := rng.Intn(8) + 2
		qaPairs := generateRandomQAPairs(rng, n)

		// Randomly confirm some QA pairs
		confirmedSet := make(map[int]bool)
		for j := 0; j < n; j++ {
			if rng.Float64() < 0.5 {
				qaPairs[j].Confirmed = true
				confirmedSet[j] = true
			}
		}

		docKey := fmt.Sprintf("refine-exit-%d", i)
		insertTestDocWithQAPairs(t, docDB, docKey, 1, StageRefining, qaPairs, 0)

		// Exit refinement
		doc, err := svc.ExitRefinement(ctx, docKey)
		if err != nil {
			t.Errorf("iteration %d: ExitRefinement failed: %v", i, err)
			continue
		}

		// Verify all confirmed states are preserved
		resultPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
		if len(resultPairs) != n {
			t.Errorf("iteration %d: expected %d pairs, got %d", i, n, len(resultPairs))
			continue
		}

		for j := 0; j < n; j++ {
			if confirmedSet[j] && !resultPairs[j].Confirmed {
				t.Errorf("iteration %d: qa_pairs[%d] was confirmed but became unconfirmed after exit", i, j)
			}
			if !confirmedSet[j] && resultPairs[j].Confirmed {
				t.Errorf("iteration %d: qa_pairs[%d] was not confirmed but became confirmed after exit", i, j)
			}
		}

		// Cleanup(dataset 行保留,迭代复用)
		docDB.DB.Exec(`DELETE FROM documents`)
	}
}

func ptrUint(v uint) *uint {
	return &v
}
