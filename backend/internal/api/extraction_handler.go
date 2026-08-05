package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ExtractionHandler handles HTTP endpoints for document extraction.
type ExtractionHandler struct {
	extractionService *service.ExtractionService
	filterRegistry    *plugin.PluginRegistry[plugin.ExtractionFilter]
}

// NewExtractionHandler creates a new ExtractionHandler.
func NewExtractionHandler(extractionService *service.ExtractionService, filterRegistry *plugin.PluginRegistry[plugin.ExtractionFilter]) *ExtractionHandler {
	return &ExtractionHandler{extractionService: extractionService, filterRegistry: filterRegistry}
}

// Execute handles POST /datasets/:id/extraction.
func (h *ExtractionHandler) Execute(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req service.ExtractionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.DatasetID = uint(id)

	result, err := h.extractionService.Execute(c.Request.Context(), req)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, result)
}

// ListResults handles GET /datasets/:id/extractions.
func (h *ExtractionHandler) ListResults(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	results, err := h.extractionService.ListResults(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, results)
}

// GetResultDocuments handles GET /extractions/:id/documents.
func (h *ExtractionHandler) GetResultDocuments(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid extraction id")
		return
	}

	result, err := h.extractionService.GetResultByID(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, err.Error())
		return
	}

	var docKeys []string
	json.Unmarshal([]byte(result.DocKeys), &docKeys)

	c.JSON(http.StatusOK, gin.H{
		"doc_keys":      docKeys,
		"matched_count": result.MatchedCount,
		"total_count":   result.TotalCount,
	})
}

// ListFilters handles GET /extraction/filters.
func (h *ExtractionHandler) ListFilters(c *gin.Context) {
	filters := h.filterRegistry.List()

	type filterInfo struct {
		FilterID    string                 `json:"filter_id"`
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		ParamSchema map[string]interface{} `json:"param_schema"`
	}

	result := make([]filterInfo, 0, len(filters))
	for _, f := range filters {
		result = append(result, filterInfo{
			FilterID:    f.FilterID(),
			Name:        f.Name(),
			Description: f.Description(),
			ParamSchema: f.ParamSchema(),
		})
	}

	c.JSON(http.StatusOK, result)
}
