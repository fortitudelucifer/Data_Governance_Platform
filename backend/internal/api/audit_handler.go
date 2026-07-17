package api

import (
	"net/http"
	"time"

	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AuditHandler handles HTTP endpoints for querying audit logs.
type AuditHandler struct {
	auditService *service.AuditService
}

// NewAuditHandler creates an AuditHandler with the given AuditService.
func NewAuditHandler(auditService *service.AuditService) *AuditHandler {
	return &AuditHandler{auditService: auditService}
}

// QueryAuditLogs handles GET /audit_logs.
// Parses optional query params: start_time (RFC3339), end_time (RFC3339),
// action_type (string), page (int, default 1), size (int, default 20).
// Builds an AuditLogFilter and calls AuditService.Query.
// Returns JSON {logs: [...], total: int64, page: int, size: int}.
func (h *AuditHandler) QueryAuditLogs(c *gin.Context) {
	var filter repository.AuditLogFilter

	if startStr := c.Query("start_time"); startStr != "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid start_time, expected RFC3339 format")
			return
		}
		filter.StartTime = &t
	}

	if endStr := c.Query("end_time"); endStr != "" {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid end_time, expected RFC3339 format")
			return
		}
		filter.EndTime = &t
	}

	if actionType := c.Query("action_type"); actionType != "" {
		filter.Action = &actionType
	}

	page, size := ParsePageParams(c)
	filter.Page = page
	filter.PageSize = size

	result, err := h.auditService.Query(c.Request.Context(), filter)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":  result.Logs,
		"total": result.Total,
		"page":  page,
		"size":  size,
	})
}
