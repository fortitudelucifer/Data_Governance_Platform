package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/repository"
)

const (
	docListTTL           = 3 * time.Minute
	docActiveTTL         = 5 * time.Minute
	importBatchIDDataKey = "_import_batch_id"
	importOrderDataKey   = "_import_order"
)

// ImportReport contains the results of a document import operation.
type ImportReport struct {
	ImportedCount int      `json:"imported_count"`
	SkippedCount  int      `json:"skipped_count"`
	FailedCount   int      `json:"failed_count"`
	SkippedKeys   []string `json:"skipped_keys,omitempty"`
}

// DocumentService handles business logic for document import, versioning, and retrieval.
type DocumentService struct {
	dbRepo         *repository.DB
	docRepo      repository.DocumentDB
	importRegistry *plugin.PluginRegistry[plugin.ImportPlugin]
	dashboardCache DashboardCacheInvalidator
	cache          *cache.Cache // nil = no Redis
}

// NewDocumentService creates a DocumentService with the given dependencies.
func NewDocumentService(
	dbRepo *repository.DB,
	docRepo repository.DocumentDB,
	importRegistry *plugin.PluginRegistry[plugin.ImportPlugin],
	dashboardCache DashboardCacheInvalidator,
) *DocumentService {
	return &DocumentService{
		dbRepo:         dbRepo,
		docRepo:      docRepo,
		importRegistry: importRegistry,
		dashboardCache: dashboardCache,
	}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *DocumentService) WithCache(c *cache.Cache) *DocumentService {
	s.cache = c
	return s
}

// invalidateDocCaches removes the doc list cache for a dataset and, if docKey
// is non-empty, the specific active-document entry.
func (s *DocumentService) invalidateDocCaches(ctx context.Context, datasetID uint, docKey string) {
	if s.cache == nil {
		return
	}
	did := strconv.FormatUint(uint64(datasetID), 10)
	s.cache.ScanDelete(ctx, "doc:list:"+did+":*")
	if docKey != "" {
		s.cache.Delete(ctx, "doc:active:"+did+":"+docKey)
	}
}

func (s *DocumentService) invalidateDashboard(datasetID uint) {
	if s.dashboardCache != nil {
		s.dashboardCache.InvalidateDataset(datasetID)
	}
}

// generateETag produces a random 16-character hex string for use as an optimistic lock token.
func generateETag() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate etag failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func resolveAnnotationStage(data map[string]interface{}, fallback string) string {
	if data != nil {
		if s, ok := data["annotation_stage"].(string); ok && s != "" {
			return s
		}
	}
	if fallback != "" {
		return fallback
	}
	return StageNotAnnotated
}

func (s *DocumentService) syncDatasetCounters(ctx context.Context, datasetID uint) error {
	if err := s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, datasetID); err != nil {
		return fmt.Errorf("sync dataset counters failed: %w", err)
	}
	return nil
}

// SetDocumentsDeadline 批量设置/清除文档截止时间（管理员分配任务用，存于 data.deadline）。
func (s *DocumentService) SetDocumentsDeadline(ctx context.Context, datasetID uint, docKeys []string, deadline string) error {
	if len(docKeys) == 0 {
		return nil
	}
	if err := s.docRepo.SetDocumentsDeadline(ctx, datasetID, docKeys, deadline); err != nil {
		return err
	}
	s.invalidateDocCaches(ctx, datasetID, "")
	return nil
}

// AssignDocuments 批量设置/清除文档任务元数据（标注员、审核员、截止时间）。
func (s *DocumentService) AssignDocuments(ctx context.Context, datasetID uint, docKeys []string, assigneeID, reviewerID *uint, deadlineAt *string) error {
	if len(docKeys) == 0 {
		return nil
	}
	if assigneeID == nil && reviewerID == nil && deadlineAt == nil {
		return fmt.Errorf("no assignment fields provided")
	}
	if err := s.docRepo.AssignDocuments(ctx, datasetID, docKeys, assigneeID, reviewerID, deadlineAt); err != nil {
		return err
	}
	s.invalidateDocCaches(ctx, datasetID, "")
	return nil
}

// FindPluginByExtension iterates the import registry and returns the first plugin
// whose SupportedExtensions list contains the given extension.
// If no match is found, an error listing all supported formats is returned.
func (s *DocumentService) FindPluginByExtension(ext string) (plugin.ImportPlugin, error) {
	ext = strings.ToLower(ext)
	plugins := s.importRegistry.List()

	var supportedExts []string
	for _, p := range plugins {
		for _, e := range p.SupportedExtensions() {
			supportedExts = append(supportedExts, e)
			if strings.ToLower(e) == ext {
				return p, nil
			}
		}
	}

	return nil, fmt.Errorf("不支持的格式 '%s'，当前支持: %s", ext, strings.Join(supportedExts, ", "))
}

// ImportDocuments parses and imports documents from a file into the specified dataset.
// The mode parameter controls duplicate handling: "incremental" skips existing doc_keys,
// while any other value (e.g. "full") imports all parsed documents.
func (s *DocumentService) ImportDocuments(
	ctx context.Context,
	datasetID uint,
	file io.Reader,
	filename string,
	mode string,
	userID uint,
) (*ImportReport, error) {
	// Determine file extension and find matching plugin
	ext := filepath.Ext(filename)
	if ext == "" {
		return nil, fmt.Errorf("filename has no extension: %s", filename)
	}

	importPlugin, err := s.FindPluginByExtension(ext)
	if err != nil {
		return nil, err
	}

	// Buffer the file content so we can read it twice (Validate + Parse)
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, file); err != nil {
		return nil, fmt.Errorf("read file failed: %w", err)
	}
	fileBytes := buf.Bytes()

	// Parse (includes format validation); skip an extra full-file Validate pass to reduce import latency.
	parsed, err := importPlugin.Parse(bytes.NewReader(fileBytes))
	if err != nil {
		return nil, fmt.Errorf("文件解析失败: %w", err)
	}

	report := &ImportReport{}
	totalCount := len(parsed)

	// Filter out documents whose doc_key already exists in this dataset.
	// This prevents duplicate active rows for the same key, which can otherwise
	// cause surprising delete/update behavior.
	if len(parsed) > 0 {
		docKeys := make([]string, len(parsed))
		for i, p := range parsed {
			docKeys[i] = strings.TrimSpace(p.DocKey)
		}

		existingKeys, err := s.docRepo.FindExistingDocKeys(ctx, datasetID, docKeys, userID)
		if err != nil {
			return nil, fmt.Errorf("check existing doc keys failed: %w", err)
		}

		existingSet := make(map[string]struct{}, len(existingKeys))
		for _, k := range existingKeys {
			existingSet[k] = struct{}{}
		}

		// Deduplicate doc_keys in the same import file to avoid duplicate-key
		// failures on the documents unique index (doc_key + version).
		seenInBatch := make(map[string]struct{}, len(parsed))
		filtered := make([]paymodel.ParsedDocument, 0, len(parsed))
		for _, p := range parsed {
			p.DocKey = strings.TrimSpace(p.DocKey)
			if _, exists := existingSet[p.DocKey]; exists {
				report.SkippedCount++
				report.SkippedKeys = append(report.SkippedKeys, p.DocKey)
				continue
			}
			if _, dup := seenInBatch[p.DocKey]; dup {
				report.SkippedCount++
				report.SkippedKeys = append(report.SkippedKeys, p.DocKey)
				continue
			}
			seenInBatch[p.DocKey] = struct{}{}
			filtered = append(filtered, p)
		}
		parsed = filtered
	}

	// Convert ParsedDocument to paymodel.Document. Stamp import metadata so
	// JSON/JSONL rows can keep their original file order even when doc_key is
	// generated from content.
	now := time.Now()
	importBatchID := fmt.Sprintf("%d_%s", datasetID, now.UTC().Format("20060102T150405.000000000Z"))
	docs := make([]paymodel.Document, 0, len(parsed))
	for i, p := range parsed {
		if p.Data == nil {
			p.Data = make(map[string]interface{})
		}
		stage := resolveAnnotationStage(p.Data, "")
		p.Data[importBatchIDDataKey] = importBatchID
		p.Data[importOrderDataKey] = i + 1
		p.Data["annotation_stage"] = stage

		etag, err := generateETag()
		if err != nil {
			return nil, err
		}
		docs = append(docs, paymodel.Document{
			DatasetID:       datasetID,
			DocKey:          p.DocKey,
			Version:         1,
			IsActive:        true,
			UserID:          userID,
			AnnotationStage: stage,
			Data:            p.Data,
			CreatedBy:       userID,
			CreatedAt:       paymodel.NewJSONTime(now),
			UpdatedAt:       paymodel.NewJSONTime(now),
			ETag:            etag,
		})
	}

	// Batch insert document rows
	if len(docs) > 0 {
		if err := s.docRepo.InsertDocuments(ctx, docs); err != nil {
			return nil, fmt.Errorf("文档写入失败: %w", err)
		}
	}

	report.ImportedCount = len(docs)
	report.FailedCount = totalCount - report.ImportedCount - report.SkippedCount

	// Update doc_count in the relational DB using real active distinct doc_keys.
	if report.ImportedCount > 0 {
		if err := s.syncDatasetCounters(ctx, datasetID); err != nil {
			// Rollback inserted document rows on counter-update failure
			docKeys := make([]string, len(docs))
			for i, d := range docs {
				docKeys[i] = d.DocKey
			}
			if rbErr := s.docRepo.DeleteDocumentsByKeys(ctx, datasetID, docKeys); rbErr != nil {
				slog.Error("document: rollback DeleteDocumentsByKeys failed", "dataset_id", datasetID, "error", rbErr)
			}
			return nil, fmt.Errorf("数据集更新失败，已回滚: %w", err)
		}
		s.invalidateDashboard(datasetID)
		s.invalidateDocCaches(ctx, datasetID, "") // import changes list, no specific docKey
	}

	return report, nil
}

// GetActiveDocument returns the active (is_active=true) version of a document.
func (s *DocumentService) GetActiveDocument(ctx context.Context, docKey string, userID uint) (*paymodel.Document, error) {
	return s.docRepo.FindActiveDocument(ctx, nil, docKey, userID)
}

func (s *DocumentService) GetActiveDocumentInDataset(ctx context.Context, datasetID uint, docKey string, userID uint) (*paymodel.Document, error) {
	key := fmt.Sprintf("doc:active:%d:%s", datasetID, docKey)
	if s.cache != nil {
		var v paymodel.Document
		if hit, _ := s.cache.GetJSON(ctx, key, &v); hit {
			return &v, nil
		}
	}
	doc, err := s.docRepo.FindActiveDocument(ctx, &datasetID, docKey, userID)
	if err != nil {
		return nil, err
	}
	if doc != nil && s.cache != nil {
		s.cache.SetJSON(ctx, key, doc, docActiveTTL)
	}
	return doc, nil
}

// GetVersionHistory returns all versions of a document ordered by version descending.
func (s *DocumentService) GetVersionHistory(ctx context.Context, docKey string, userID uint) ([]paymodel.Document, error) {
	return s.docRepo.FindVersionHistory(ctx, nil, docKey, userID)
}

func (s *DocumentService) GetVersionHistoryInDataset(ctx context.Context, datasetID uint, docKey string, userID uint) ([]paymodel.Document, error) {
	return s.docRepo.FindVersionHistory(ctx, &datasetID, docKey, userID)
}

// UpdateDocument creates a new version of a document using Copy-on-Write.
// It deactivates the current active version and inserts a new document with version+1.
func (s *DocumentService) UpdateDocument(
	ctx context.Context,
	docKey string,
	data map[string]interface{},
	createdBy uint,
) (*paymodel.Document, error) {
	return s.updateDocument(ctx, nil, docKey, data, createdBy)
}

func (s *DocumentService) UpdateDocumentInDataset(
	ctx context.Context,
	datasetID uint,
	docKey string,
	data map[string]interface{},
	createdBy uint,
) (*paymodel.Document, error) {
	return s.updateDocument(ctx, &datasetID, docKey, data, createdBy)
}

func (s *DocumentService) updateDocument(
	ctx context.Context,
	datasetID *uint,
	docKey string,
	data map[string]interface{},
	createdBy uint,
) (*paymodel.Document, error) {
	// Find the current active document
	current, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, createdBy)
	if err != nil {
		return nil, fmt.Errorf("find active document failed: %w", err)
	}
	if current == nil {
		return nil, fmt.Errorf("document not found: doc_key=%s", docKey)
	}

	stage := resolveAnnotationStage(data, current.AnnotationStage)
	if data == nil {
		data = make(map[string]interface{})
	}
	data["annotation_stage"] = stage

	// Deactivate current version
	if err := s.docRepo.DeactivateVersion(ctx, datasetID, docKey, current.Version); err != nil {
		return nil, fmt.Errorf("deactivate version failed: %w", err)
	}

	// Generate new etag
	etag, err := generateETag()
	if err != nil {
		return nil, err
	}

	history, err := s.docRepo.FindVersionHistory(ctx, datasetID, docKey, createdBy)
	if err != nil {
		return nil, fmt.Errorf("find version history failed: %w", err)
	}
	nextVersion := current.Version + 1
	for _, item := range history {
		if item.Version >= nextVersion {
			nextVersion = item.Version + 1
		}
	}

	// Insert new version
	now := time.Now()
	newDoc := paymodel.Document{
		DatasetID:       current.DatasetID,
		DocKey:          docKey,
		Version:         nextVersion,
		IsActive:        true,
		UserID:          current.UserID,
		AnnotationStage: stage,
		Data:            data,
		CreatedBy:       createdBy,
		CreatedAt:       paymodel.NewJSONTime(now),
		UpdatedAt:       paymodel.NewJSONTime(now),
		ETag:            etag,
		AnnotatorName:   s.getAnnotatorName(ctx, createdBy),
	}

	if err := s.docRepo.InsertDocument(ctx, newDoc); err != nil {
		return nil, fmt.Errorf("insert new version failed: %w", err)
	}

	// Keep per-dataset dashboard counters in sync. Single-dataset aggregation is
	// fast thanks to the dataset_id index and avoids request-time full scans.
	_ = s.syncDatasetCounters(ctx, current.DatasetID)

	s.invalidateDashboard(current.DatasetID)
	s.invalidateDocCaches(ctx, current.DatasetID, docKey)

	return &newDoc, nil
}

// DirectCompleteRefinement skips the "refining" stage and marks a document as "refined" directly.
func (s *DocumentService) DirectCompleteRefinement(ctx context.Context, docKey string, userID uint) (*paymodel.Document, error) {
	return s.directCompleteRefinement(ctx, nil, docKey, userID)
}

func (s *DocumentService) DirectCompleteRefinementInDataset(ctx context.Context, datasetID uint, docKey string, userID uint) (*paymodel.Document, error) {
	return s.directCompleteRefinement(ctx, &datasetID, docKey, userID)
}

func (s *DocumentService) directCompleteRefinement(ctx context.Context, datasetID *uint, docKey string, userID uint) (*paymodel.Document, error) {
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, userID)
	if err != nil {
		return nil, fmt.Errorf("find active document failed: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: doc_key=%s", docKey)
	}

	currentStage := doc.AnnotationStage
	if currentStage == "" {
		currentStage = StageNotAnnotated
	}

	// Validate we can jump to Refined directly
	if currentStage != StageRefined {
		// Update the annotation stage on the raw map
		doc.Data["annotation_stage"] = StageRefined

		// Reuse standard COW logic
		updatedDoc, err := s.updateDocument(ctx, datasetID, docKey, doc.Data, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to complete refinement: %w", err)
		}

		// Sync local stage tracker if somehow disconnected
		updatedDoc.AnnotationStage = StageRefined
		return updatedDoc, nil
	}

	return doc, nil
}

// ReAnnotateDocument rolls back a refined document's status to refining.
func (s *DocumentService) ReAnnotateDocument(ctx context.Context, docKey string, userID uint) (*paymodel.Document, error) {
	return s.reAnnotateDocument(ctx, nil, docKey, userID)
}

func (s *DocumentService) ReAnnotateDocumentInDataset(ctx context.Context, datasetID uint, docKey string, userID uint) (*paymodel.Document, error) {
	return s.reAnnotateDocument(ctx, &datasetID, docKey, userID)
}

func (s *DocumentService) reAnnotateDocument(ctx context.Context, datasetID *uint, docKey string, userID uint) (*paymodel.Document, error) {
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, userID)
	if err != nil {
		return nil, fmt.Errorf("find active document failed: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: doc_key=%s", docKey)
	}

	// Update the annotation stage on the raw map back to refining
	doc.Data["annotation_stage"] = StageRefining

	// Reuse standard COW logic
	updatedDoc, err := s.updateDocument(ctx, datasetID, docKey, doc.Data, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to re-annotate document: %w", err)
	}

	// Sync local stage tracker
	updatedDoc.AnnotationStage = StageRefining
	return updatedDoc, nil
}

// PaginatedDocumentsResult is the paginated response for document listing.
type PaginatedDocumentsResult struct {
	Items    []paymodel.Document `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

// GetDocumentsByDatasetPaginated returns a paginated list of active documents for a dataset.
// Result is cached under "doc:list:{datasetID}:{page}:{pageSize}:{query}" for 3 minutes.
// userID does not affect the query result (the query ignores it).
func (s *DocumentService) GetDocumentsByDatasetPaginated(ctx context.Context, datasetID uint, page, pageSize int, userID uint, query string) (*PaginatedDocumentsResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	query = strings.TrimSpace(query)

	did := strconv.FormatUint(uint64(datasetID), 10)
	key := fmt.Sprintf("doc:list:%s:%d:%d:%s", did, page, pageSize, cacheKeyPart(query))
	if s.cache != nil {
		var v PaginatedDocumentsResult
		if hit, _ := s.cache.GetJSON(ctx, key, &v); hit {
			return &v, nil
		}
	}

	result, err := s.docRepo.FindDocumentsByDatasetPaginated(ctx, datasetID, page, pageSize, userID, query)
	if err != nil {
		return nil, err
	}
	res := &PaginatedDocumentsResult{
		Items:    result.Items,
		Total:    result.Total,
		Page:     page,
		PageSize: pageSize,
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, key, res, docListTTL)
	}
	return res, nil
}

func cacheKeyPart(s string) string {
	if s == "" {
		return "_"
	}
	return strings.NewReplacer(":", "_", "*", "_", "?", "_", " ", "_").Replace(s)
}

// GetDocumentsByDataset returns all active documents for a dataset.
func (s *DocumentService) GetDocumentsByDataset(ctx context.Context, datasetID uint, userID uint) ([]paymodel.Document, error) {
	return s.docRepo.FindDocumentsByDataset(ctx, datasetID, nil, userID)
}

// DeleteDocument deletes a single document (all versions) by doc_key and updates doc_count.
func (s *DocumentService) DeleteDocument(ctx context.Context, datasetID uint, docKey string) error {
	deleted, err := s.docRepo.DeleteDocumentByKey(ctx, datasetID, docKey)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return fmt.Errorf("文档不存在: %s", docKey)
	}

	if err := s.syncDatasetCounters(ctx, datasetID); err != nil {
		return err
	}
	s.invalidateDashboard(datasetID)
	s.invalidateDocCaches(ctx, datasetID, "")
	return nil
}

// DeleteDocumentsBatch deletes multiple documents by doc_keys and updates doc_count.
func (s *DocumentService) DeleteDocumentsBatch(ctx context.Context, datasetID uint, docKeys []string) (int, error) {
	if len(docKeys) == 0 {
		return 0, nil
	}
	deletedCount := 0
	for _, key := range docKeys {
		n, err := s.docRepo.DeleteDocumentByKey(ctx, datasetID, key)
		if err != nil {
			return deletedCount, fmt.Errorf("删除文档 %s 失败: %w", key, err)
		}
		if n > 0 {
			deletedCount++
		}
	}
	if deletedCount > 0 {
		if err := s.syncDatasetCounters(ctx, datasetID); err != nil {
			return deletedCount, fmt.Errorf("更新文档数量失败: %w", err)
		}
		s.invalidateDashboard(datasetID)
		s.invalidateDocCaches(ctx, datasetID, "")
	}
	return deletedCount, nil
}

// DeleteDocumentsRange deletes active documents in the given index range [startIdx, endIdx] (0-indexed inclusive).
func (s *DocumentService) DeleteDocumentsRange(ctx context.Context, datasetID uint, startIdx, endIdx int64) (int, error) {
	if startIdx < 0 || endIdx < startIdx {
		return 0, fmt.Errorf("无效的范围参数 / invalid range parameters")
	}

	limit := endIdx - startIdx + 1
	docKeys, err := s.docRepo.FindDocKeysByRange(ctx, datasetID, startIdx, limit)
	if err != nil {
		return 0, err
	}
	if len(docKeys) == 0 {
		return 0, nil
	}

	return s.DeleteDocumentsBatch(ctx, datasetID, docKeys)
}

// getAnnotatorName looks up the username by ID. If not found, returns "Unknown".
func (s *DocumentService) getAnnotatorName(ctx context.Context, userID uint) string {
	user, err := s.dbRepo.FindUserByID(ctx, userID)
	if err != nil || user == nil {
		return "Unknown"
	}
	return user.Username
}
