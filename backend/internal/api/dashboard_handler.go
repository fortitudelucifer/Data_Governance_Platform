package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

func parseForceRefresh(c *gin.Context) bool {
	value := c.Query("force_refresh")
	if value == "" {
		return false
	}
	forceRefresh, err := strconv.ParseBool(value)
	return err == nil && forceRefresh
}

// DashboardHandler handles HTTP endpoints for the dashboard.
type DashboardHandler struct {
	dashboardService *service.DashboardService
}

// NewDashboardHandler creates a new DashboardHandler.
func NewDashboardHandler(dashboardService *service.DashboardService) *DashboardHandler {
	return &DashboardHandler{dashboardService: dashboardService}
}

// GetStats handles GET /dashboard/stats.
func (h *DashboardHandler) GetStats(c *gin.Context) {
	var datasetID *uint
	if idStr := c.Query("dataset_id"); idStr != "" {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid dataset_id")
			return
		}
		uid := uint(id)
		datasetID = &uid
	}

	stats, err := h.dashboardService.GetStats(c.Request.Context(), datasetID, parseForceRefresh(c))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, stats)
}

// GetTrend handles GET /dashboard/trend.
func (h *DashboardHandler) GetTrend(c *gin.Context) {
	days := 7
	if daysStr := c.Query("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && d > 0 {
			days = d
		}
	}

	var datasetID *uint
	if idStr := c.Query("dataset_id"); idStr != "" {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid dataset_id")
			return
		}
		uid := uint(id)
		datasetID = &uid
	}

	trends, err := h.dashboardService.GetDailyTrend(c.Request.Context(), days, datasetID, parseForceRefresh(c))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, trends)
}

// GetImageAnnotatorStats handles GET /dashboard/image-annotators.
// Returns per-assignee image task counts (HUMAN_PENDING / IN_PROGRESS /
// QA_PENDING / FINALIZED + completion rate). Admin sees all; others see self.
func (h *DashboardHandler) GetImageAnnotatorStats(c *gin.Context) {
	uc := middleware.GetUserContext(c)
	if uc == nil {
		Error(c, http.StatusForbidden, "无权访问")
		return
	}

	var datasetID *uint
	if idStr := c.Query("dataset_id"); idStr != "" {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid dataset_id")
			return
		}
		uid := uint(id)
		datasetID = &uid
	}

	stats, err := h.dashboardService.GetImageAnnotatorStats(c.Request.Context(), datasetID, uc.UserID, uc.Role, parseForceRefresh(c))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, stats)
}

// GetAnnotatorStats handles GET /dashboard/annotators.
func (h *DashboardHandler) GetAnnotatorStats(c *gin.Context) {
	uc := middleware.GetUserContext(c)
	if uc == nil {
		Error(c, http.StatusForbidden, "无权访问")
		return
	}

	var datasetID *uint
	if idStr := c.Query("dataset_id"); idStr != "" {
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid dataset_id")
			return
		}
		uid := uint(id)
		datasetID = &uid
	}

	stats, err := h.dashboardService.GetAnnotatorStats(c.Request.Context(), datasetID, uc.UserID, uc.Role, parseForceRefresh(c))
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, stats)
}

// RebuildCounters recomputes the counter columns on every dataset row.
// This is an admin-only repair operation.
func (h *DashboardHandler) RebuildCounters(c *gin.Context) {
	if err := h.dashboardService.RebuildAllCounters(c.Request.Context()); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
