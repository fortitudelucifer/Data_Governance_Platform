package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// BatchAnnotateHandler exposes list-level batch auto-annotation for assets:
// select tasks → pick capability+model → run at a bounded concurrency, with
// progress polling + cancel. See plan_v2 item 4.
type BatchAnnotateHandler struct {
	svc *service.BatchAnnotateService
}

// NewBatchAnnotateHandler wires the service.
func NewBatchAnnotateHandler(svc *service.BatchAnnotateService) *BatchAnnotateHandler {
	return &BatchAnnotateHandler{svc: svc}
}

func datasetIDParam(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return 0, false
	}
	return uint(id), true
}

// Start handles POST /datasets/:id/assets/auto_annotate.
func (h *BatchAnnotateHandler) Start(c *gin.Context) {
	dsID, ok := datasetIDParam(c)
	if !ok {
		return
	}
	var req struct {
		TaskIDs     []uint `json:"task_ids" binding:"required"`
		Capability  string `json:"capability" binding:"required"`
		Model       string `json:"model"`
		Concurrency int    `json:"concurrency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	status, err := h.svc.Start(dsID, req.TaskIDs, req.Capability, req.Model, req.Concurrency)
	if err != nil {
		if err == service.ErrBatchRunning {
			Error(c, http.StatusConflict, err.Error())
			return
		}
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, status)
}

// Status handles GET /datasets/:id/assets/auto_annotate/status.
func (h *BatchAnnotateHandler) Status(c *gin.Context) {
	dsID, ok := datasetIDParam(c)
	if !ok {
		return
	}
	if job := h.svc.Status(dsID); job != nil {
		c.JSON(http.StatusOK, job)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "idle"})
}

// Cancel handles POST /datasets/:id/assets/auto_annotate/cancel.
func (h *BatchAnnotateHandler) Cancel(c *gin.Context) {
	dsID, ok := datasetIDParam(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"cancelled": h.svc.Cancel(dsID)})
}
