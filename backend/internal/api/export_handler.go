package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ExportHandler handles HTTP endpoints for dataset export and export format listing.
type ExportHandler struct {
	exportService *service.ExportService
}

// NewExportHandler creates an ExportHandler with the given ExportService.
func NewExportHandler(exportService *service.ExportService) *ExportHandler {
	return &ExportHandler{exportService: exportService}
}

// ExportDataset handles GET /datasets/:id/export.
// Parses dataset ID from URL, reads required query param "format" and optional
// "since" (RFC3339 timestamp), retrieves user_id from UserContext, sets
// appropriate response headers for file download, and streams the export via
// ExportService.Export.
func (h *ExportHandler) ExportDataset(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	format := c.Query("format")
	if format == "" {
		Error(c, http.StatusBadRequest, "format query parameter is required")
		return
	}

	var since *time.Time
	if sinceStr := c.Query("since"); sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid since parameter, expected RFC3339 format")
			return
		}
		since = &t
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}
	docKeys := parseDocKeys(c.Query("doc_keys"))
	stage := strings.TrimSpace(c.Query("stage"))

	// Determine content type and file extension based on format
	contentType := "application/octet-stream"
	fileExt := format
	switch format {
	case "json":
		contentType = "application/json"
		fileExt = "json"
	case "jsonl":
		contentType = "application/jsonl"
		fileExt = "jsonl"
	case "csv":
		contentType = "text/csv"
		fileExt = "csv"
	}

	filename := fmt.Sprintf("dataset_%d_export.%s", id, fileExt)
	if len(docKeys) > 0 {
		filename = fmt.Sprintf("dataset_%d_selected%d_export.%s", id, len(docKeys), fileExt)
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Header("Content-Type", contentType)

	err = h.exportService.Export(c.Request.Context(), uint(id), format, c.Writer, since, userID, docKeys, stage)
	if err != nil {
		// If headers were already sent we can't change the status, but for
		// errors that occur before any data is written we return JSON.
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
}

func parseDocKeys(raw string) []string {
	if raw == "" {
		return nil
	}
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// ListExportFormats handles GET /export/formats.
// Returns all registered export formats as a JSON array.
func (h *ExportHandler) ListExportFormats(c *gin.Context) {
	formats := h.exportService.ListFormats()
	c.JSON(http.StatusOK, formats)
}
