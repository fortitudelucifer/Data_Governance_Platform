package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// LLMHandler handles HTTP endpoints for LLM-assisted operations.
type LLMHandler struct {
	llmService *service.LLMService
}

// NewLLMHandler creates an LLMHandler with the given LLMService.
func NewLLMHandler(llmService *service.LLMService) *LLMHandler {
	return &LLMHandler{llmService: llmService}
}

// generateQARequest represents the JSON body for POST /datasets/:id/qa/llm_suggest.
type generateQARequest struct {
	DocKey   string `json:"doc_key"`
	TextSpan string `json:"text_span"`
	MaxItems int    `json:"max_items"`
	QAStyle  string `json:"qa_style"`
}

// GenerateQACandidates handles POST /datasets/:id/qa/llm_suggest.
func (h *LLMHandler) GenerateQACandidates(c *gin.Context) {
	_, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req generateQARequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.TextSpan == "" {
		Error(c, http.StatusBadRequest, "text_span is required")
		return
	}

	llmReq := service.LLMRequest{
		DocKey:   req.DocKey,
		TextSpan: req.TextSpan,
		MaxItems: req.MaxItems,
		QAStyle:  req.QAStyle,
	}

	candidates, err := h.llmService.GenerateQACandidates(c.Request.Context(), llmReq)
	if err != nil {
		if _, ok := err.(*service.LLMDegradedError); ok {
			ErrorWithExtras(c, http.StatusServiceUnavailable, err.Error(), gin.H{"degraded": true})
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"candidates": candidates})
}

// ListTaskTypes handles GET /llm/task_types.
func (h *LLMHandler) ListTaskTypes(c *gin.Context) {
	types := h.llmService.ListTaskTypes()
	c.JSON(http.StatusOK, types)
}
