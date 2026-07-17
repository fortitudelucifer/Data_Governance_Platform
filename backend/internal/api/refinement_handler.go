package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"text-annotation-platform/internal/api/middleware"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// RefinementHandler handles HTTP endpoints for the refinement workflow.
type RefinementHandler struct {
	refinementService *service.RefinementService
}

// NewRefinementHandler creates a new RefinementHandler.
func NewRefinementHandler(refinementService *service.RefinementService) *RefinementHandler {
	return &RefinementHandler{refinementService: refinementService}
}

// refinementResponse is the formatted response for refinement endpoints.
type refinementResponse struct {
	DocKey  string              `json:"doc_key"`
	Stage   string              `json:"stage"`
	Cursor  int                 `json:"cursor"`
	QAPairs []paymodel.QAPair `json:"qa_pairs"`
	ETag    string              `json:"etag"`
}

// formatRefinementResponse extracts qa_pairs from document data and formats the response.
func formatRefinementResponse(doc *paymodel.Document) *refinementResponse {
	var qaPairs []paymodel.QAPair
	if doc.Data != nil {
		if raw, ok := doc.Data["qa_pairs"]; ok {
			// Try direct type assertion first
			if pairs, ok := raw.([]paymodel.QAPair); ok {
				qaPairs = pairs
			} else if arr, ok := raw.([]interface{}); ok {
				jsonBytes, err := json.Marshal(arr)
				if err == nil {
					json.Unmarshal(jsonBytes, &qaPairs)
				}
			}
		}
	}
	if qaPairs == nil {
		qaPairs = []paymodel.QAPair{}
	}
	return &refinementResponse{
		DocKey:  doc.DocKey,
		Stage:   doc.AnnotationStage,
		Cursor:  doc.RefinementCursor,
		QAPairs: qaPairs,
		ETag:    doc.ETag,
	}
}

// StartRefinement handles POST /documents/:key/start_refinement.
func (h *RefinementHandler) StartRefinement(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, parseErr := parseOptionalDatasetID(c)
	if parseErr != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var doc *paymodel.Document
	var err error
	if datasetID != nil {
		doc, err = h.refinementService.StartRefinementInDataset(c.Request.Context(), *datasetID, docKey)
	} else {
		doc, err = h.refinementService.StartRefinement(c.Request.Context(), docKey)
	}
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// navigateRequest represents the JSON body for cursor navigation.
type navigateRequest struct {
	Action string `json:"action"` // "next", "prev", "jump"
	Index  *int   `json:"index"`  // required for "jump"
	ETag   string `json:"etag"`
}

// NavigateCursor handles PUT /documents/:key/refinement_cursor.
func (h *RefinementHandler) NavigateCursor(c *gin.Context) {
	docKey := c.Param("key")
	if docKey == "" {
		Error(c, http.StatusBadRequest, "document key is required")
		return
	}
	datasetID, parseErr := parseOptionalDatasetID(c)
	if parseErr != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var req navigateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	var doc *paymodel.Document
	var err error

	switch req.Action {
	case "next":
		if datasetID != nil {
			doc, err = h.refinementService.NavigateNextInDataset(c.Request.Context(), *datasetID, docKey, req.ETag)
		} else {
			doc, err = h.refinementService.NavigateNext(c.Request.Context(), docKey, req.ETag)
		}
	case "prev":
		if datasetID != nil {
			doc, err = h.refinementService.NavigatePrevInDataset(c.Request.Context(), *datasetID, docKey, req.ETag)
		} else {
			doc, err = h.refinementService.NavigatePrev(c.Request.Context(), docKey, req.ETag)
		}
	case "jump":
		if req.Index == nil {
			Error(c, http.StatusBadRequest, "index is required for jump action")
			return
		}
		if datasetID != nil {
			doc, err = h.refinementService.JumpToInDataset(c.Request.Context(), *datasetID, docKey, req.ETag, *req.Index)
		} else {
			doc, err = h.refinementService.JumpTo(c.Request.Context(), docKey, req.ETag, *req.Index)
		}
	default:
		Error(c, http.StatusBadRequest, "invalid action, must be next/prev/jump")
		return
	}

	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// editQAPairRequest represents the JSON body for editing a QA pair.
type editQAPairRequest struct {
	Question    string   `json:"question"`
	Answer      string   `json:"answer"`
	QuestionKey string   `json:"question_key,omitempty"`
	Category    string   `json:"category,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	SpanText    string   `json:"span_text,omitempty"`
	SpanStart   *int     `json:"span_start,omitempty"`
	SpanEnd     *int     `json:"span_end,omitempty"`
	TextField   string   `json:"text_field,omitempty"`
	ETag        string   `json:"etag"`
}

// EditQAPair handles PUT /documents/:key/qa_pairs/:index.
func (h *RefinementHandler) EditQAPair(c *gin.Context) {
	docKey := c.Param("key")
	indexStr := c.Param("index")
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid index")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	var req editQAPairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	pair := paymodel.QAPair{
		Question:    req.Question,
		Answer:      req.Answer,
		QuestionKey: req.QuestionKey,
		Category:    req.Category,
		Evidence:    req.Evidence,
		Confidence:  req.Confidence,
		Reason:      req.Reason,
		SpanText:    req.SpanText,
		SpanStart:   req.SpanStart,
		SpanEnd:     req.SpanEnd,
		TextField:   req.TextField,
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.refinementService.EditQAPairInDataset(c.Request.Context(), *datasetID, docKey, req.ETag, index, pair, userID)
	} else {
		doc, err = h.refinementService.EditQAPair(c.Request.Context(), docKey, req.ETag, index, pair, userID)
	}
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// deleteQAPairRequest represents the JSON body for deleting a QA pair.
type deleteQAPairRequest struct {
	ETag string `json:"etag"`
}

// DeleteQAPair handles DELETE /documents/:key/qa_pairs/:index.
func (h *RefinementHandler) DeleteQAPair(c *gin.Context) {
	docKey := c.Param("key")
	indexStr := c.Param("index")
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid index")
		return
	}
	datasetID, err := parseOptionalDatasetID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset_id")
		return
	}

	// Read etag from query param or body
	etag := c.Query("etag")
	if etag == "" {
		var req deleteQAPairRequest
		if err := c.ShouldBindJSON(&req); err == nil {
			etag = req.ETag
		}
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.refinementService.DeleteQAPairInDataset(c.Request.Context(), *datasetID, docKey, etag, index, userID)
	} else {
		doc, err = h.refinementService.DeleteQAPair(c.Request.Context(), docKey, etag, index, userID)
	}
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// addQAPairRequest represents the JSON body for adding a QA pair.
type addQAPairRequest struct {
	Question    string   `json:"question"`
	Answer      string   `json:"answer"`
	QuestionKey string   `json:"question_key,omitempty"`
	Category    string   `json:"category,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	SpanText    string   `json:"span_text,omitempty"`
	SpanStart   *int     `json:"span_start,omitempty"`
	SpanEnd     *int     `json:"span_end,omitempty"`
	TextField   string   `json:"text_field,omitempty"`
	ETag        string   `json:"etag"`
}

// AddQAPair handles POST /documents/:key/qa_pairs.
func (h *RefinementHandler) AddQAPair(c *gin.Context) {
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

	var req addQAPairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	pair := paymodel.QAPair{
		Question:    req.Question,
		Answer:      req.Answer,
		QuestionKey: req.QuestionKey,
		Category:    req.Category,
		Evidence:    req.Evidence,
		Confidence:  req.Confidence,
		Reason:      req.Reason,
		SpanText:    req.SpanText,
		SpanStart:   req.SpanStart,
		SpanEnd:     req.SpanEnd,
		TextField:   req.TextField,
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.refinementService.AddQAPairInDataset(c.Request.Context(), *datasetID, docKey, req.ETag, pair, userID)
	} else {
		doc, err = h.refinementService.AddQAPair(c.Request.Context(), docKey, req.ETag, pair, userID)
	}
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// CompleteRefinement handles POST /documents/:key/complete_refinement.
func (h *RefinementHandler) CompleteRefinement(c *gin.Context) {
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

	var req struct {
		ETag string `json:"etag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		// etag is optional for complete
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.refinementService.CompleteRefinementInDataset(c.Request.Context(), *datasetID, docKey, req.ETag)
	} else {
		doc, err = h.refinementService.CompleteRefinement(c.Request.Context(), docKey, req.ETag)
	}
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}

// bulkUpdateQAPairsRequest represents the JSON body for bulk updating QA pairs.
type bulkUpdateQAPairsRequest struct {
	QAPairs []paymodel.QAPair `json:"qa_pairs"`
	ETag    string              `json:"etag"`
}

// BulkUpdateQAPairs handles PUT /documents/:key/qa_pairs_bulk.
func (h *RefinementHandler) BulkUpdateQAPairs(c *gin.Context) {
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

	var req bulkUpdateQAPairsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var doc *paymodel.Document
	if datasetID != nil {
		doc, err = h.refinementService.BulkUpdateQAPairsInDataset(c.Request.Context(), *datasetID, docKey, req.ETag, req.QAPairs, userID)
	} else {
		doc, err = h.refinementService.BulkUpdateQAPairs(c.Request.Context(), docKey, req.ETag, req.QAPairs, userID)
	}
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	c.JSON(http.StatusOK, formatRefinementResponse(doc))
}
