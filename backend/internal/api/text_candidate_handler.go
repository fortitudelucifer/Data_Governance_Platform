package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// TextCandidateHandler exposes multi-model text auto-annotation candidates.
type TextCandidateHandler struct {
	svc *service.TextCandidateService
}

// NewTextCandidateHandler creates a TextCandidateHandler.
func NewTextCandidateHandler(svc *service.TextCandidateService) *TextCandidateHandler {
	return &TextCandidateHandler{svc: svc}
}

type compareTextCandidatesRequest struct {
	DocKey            string `json:"doc_key"`
	ProviderIDs       []uint `json:"provider_ids"`
	PromptTemplateIDs []uint `json:"prompt_template_ids,omitempty"`
	TextField         string `json:"text_field,omitempty"`
}

type judgeTextCandidatesRequest struct {
	DocKey           string   `json:"doc_key"`
	CandidateRunIDs  []string `json:"candidate_run_ids"`
	ProviderID       uint     `json:"provider_id"`
	PromptTemplateID uint     `json:"prompt_template_id"`
	TextField        string   `json:"text_field,omitempty"`
}

// ListProviders handles GET /datasets/:id/auto_annotate/providers.
func (h *TextCandidateHandler) ListProviders(c *gin.Context) {
	if _, err := strconv.ParseUint(c.Param("id"), 10, 64); err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	providers, err := h.svc.ListProviderOptions(c.Request.Context())
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, providers)
}

// ListPromptTemplates handles GET /datasets/:id/auto_annotate/prompts.
func (h *TextCandidateHandler) ListPromptTemplates(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	templates, err := h.svc.ListPromptTemplatesForDataset(c.Request.Context(), uint(datasetID))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, templates)
}

// ListJudgePromptTemplates handles GET /datasets/:id/auto_annotate/judge_prompts.
func (h *TextCandidateHandler) ListJudgePromptTemplates(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	templates, err := h.svc.ListJudgePromptTemplatesForDataset(c.Request.Context(), uint(datasetID))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, templates)
}

// Compare handles POST /datasets/:id/auto_annotate/compare.
func (h *TextCandidateHandler) Compare(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var req compareTextCandidatesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DocKey == "" {
		Error(c, http.StatusBadRequest, "doc_key is required")
		return
	}
	if len(req.ProviderIDs) == 0 {
		Error(c, http.StatusBadRequest, "provider_ids is required")
		return
	}

	userID := currentUserID(c)
	result, err := h.svc.Compare(c.Request.Context(), service.TextCandidateCompareRequest{
		DatasetID:         uint(datasetID),
		DocKey:            req.DocKey,
		ProviderIDs:       req.ProviderIDs,
		PromptTemplateIDs: req.PromptTemplateIDs,
		TextField:         req.TextField,
		UserID:            userID,
	})
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, result)
}

// Judge handles POST /datasets/:id/auto_annotate/judge.
func (h *TextCandidateHandler) Judge(c *gin.Context) {
	datasetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var req judgeTextCandidatesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.DocKey == "" {
		Error(c, http.StatusBadRequest, "doc_key is required")
		return
	}
	if len(req.CandidateRunIDs) == 0 {
		Error(c, http.StatusBadRequest, "candidate_run_ids is required")
		return
	}
	if req.ProviderID == 0 {
		Error(c, http.StatusBadRequest, "provider_id is required")
		return
	}
	if req.PromptTemplateID == 0 {
		Error(c, http.StatusBadRequest, "prompt_template_id is required")
		return
	}
	run, err := h.svc.Judge(c.Request.Context(), service.TextCandidateJudgeRequest{
		DatasetID:        uint(datasetID),
		DocKey:           req.DocKey,
		CandidateRunIDs:  req.CandidateRunIDs,
		ProviderID:       req.ProviderID,
		PromptTemplateID: req.PromptTemplateID,
		TextField:        req.TextField,
		UserID:           currentUserID(c),
	})
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, run)
}

// List handles GET /documents/:key/auto_annotate/candidates.
func (h *TextCandidateHandler) List(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil || datasetID == nil {
		Error(c, http.StatusBadRequest, "dataset_id is required")
		return
	}
	candidates, err := h.svc.List(c.Request.Context(), *datasetID, docKey)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"dataset_id": *datasetID,
		"doc_key":    docKey,
		"candidates": candidates,
	})
}

// Delete handles DELETE /documents/:key/auto_annotate/candidates/:run_id.
func (h *TextCandidateHandler) Delete(c *gin.Context) {
	docKey := c.Param("key")
	runID := c.Param("run_id")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	if runID == "" {
		Error(c, http.StatusBadRequest, "run_id is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil || datasetID == nil {
		Error(c, http.StatusBadRequest, "dataset_id is required")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), service.TextCandidateDeleteRequest{
		DatasetID: *datasetID,
		DocKey:    docKey,
		RunID:     runID,
	}); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	OK(c, "candidate deleted")
}

// ListJudges handles GET /documents/:key/auto_annotate/judges.
func (h *TextCandidateHandler) ListJudges(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil || datasetID == nil {
		Error(c, http.StatusBadRequest, "dataset_id is required")
		return
	}
	runs, err := h.svc.ListJudgeRuns(c.Request.Context(), *datasetID, docKey)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"dataset_id": *datasetID,
		"doc_key":    docKey,
		"judges":     runs,
	})
}

type adoptTextCandidateRequest struct {
	RunID   string `json:"run_id"`
	Indexes []int  `json:"indexes,omitempty"`
	ETag    string `json:"etag"`
}

// Adopt handles POST /documents/:key/qa_pairs/adopt.
func (h *TextCandidateHandler) Adopt(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil || datasetID == nil {
		Error(c, http.StatusBadRequest, "dataset_id is required")
		return
	}
	var req adoptTextCandidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RunID == "" {
		Error(c, http.StatusBadRequest, "run_id is required")
		return
	}
	if req.ETag == "" {
		Error(c, http.StatusBadRequest, "etag is required")
		return
	}

	doc, err := h.svc.Adopt(c.Request.Context(), service.TextCandidateAdoptRequest{
		DatasetID: *datasetID,
		DocKey:    docKey,
		RunID:     req.RunID,
		Indexes:   req.Indexes,
		ETag:      req.ETag,
		UserID:    currentUserID(c),
	})
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// AdoptJudge handles POST /documents/:key/qa_pairs/adopt_judge.
func (h *TextCandidateHandler) AdoptJudge(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil || datasetID == nil {
		Error(c, http.StatusBadRequest, "dataset_id is required")
		return
	}
	var req adoptTextCandidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RunID == "" {
		Error(c, http.StatusBadRequest, "run_id is required")
		return
	}
	if req.ETag == "" {
		Error(c, http.StatusBadRequest, "etag is required")
		return
	}

	doc, err := h.svc.AdoptJudge(c.Request.Context(), service.TextCandidateJudgeAdoptRequest{
		DatasetID: *datasetID,
		DocKey:    docKey,
		RunID:     req.RunID,
		Indexes:   req.Indexes,
		ETag:      req.ETag,
		UserID:    currentUserID(c),
	})
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

func currentUserID(c *gin.Context) uint {
	if uc := middleware.GetUserContext(c); uc != nil && uc.UserID != 0 {
		return uc.UserID
	}
	return 1
}
