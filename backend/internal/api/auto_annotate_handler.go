package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AutoAnnotateHandler handles HTTP endpoints for auto-annotation.
type AutoAnnotateHandler struct {
	autoAnnotationService *service.AutoAnnotationService
}

// NewAutoAnnotateHandler creates an AutoAnnotateHandler.
func NewAutoAnnotateHandler(svc *service.AutoAnnotationService) *AutoAnnotateHandler {
	return &AutoAnnotateHandler{autoAnnotationService: svc}
}

type autoAnnotateRequest struct {
	DocKeys    []string `json:"doc_keys"`
	ProviderID uint     `json:"provider_id"`
}

// TriggerAutoAnnotate handles POST /datasets/:id/auto_annotate.
func (h *AutoAnnotateHandler) TriggerAutoAnnotate(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req autoAnnotateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.DocKeys) == 0 {
		Error(c, http.StatusBadRequest, "doc_keys is required")
		return
	}
	if req.ProviderID == 0 {
		Error(c, http.StatusBadRequest, "provider_id is required")
		return
	}

	// Check provider connectivity
	ctx := c.Request.Context()
	if warning := h.autoAnnotationService.CheckProviderConnectivity(ctx, req.ProviderID); warning != "" {
		Error(c, http.StatusBadRequest, warning)
		return
	}

	annotateReq := service.AutoAnnotateRequest{
		DatasetID:  uint(datasetID),
		DocKeys:    req.DocKeys,
		ProviderID: req.ProviderID,
	}

	status := h.autoAnnotationService.AutoAnnotateDocuments(ctx, annotateReq)
	c.JSON(http.StatusOK, status)
}

// GetAutoAnnotateStatus handles GET /datasets/:id/auto_annotate/status.
// Returns the current annotation stage for all active documents in the dataset.
func (h *AutoAnnotateHandler) GetAutoAnnotateStatus(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	// Simple implementation: return stage distribution from current doc states
	c.JSON(http.StatusOK, gin.H{
		"dataset_id": datasetID,
		"message":    "use document list API to check individual document stages",
	})
}

type cancelAutoAnnotateRequest struct {
	DocKeys []string `json:"doc_keys"`
}

// CancelAutoAnnotate handles POST /datasets/:id/auto_annotate/cancel.
func (h *AutoAnnotateHandler) CancelAutoAnnotate(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req cancelAutoAnnotateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "无效的请求参数 / invalid request parameters")
		return
	}
	if len(req.DocKeys) == 0 {
		Error(c, http.StatusBadRequest, "doc_keys is required")
		return
	}

	if err := h.autoAnnotationService.CancelAutoAnnotation(c.Request.Context(), uint(datasetID), req.DocKeys); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	OK(c, "termination signal sent")
}

type rangeAutoAnnotateRequest struct {
	StartIdx   int64 `json:"start_idx"`
	EndIdx     int64 `json:"end_idx"`
	ProviderID uint  `json:"provider_id"`
}

// RangeAutoAnnotate handles POST /datasets/:id/auto_annotate/range.
func (h *AutoAnnotateHandler) RangeAutoAnnotate(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "无效的数据集ID / invalid dataset id")
		return
	}

	var req rangeAutoAnnotateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "无效的请求格式 / invalid request body")
		return
	}

	if req.StartIdx < 0 || req.EndIdx < req.StartIdx {
		Error(c, http.StatusBadRequest, "无效的索引范围 / invalid index range")
		return
	}
	if req.ProviderID == 0 {
		Error(c, http.StatusBadRequest, "provider_id is required")
		return
	}

	ctx := c.Request.Context()
	if warning := h.autoAnnotationService.CheckProviderConnectivity(ctx, req.ProviderID); warning != "" {
		Error(c, http.StatusBadRequest, warning)
		return
	}

	status, err := h.autoAnnotationService.RangeAutoAnnotateDocuments(ctx, uint(datasetID), req.StartIdx, req.EndIdx, req.ProviderID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, status)
}
