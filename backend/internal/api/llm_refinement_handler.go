package api

import (
	"net/http"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

type LLMRefinementHandler struct {
	llmService *service.LLMRefinementService
}

func NewLLMRefinementHandler(llmService *service.LLMRefinementService) *LLMRefinementHandler {
	return &LLMRefinementHandler{llmService: llmService}
}

type TriggerRefinementRequest struct {
	Enabled    bool   `json:"enabled"`
	Model      string `json:"model"`
	ProviderID uint   `json:"provider_id"`
}

func (h *LLMRefinementHandler) TriggerRefinement(c *gin.Context) {
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

	var req TriggerRefinementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if !req.Enabled {
		Error(c, http.StatusBadRequest, "must be enabled to trigger")
		return
	}

	userID := getUserId(c) // assume this is available from middleware

	var resp *service.LLMRefinementResponse
	if datasetID != nil {
		resp, err = h.llmService.TriggerRefinementInDataset(c.Request.Context(), *datasetID, docKey, req.Model, req.ProviderID, userID)
	} else {
		resp, err = h.llmService.TriggerRefinement(c.Request.Context(), docKey, req.Model, req.ProviderID, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *LLMRefinementHandler) RollbackRefinement(c *gin.Context) {
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

	userID := getUserId(c)

	if datasetID != nil {
		err = h.llmService.RollbackRefinementInDataset(c.Request.Context(), *datasetID, docKey, userID)
	} else {
		err = h.llmService.RollbackRefinement(c.Request.Context(), docKey, userID)
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	OK(c, "rollback successful")
}

// Utility to extract UserID from context
func getUserId(c *gin.Context) uint {
	val, exists := c.Get("userID")
	if !exists {
		return 0
	}
	if v, ok := val.(uint); ok {
		return v
	}
	return 0
}
