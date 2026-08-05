package api

import (
	"net/http"
	"strconv"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// DatasetCategoryHandler handles HTTP endpoints for dataset category CRUD.
type DatasetCategoryHandler struct {
	datasetService *service.DatasetService
}

// NewDatasetCategoryHandler creates a DatasetCategoryHandler with the given DatasetService.
func NewDatasetCategoryHandler(datasetService *service.DatasetService) *DatasetCategoryHandler {
	return &DatasetCategoryHandler{datasetService: datasetService}
}

// createCategoryRequest represents the JSON body for creating/updating a category.
type createCategoryRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListCategories handles GET /dataset_categories.
// Returns all categories with their associated dataset counts.
func (h *DatasetCategoryHandler) ListCategories(c *gin.Context) {
	categories, err := h.datasetService.ListCategories(c.Request.Context())
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, categories)
}

// CreateCategory handles POST /dataset_categories.
// Binds JSON {name, description}, validates name is non-empty, creates and returns 201.
func (h *DatasetCategoryHandler) CreateCategory(c *gin.Context) {
	var req createCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		Error(c, http.StatusBadRequest, "name is required")
		return
	}

	cat := &dbmodel.DatasetCategory{
		Name:        req.Name,
		Description: req.Description,
	}

	if err := h.datasetService.CreateCategory(c.Request.Context(), cat); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":          cat.ID,
		"name":        cat.Name,
		"description": cat.Description,
	})
}

// UpdateCategory handles PUT /dataset_categories/:id.
// Parses id from URL, binds JSON {name, description}, and updates the category.
func (h *DatasetCategoryHandler) UpdateCategory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid category id")
		return
	}

	var req createCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Description != "" {
		updates["description"] = req.Description
	}

	if err := h.datasetService.UpdateCategory(c.Request.Context(), uint(id), updates); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          id,
		"name":        req.Name,
		"description": req.Description,
	})
}

// DeleteCategory handles DELETE /dataset_categories/:id.
// Parses id from URL and deletes the category.
func (h *DatasetCategoryHandler) DeleteCategory(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid category id")
		return
	}

	if err := h.datasetService.DeleteCategory(c.Request.Context(), uint(id)); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}
