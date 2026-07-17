package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// DatasetHandler handles HTTP endpoints for dataset CRUD operations.
type DatasetHandler struct {
	datasetService      *service.DatasetService
	compensationHandler *service.CompensationHandler
}

// NewDatasetHandler creates a DatasetHandler with the given dependencies.
func NewDatasetHandler(
	datasetService *service.DatasetService,
	compensationHandler *service.CompensationHandler,
) *DatasetHandler {
	return &DatasetHandler{
		datasetService:      datasetService,
		compensationHandler: compensationHandler,
	}
}

// createDatasetRequest represents the JSON body for POST /datasets.
type createDatasetRequest struct {
	Name              string `json:"name"`
	Modality          string `json:"modality"`
	CategoryID        uint   `json:"category_id"`
	TagIDs            []uint `json:"tag_ids"`
	IndustryTagIDs    []uint `json:"industry_tag_ids"`
	AnnotationType    string `json:"annotation_type"`
	CaseType          string `json:"case_type"`
	DatasetFunctionID *uint  `json:"dataset_function_id"`
}

// updateDatasetRequest represents the JSON body for PUT /datasets/:id.
type updateDatasetRequest struct {
	Name              string `json:"name"`
	CategoryID        uint   `json:"category_id"`
	TagIDs            []uint `json:"tag_ids"`
	IndustryTagIDs    []uint `json:"industry_tag_ids"`
	AnnotationType    string `json:"annotation_type"`
	CaseType          string `json:"case_type"`
	DatasetFunctionID *uint  `json:"dataset_function_id"`
}

// ListDatasets handles GET /datasets.
// Supports optional query params: category_id (uint) and tag_ids (comma-separated uints).
func (h *DatasetHandler) ListDatasets(c *gin.Context) {
	var categoryID *uint
	if cidStr := c.Query("category_id"); cidStr != "" {
		cid, err := strconv.ParseUint(cidStr, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid category_id")
			return
		}
		cidUint := uint(cid)
		categoryID = &cidUint
	}

	var tagIDs []uint
	if tagIDsStr := c.Query("tag_ids"); tagIDsStr != "" {
		parts := strings.Split(tagIDsStr, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			tid, err := strconv.ParseUint(p, 10, 64)
			if err != nil {
				Error(c, http.StatusBadRequest, "invalid tag_ids")
				return
			}
			tagIDs = append(tagIDs, uint(tid))
		}
	}

	page := 1
	if pageStr := c.DefaultQuery("page", "1"); pageStr != "" {
		parsed, err := strconv.Atoi(pageStr)
		if err != nil || parsed <= 0 {
			Error(c, http.StatusBadRequest, "invalid page")
			return
		}
		page = parsed
	}

	pageSize := 10
	if pageSizeStr := c.DefaultQuery("page_size", "10"); pageSizeStr != "" {
		parsed, err := strconv.Atoi(pageSizeStr)
		if err != nil || parsed <= 0 {
			Error(c, http.StatusBadRequest, "invalid page_size")
			return
		}
		pageSize = parsed
	}

	// q：按名字模糊搜（服务端）。此前搜索只在前端过滤当前页，第 5 页上的数据集
	// 永远搜不到。
	result, err := h.datasetService.ListDatasetsPage(c.Request.Context(), categoryID, tagIDs, c.Query("q"), repository.DatasetListSort{
		By:    c.Query("sort_by"),
		Order: c.Query("sort_order"),
	}, page, pageSize)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, result)
}

func (h *DatasetHandler) ListDatasetOptions(c *gin.Context) {
	options, err := h.datasetService.ListDatasetOptions(c.Request.Context())
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, options)
}

// GetDataset handles GET /datasets/:id.
func (h *DatasetHandler) GetDataset(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	ds, err := h.datasetService.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, "dataset not found")
		return
	}

	c.JSON(http.StatusOK, ds)
}

// CreateDataset handles POST /datasets.
// Binds JSON {name, category_id, tag_ids, annotation_type}, validates name
// is non-empty, gets user_id from UserContext, calls service, returns 201.
func (h *DatasetHandler) CreateDataset(c *gin.Context) {
	var req createDatasetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		Error(c, http.StatusBadRequest, "name is required")
		return
	}

	// Get user_id from middleware-injected UserContext
	var ownerID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		ownerID = uc.UserID
	}

	ds, err := h.datasetService.CreateDatasetWithModality(c.Request.Context(), req.Name, req.Modality, req.CategoryID, ownerID, req.TagIDs, req.IndustryTagIDs, req.AnnotationType, req.CaseType, req.DatasetFunctionID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":   ds.ID,
		"name": ds.Name,
	})
}

// UpdateDataset handles PUT /datasets/:id.
// Parses id from URL, binds JSON {name, category_id, tag_ids}, and updates the dataset.
func (h *DatasetHandler) UpdateDataset(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req updateDatasetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.datasetService.UpdateDataset(c.Request.Context(), uint(id), req.Name, req.CategoryID, req.TagIDs, req.IndustryTagIDs, req.AnnotationType, req.CaseType, req.DatasetFunctionID); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":   id,
		"name": req.Name,
	})
}

// updateExportMetaRequest represents the JSON body for PUT /datasets/:id/export-meta.
// It carries the export-envelope constants defined by 《通用元数据字段》规范.
type updateExportMetaRequest struct {
	AuthType     string          `json:"auth_type"`
	SourceType   string          `json:"source_type"`
	SourceDetail json.RawMessage `json:"source_detail"`
	DataVersion  string          `json:"data_version"`
}

// GetVideoAIConfig handles GET /datasets/:id/video-ai-config — the dataset's
// detect_track cost gate, with global defaults filled in (B2.8 成本闸门).
func (h *DatasetHandler) GetVideoAIConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	cfg, err := h.datasetService.GetVideoAIConfig(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// UpdateVideoAIConfig handles PUT /datasets/:id/video-ai-config (admin).
// Returns the *stored* config: out-of-range values are clamped rather than
// rejected, so the caller sees what actually took effect.
func (h *DatasetHandler) UpdateVideoAIConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var cfg service.VideoAIConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	stored, err := h.datasetService.UpdateVideoAIConfig(c.Request.Context(), uint(id), cfg)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, stored)
}

// UpdateExportMeta handles PUT /datasets/:id/export-meta.
// Persists the dataset-level export-envelope metadata (auth_type / source_type /
// source_detail / data_version) stamped onto every exported record.
func (h *DatasetHandler) UpdateExportMeta(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req updateExportMetaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	// source_detail is stored as a JSON object string; normalise empty/null to "{}".
	sourceDetail := strings.TrimSpace(string(req.SourceDetail))
	if sourceDetail == "" || sourceDetail == "null" {
		sourceDetail = "{}"
	} else if !json.Valid([]byte(sourceDetail)) {
		Error(c, http.StatusBadRequest, "source_detail must be valid JSON")
		return
	}

	if err := h.datasetService.UpdateExportMeta(c.Request.Context(), uint(id), req.AuthType, req.SourceType, sourceDetail, req.DataVersion); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": id})
}

// DeleteDataset handles DELETE /datasets/:id.
// Parses id from URL and calls CompensationHandler.DeleteDatasetWithCompensation
// which removes the dataset's document rows and the relational record.
func (h *DatasetHandler) DeleteDataset(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	if err := h.compensationHandler.DeleteDatasetWithCompensation(c.Request.Context(), uint(id)); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}

// GetLabelConfig handles GET /datasets/:id/label-config.
// Returns the raw JSON label config string (array of label defs).
func (h *DatasetHandler) GetLabelConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	ds, err := h.datasetService.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, "dataset not found")
		return
	}
	cfg := ds.LabelConfig
	if cfg == "" {
		cfg = "[]"
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(cfg))
}

// PutLabelConfig handles PUT /datasets/:id/label-config.
// Accepts a JSON array body and stores it as the dataset's label config.
func (h *DatasetHandler) PutLabelConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil || len(raw) == 0 {
		Error(c, http.StatusBadRequest, "empty body")
		return
	}
	if err := h.datasetService.UpdateLabelConfig(c.Request.Context(), uint(id), string(raw)); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GetLabelOntology handles GET /datasets/:id/ontology.
// Returns the raw JSON label-ontology object (audio/video). See plan_v2
// 执行方案-00 T0.6 《标签本体 Schema》.
func (h *DatasetHandler) GetLabelOntology(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	ds, err := h.datasetService.GetByID(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, "dataset not found")
		return
	}
	cfg := ds.LabelOntology
	if cfg == "" {
		cfg = "{}"
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(cfg))
}

// PutLabelOntology handles PUT /datasets/:id/ontology.
// Accepts a JSON object body, validates it parses, and stores it.
func (h *DatasetHandler) PutLabelOntology(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil || len(raw) == 0 {
		Error(c, http.StatusBadRequest, "empty body")
		return
	}
	var probe map[string]interface{}
	if jerr := json.Unmarshal(raw, &probe); jerr != nil {
		Error(c, http.StatusBadRequest, "ontology must be a JSON object")
		return
	}
	if err := h.datasetService.UpdateLabelOntology(c.Request.Context(), uint(id), string(raw)); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
