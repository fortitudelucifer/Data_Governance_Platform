package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// RefinementService handles the second-layer manual refinement workflow.
type RefinementService struct {
	docRepo      repository.DocumentDB
	dbRepo      *repository.DB
	dashboardCache DashboardCacheInvalidator
	cache          *cache.Cache // nil = 无 Redis
}

// NewRefinementService creates a new RefinementService.
func NewRefinementService(docRepo repository.DocumentDB, dbRepo *repository.DB, dashboardCache DashboardCacheInvalidator) *RefinementService {
	return &RefinementService{
		docRepo:      docRepo,
		dbRepo:      dbRepo,
		dashboardCache: dashboardCache,
	}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *RefinementService) WithCache(c *cache.Cache) *RefinementService {
	s.cache = c
	return s
}

func (s *RefinementService) invalidateDashboard(datasetID uint) {
	if s.dashboardCache != nil {
		s.dashboardCache.InvalidateDataset(datasetID)
	}
}

// invalidateDocCache 失效文档缓存（精标写操作后必须调用，否则有 Redis 时
// 工作台会读到精标前的旧文档/旧阶段）。datasetID 为 nil 时无对应缓存键，跳过。
func (s *RefinementService) invalidateDocCache(ctx context.Context, datasetID *uint, docKey string) {
	if s.cache == nil || datasetID == nil {
		return
	}
	did := strconv.FormatUint(uint64(*datasetID), 10)
	s.cache.Delete(ctx, "doc:active:"+did+":"+docKey)
	s.cache.ScanDelete(ctx, "doc:list:"+did+":*")
}

// getAnnotatorName looks up the username by ID. If not found, returns "Unknown".
func (s *RefinementService) getAnnotatorName(ctx context.Context, userID uint) string {
	user, err := s.dbRepo.FindUserByID(ctx, userID)
	if err != nil || user == nil {
		return "Unknown"
	}
	return user.Username
}

// StartRefinement transitions a document from auto_annotated to refining and initializes cursor.
func (s *RefinementService) StartRefinement(ctx context.Context, docKey string) (*paymodel.Document, error) {
	return s.startRefinement(ctx, nil, docKey)
}

func (s *RefinementService) StartRefinementInDataset(ctx context.Context, datasetID uint, docKey string) (*paymodel.Document, error) {
	return s.startRefinement(ctx, &datasetID, docKey)
}

func (s *RefinementService) startRefinement(ctx context.Context, datasetID *uint, docKey string) (*paymodel.Document, error) {
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to find document: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: %s", docKey)
	}

	currentStage := doc.AnnotationStage
	if currentStage == "" {
		currentStage = StageNotAnnotated
	}

	// Allow opening an existing refinement session without changing stage.
	// Finished documents must stay finished; explicit rework goes through
	// /documents/:key/reannotate instead of this auto-start endpoint.
	if currentStage == StageRefining || currentStage == StageRefined {
		return doc, nil
	}

	if err := ValidateStageTransition(currentStage, StageRefining); err != nil {
		return nil, err
	}

	etag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentRefinementCursor(ctx, datasetID, docKey, "", 0, etag, StageRefining)
	if err != nil {
		return nil, fmt.Errorf("failed to update stage: %w", err)
	}

	doc.AnnotationStage = StageRefining
	doc.RefinementCursor = 0
	doc.ETag = etag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)
	s.invalidateDocCache(ctx, datasetID, docKey)
	s.invalidateDashboard(doc.DatasetID)
	return doc, nil
}

// NavigateNext confirms the current QA pair and moves cursor forward.
func (s *RefinementService) NavigateNext(ctx context.Context, docKey, etag string) (*paymodel.Document, error) {
	return s.navigateNext(ctx, nil, docKey, etag)
}

func (s *RefinementService) NavigateNextInDataset(ctx context.Context, datasetID uint, docKey, etag string) (*paymodel.Document, error) {
	return s.navigateNext(ctx, &datasetID, docKey, etag)
}

func (s *RefinementService) navigateNext(ctx context.Context, datasetID *uint, docKey, etag string) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	qaPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	n := len(qaPairs)
	if n == 0 {
		return nil, fmt.Errorf("document has no QA pairs")
	}

	cursor := doc.RefinementCursor
	qaPairs[cursor].Confirmed = true

	newCursor := cursor + 1
	if newCursor >= n {
		newCursor = n - 1
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentQAPairsAndCursor(ctx, datasetID, docKey, etag, qaPairs, newCursor, newEtag, nil, "")
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	doc.Data["qa_pairs"] = qaPairs
	doc.RefinementCursor = newCursor
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	s.invalidateDocCache(ctx, datasetID, docKey)
	return doc, nil
}

// NavigatePrev moves cursor backward.
func (s *RefinementService) NavigatePrev(ctx context.Context, docKey, etag string) (*paymodel.Document, error) {
	return s.navigatePrev(ctx, nil, docKey, etag)
}

func (s *RefinementService) NavigatePrevInDataset(ctx context.Context, datasetID uint, docKey, etag string) (*paymodel.Document, error) {
	return s.navigatePrev(ctx, &datasetID, docKey, etag)
}

func (s *RefinementService) navigatePrev(ctx context.Context, datasetID *uint, docKey, etag string) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	cursor := doc.RefinementCursor
	newCursor := cursor - 1
	if newCursor < 0 {
		newCursor = 0
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentRefinementCursor(ctx, datasetID, docKey, etag, newCursor, newEtag, "")
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	doc.RefinementCursor = newCursor
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	s.invalidateDocCache(ctx, datasetID, docKey)
	return doc, nil
}

// JumpTo sets cursor to a specific index.
func (s *RefinementService) JumpTo(ctx context.Context, docKey, etag string, index int) (*paymodel.Document, error) {
	return s.jumpTo(ctx, nil, docKey, etag, index)
}

func (s *RefinementService) JumpToInDataset(ctx context.Context, datasetID uint, docKey, etag string, index int) (*paymodel.Document, error) {
	return s.jumpTo(ctx, &datasetID, docKey, etag, index)
}

func (s *RefinementService) jumpTo(ctx context.Context, datasetID *uint, docKey, etag string, index int) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	qaPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	n := len(qaPairs)
	if n == 0 {
		return nil, fmt.Errorf("document has no QA pairs")
	}

	if index < 0 || index >= n {
		return nil, fmt.Errorf("index out of bounds: %d, valid range [0, %d)", index, n)
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentRefinementCursor(ctx, datasetID, docKey, etag, index, newEtag, "")
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	doc.RefinementCursor = index
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	s.invalidateDocCache(ctx, datasetID, docKey)
	return doc, nil
}

// CompleteRefinement transitions from refining to refined.
func (s *RefinementService) CompleteRefinement(ctx context.Context, docKey, etag string) (*paymodel.Document, error) {
	return s.completeRefinement(ctx, nil, docKey, etag)
}

func (s *RefinementService) CompleteRefinementInDataset(ctx context.Context, datasetID uint, docKey, etag string) (*paymodel.Document, error) {
	return s.completeRefinement(ctx, &datasetID, docKey, etag)
}

func (s *RefinementService) completeRefinement(ctx context.Context, datasetID *uint, docKey, etag string) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	if err := ValidateStageTransition(StageRefining, StageRefined); err != nil {
		return nil, err
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentRefinementCursor(ctx, datasetID, docKey, etag, doc.RefinementCursor, newEtag, StageRefined)
	if err != nil {
		return nil, fmt.Errorf("failed to update stage: %w", err)
	}

	doc.AnnotationStage = StageRefined
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)
	s.invalidateDocCache(ctx, datasetID, docKey)
	s.invalidateDashboard(doc.DatasetID)
	return doc, nil
}

// ExitRefinement returns to document list without changing confirmed states.
func (s *RefinementService) ExitRefinement(ctx context.Context, docKey string) (*paymodel.Document, error) {
	return s.exitRefinement(ctx, nil, docKey)
}

func (s *RefinementService) ExitRefinementInDataset(ctx context.Context, datasetID uint, docKey string) (*paymodel.Document, error) {
	return s.exitRefinement(ctx, &datasetID, docKey)
}

func (s *RefinementService) exitRefinement(ctx context.Context, datasetID *uint, docKey string) (*paymodel.Document, error) {
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to find document: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: %s", docKey)
	}
	return doc, nil
}

// EditQAPair updates a QA pair at the given index.
func (s *RefinementService) EditQAPair(ctx context.Context, docKey, etag string, index int, pair paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	return s.editQAPair(ctx, nil, docKey, etag, index, pair, userID)
}

func (s *RefinementService) EditQAPairInDataset(ctx context.Context, datasetID uint, docKey, etag string, index int, pair paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	return s.editQAPair(ctx, &datasetID, docKey, etag, index, pair, userID)
}

func (s *RefinementService) editQAPair(ctx context.Context, datasetID *uint, docKey, etag string, index int, pair paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	qaPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	n := len(qaPairs)
	if index < 0 || index >= n {
		return nil, fmt.Errorf("index out of bounds: %d, valid range [0, %d)", index, n)
	}

	qaPairs[index].Question = pair.Question
	qaPairs[index].Answer = pair.Answer
	qaPairs[index].QuestionKey = pair.QuestionKey
	qaPairs[index].Category = pair.Category
	qaPairs[index].Evidence = pair.Evidence
	qaPairs[index].Confidence = pair.Confidence
	qaPairs[index].Reason = pair.Reason
	if pair.SpanText != "" {
		qaPairs[index].SpanText = pair.SpanText
	}
	if pair.SpanStart != nil {
		qaPairs[index].SpanStart = pair.SpanStart
	}
	if pair.SpanEnd != nil {
		qaPairs[index].SpanEnd = pair.SpanEnd
	}
	if pair.TextField != "" {
		qaPairs[index].TextField = pair.TextField
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentQAPairsAndCursor(ctx, datasetID, docKey, etag, qaPairs, doc.RefinementCursor, newEtag, &userID, s.getAnnotatorName(ctx, userID))
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	doc.Data["qa_pairs"] = qaPairs
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)
	s.invalidateDocCache(ctx, datasetID, docKey)
	s.invalidateDashboard(doc.DatasetID)
	return doc, nil
}

// DeleteQAPair removes a QA pair at the given index and adjusts cursor.
func (s *RefinementService) DeleteQAPair(ctx context.Context, docKey, etag string, index int, userID uint) (*paymodel.Document, error) {
	return s.deleteQAPair(ctx, nil, docKey, etag, index, userID)
}

func (s *RefinementService) DeleteQAPairInDataset(ctx context.Context, datasetID uint, docKey, etag string, index int, userID uint) (*paymodel.Document, error) {
	return s.deleteQAPair(ctx, &datasetID, docKey, etag, index, userID)
}

func (s *RefinementService) deleteQAPair(ctx context.Context, datasetID *uint, docKey, etag string, index int, userID uint) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	qaPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	n := len(qaPairs)
	if index < 0 || index >= n {
		return nil, fmt.Errorf("index out of bounds: %d, valid range [0, %d)", index, n)
	}

	qaPairs = append(qaPairs[:index], qaPairs[index+1:]...)

	cursor := doc.RefinementCursor
	if len(qaPairs) == 0 {
		cursor = 0
	} else if cursor >= len(qaPairs) {
		cursor = len(qaPairs) - 1
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentQAPairsAndCursor(ctx, datasetID, docKey, etag, qaPairs, cursor, newEtag, &userID, s.getAnnotatorName(ctx, userID))
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	doc.Data["qa_pairs"] = qaPairs
	doc.RefinementCursor = cursor
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)
	s.invalidateDocCache(ctx, datasetID, docKey)
	s.invalidateDashboard(doc.DatasetID)
	return doc, nil
}

// AddQAPair appends a new QA pair with source="manual".
func (s *RefinementService) AddQAPair(ctx context.Context, docKey, etag string, pair paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	return s.addQAPair(ctx, nil, docKey, etag, pair, userID)
}

func (s *RefinementService) AddQAPairInDataset(ctx context.Context, datasetID uint, docKey, etag string, pair paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	return s.addQAPair(ctx, &datasetID, docKey, etag, pair, userID)
}

func (s *RefinementService) addQAPair(ctx context.Context, datasetID *uint, docKey, etag string, pair paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	pair.Source = "manual"
	qaPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	qaPairs = append(qaPairs, pair)

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentQAPairsAndCursor(ctx, datasetID, docKey, etag, qaPairs, doc.RefinementCursor, newEtag, &userID, s.getAnnotatorName(ctx, userID))
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}

	doc.Data["qa_pairs"] = qaPairs
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)
	s.invalidateDocCache(ctx, datasetID, docKey)
	s.invalidateDashboard(doc.DatasetID)
	return doc, nil
}

// BulkUpdateQAPairs replaces the entire list of QA pairs. Useful for undo/redo and batch operations.
func (s *RefinementService) BulkUpdateQAPairs(ctx context.Context, docKey, etag string, pairs []paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	return s.bulkUpdateQAPairs(ctx, nil, docKey, etag, pairs, userID)
}

func (s *RefinementService) BulkUpdateQAPairsInDataset(ctx context.Context, datasetID uint, docKey, etag string, pairs []paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	return s.bulkUpdateQAPairs(ctx, &datasetID, docKey, etag, pairs, userID)
}

func (s *RefinementService) bulkUpdateQAPairs(ctx context.Context, datasetID *uint, docKey, etag string, pairs []paymodel.QAPair, userID uint) (*paymodel.Document, error) {
	doc, err := s.loadAndValidateRefining(ctx, datasetID, docKey, etag)
	if err != nil {
		return nil, err
	}

	// Ensure cursor is within new boundaries
	cursor := doc.RefinementCursor
	if len(pairs) == 0 {
		cursor = 0
	} else if cursor >= len(pairs) {
		cursor = len(pairs) - 1
	}

	newEtag, err := generateETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	err = s.docRepo.UpdateDocumentQAPairsAndCursor(ctx, datasetID, docKey, etag, pairs, cursor, newEtag, &userID, s.getAnnotatorName(ctx, userID))
	if err != nil {
		return nil, fmt.Errorf("bulk update failed: %w", err)
	}

	doc.Data["qa_pairs"] = pairs
	doc.RefinementCursor = cursor
	doc.ETag = newEtag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)
	s.invalidateDocCache(ctx, datasetID, docKey)
	s.invalidateDashboard(doc.DatasetID)
	return doc, nil
}

// loadAndValidateRefining loads a document and validates it's in refining state with matching etag.
func (s *RefinementService) loadAndValidateRefining(ctx context.Context, datasetID *uint, docKey, etag string) (*paymodel.Document, error) {
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to find document: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: %s", docKey)
	}

	if doc.AnnotationStage != StageRefining {
		return nil, fmt.Errorf("document not in refining state, current: %s", doc.AnnotationStage)
	}

	if etag != "" && doc.ETag != etag {
		return nil, fmt.Errorf("document has been modified, please refresh")
	}

	return doc, nil
}
