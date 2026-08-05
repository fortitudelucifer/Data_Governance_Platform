package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// DatasetFunctionHandler handles HTTP endpoints for dataset function management.
type DatasetFunctionHandler struct {
	functionService *service.DatasetFunctionService
}

// NewDatasetFunctionHandler creates a new DatasetFunctionHandler.
func NewDatasetFunctionHandler(functionService *service.DatasetFunctionService) *DatasetFunctionHandler {
	return &DatasetFunctionHandler{functionService: functionService}
}

// List handles GET /dataset_functions.
func (h *DatasetFunctionHandler) List(c *gin.Context) {
	functions, err := h.functionService.List(c.Request.Context())
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, functions)
}

// Create handles POST /dataset_functions.
func (h *DatasetFunctionHandler) Create(c *gin.Context) {
	var req service.CreateFunctionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	fn, err := h.functionService.Create(c.Request.Context(), req)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, fn)
}

// Update handles PUT /dataset_functions/:id.
func (h *DatasetFunctionHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	var req service.UpdateFunctionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.functionService.Update(c.Request.Context(), uint(id), req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "name": req.Name})
}

// Delete handles DELETE /dataset_functions/:id.
func (h *DatasetFunctionHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.functionService.Delete(c.Request.Context(), uint(id)); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
