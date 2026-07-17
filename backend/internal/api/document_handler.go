package api

import (
	"net/http"
	"strconv"
	"time"

	"text-annotation-platform/internal/api/middleware"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// DocumentHandler handles HTTP endpoints for document import, retrieval,
// versioning, and update operations.
type DocumentHandler struct {
	documentService     *service.DocumentService
	compensationHandler *service.CompensationHandler
	importRegistry      *plugin.PluginRegistry[plugin.ImportPlugin]
}

// NewDocumentHandler creates a DocumentHandler with the given dependencies.
func NewDocumentHandler(
	documentService *service.DocumentService,
	compensationHandler *service.CompensationHandler,
	importRegistry *plugin.PluginRegistry[plugin.ImportPlugin],
) *DocumentHandler {
	return &DocumentHandler{
		documentService:     documentService,
		compensationHandler: compensationHandler,
		importRegistry:      importRegistry,
	}
}

// updateDocumentRequest represents the JSON body for POST /documents/:key/update.
type updateDocumentRequest struct {
	Data map[string]interface{} `json:"data"`
}

func parseOptionalDatasetID(c *gin.Context) (*uint, error) {
	value := c.Query("dataset_id")
	if value == "" {
		return nil, nil
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, err
	}
	datasetID := uint(id)
	return &datasetID, nil
}

// ImportDocuments handles POST /datasets/:id/documents/import.
// Accepts a multipart file upload and an optional query param mode (default "full").
// Parses the dataset ID from the URL, retrieves user_id from UserContext,
// and delegates to DocumentService.ImportDocuments.
func (h *DocumentHandler) ImportDocuments(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		Error(c, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	mode := c.DefaultQuery("mode", "full")

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	report, err := h.documentService.ImportDocuments(
		c.Request.Context(),
		uint(id),
		file,
		header.Filename,
		mode,
		userID,
	)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, report)
}

// ListDocuments handles GET /datasets/:id/documents.
// Supports pagination via query params: page (default 1), page_size (default 20), q.
// Returns { items: [...], total: N, page: P, page_size: S }.
func (h *DocumentHandler) ListDocuments(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	page, pageSize := ParsePageParams(c)
	query := c.Query("q")

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	result, err := h.documentService.GetDocumentsByDatasetPaginated(c.Request.Context(), uint(id), page, pageSize, userID, query)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, result)
}

// GetDocument handles GET /documents/:key.
// Parses doc key from URL, retrieves user_id from UserContext, and returns
// the latest active version of the document.
func (h *DocumentHandler) GetDocument(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.documentService.GetActiveDocumentInDataset(c.Request.Context(), *datasetID, docKey, userID)
	} else {
		doc, err = h.documentService.GetActiveDocument(c.Request.Context(), docKey, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if doc == nil {
		Error(c, http.StatusNotFound, "document not found")
		return
	}

	c.JSON(http.StatusOK, doc)
}

type rangeDeleteRequest struct {
	StartIdx int64 `json:"start_idx"`
	EndIdx   int64 `json:"end_idx"`
}

// RangeDeleteDocuments handles POST /datasets/:id/documents/range_delete.
func (h *DocumentHandler) RangeDeleteDocuments(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "无效的数据集ID / invalid dataset id")
		return
	}

	var req rangeDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "无效的请求格式 / invalid request body")
		return
	}

	if req.StartIdx < 0 || req.EndIdx < req.StartIdx {
		Error(c, http.StatusBadRequest, "无效的索引范围 / invalid index range")
		return
	}

	ctx := c.Request.Context()
	deletedCount, err := h.documentService.DeleteDocumentsRange(ctx, uint(datasetID), req.StartIdx, req.EndIdx)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "删除成功 / deleted successfully",
		"deleted_count": deletedCount,
	})
}

// GetVersionHistory handles GET /documents/:key/versions.
// Parses doc key from URL, retrieves user_id from UserContext, and returns
// all versions of the document.
func (h *DocumentHandler) GetVersionHistory(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var versions []paymodel.Document
	if datasetID != nil {
		versions, err = h.documentService.GetVersionHistoryInDataset(c.Request.Context(), *datasetID, docKey, userID)
	} else {
		versions, err = h.documentService.GetVersionHistory(c.Request.Context(), docKey, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, versions)
}

// UpdateDocument handles POST /documents/:key/update.
// Parses doc key from URL, binds JSON {data: map}, retrieves user_id from
// UserContext, and creates a new document version via Copy-on-Write.
func (h *DocumentHandler) UpdateDocument(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var req updateDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Data == nil {
		Error(c, http.StatusBadRequest, "data is required")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.documentService.UpdateDocumentInDataset(c.Request.Context(), *datasetID, docKey, req.Data, userID)
	} else {
		doc, err = h.documentService.UpdateDocument(c.Request.Context(), docKey, req.Data, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"doc_key": doc.DocKey,
		"version": doc.Version,
	})
}

// DirectCompleteDocument handles POST /documents/:key/direct_complete.
func (h *DocumentHandler) DirectCompleteDocument(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.documentService.DirectCompleteRefinementInDataset(c.Request.Context(), *datasetID, docKey, userID)
	} else {
		doc, err = h.documentService.DirectCompleteRefinement(c.Request.Context(), docKey, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"doc_key": doc.DocKey,
		"version": doc.Version,
		"stage":   doc.AnnotationStage,
	})
}

// ReAnnotateDocument handles POST /documents/:key/reannotate.
func (h *DocumentHandler) ReAnnotateDocument(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.documentService.ReAnnotateDocumentInDataset(c.Request.Context(), *datasetID, docKey, userID)
	} else {
		doc, err = h.documentService.ReAnnotateDocument(c.Request.Context(), docKey, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"doc_key": doc.DocKey,
		"version": doc.Version,
		"stage":   doc.AnnotationStage,
	})
}

// DeleteDocument handles DELETE /datasets/:id/documents/:key.
// Deletes a single document (all versions) by doc_key.
func (h *DocumentHandler) DeleteDocument(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	if err := h.documentService.DeleteDocument(c.Request.Context(), uint(id), docKey); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	OK(c, "文档已删除")
}

// batchDeleteRequest represents the JSON body for batch document deletion.
type batchDeleteRequest struct {
	DocKeys []string `json:"doc_keys"`
}

// SetDocumentsDeadline handles PUT /datasets/:id/documents/deadline.
// Body: { "doc_keys": ["a","b"], "deadline": "2026-06-20T00:00:00Z" }（deadline 为空串=清除截止）。
func (h *DocumentHandler) SetDocumentsDeadline(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var req struct {
		DocKeys  []string `json:"doc_keys"`
		Deadline string   `json:"deadline"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.DocKeys) == 0 {
		Error(c, http.StatusBadRequest, "doc_keys is required")
		return
	}
	if err := h.documentService.SetDocumentsDeadline(c.Request.Context(), uint(id), req.DocKeys, req.Deadline); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(req.DocKeys)})
}

// AssignDocuments handles PUT /datasets/:id/documents/assign.
// Body: { "doc_keys": ["a","b"], "assignee_id": 3, "reviewer_id": 5, "deadline_at": "2026-06-01T00:00:00Z" }
// assignee_id/reviewer_id 为 0 表示清空；deadline_at 空串表示清除截止时间。
func (h *DocumentHandler) AssignDocuments(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var req struct {
		DocKeys    []string `json:"doc_keys"`
		AssigneeID *uint    `json:"assignee_id"`
		ReviewerID *uint    `json:"reviewer_id"`
		DeadlineAt *string  `json:"deadline_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.DocKeys) == 0 {
		Error(c, http.StatusBadRequest, "doc_keys is required")
		return
	}
	if req.AssigneeID == nil && req.ReviewerID == nil && req.DeadlineAt == nil {
		Error(c, http.StatusBadRequest, "no assignment fields provided")
		return
	}
	if req.DeadlineAt != nil && *req.DeadlineAt != "" {
		if _, err := time.Parse(time.RFC3339, *req.DeadlineAt); err != nil {
			Error(c, http.StatusBadRequest, "invalid deadline_at: "+err.Error())
			return
		}
	}
	if err := h.documentService.AssignDocuments(c.Request.Context(), uint(id), req.DocKeys, req.AssigneeID, req.ReviewerID, req.DeadlineAt); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(req.DocKeys)})
}

// BatchDeleteDocuments handles POST /datasets/:id/documents/batch_delete.
// Deletes multiple documents by doc_keys.
func (h *DocumentHandler) BatchDeleteDocuments(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var req batchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.DocKeys) == 0 {
		Error(c, http.StatusBadRequest, "doc_keys is required")
		return
	}
	deleted, err := h.documentService.DeleteDocumentsBatch(c.Request.Context(), uint(id), req.DocKeys)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted_count": deleted})
}

// ListImportFormats handles GET /import/formats.
// Iterates the import plugin registry and returns format info for each
// registered plugin.
func (h *DocumentHandler) ListImportFormats(c *gin.Context) {
	plugins := h.importRegistry.List()

	type formatInfo struct {
		FormatID   string   `json:"format_id"`
		Extensions []string `json:"extensions"`
	}

	formats := make([]formatInfo, 0, len(plugins))
	for _, p := range plugins {
		formats = append(formats, formatInfo{
			FormatID:   p.FormatID(),
			Extensions: p.SupportedExtensions(),
		})
	}

	c.JSON(http.StatusOK, formats)
}
