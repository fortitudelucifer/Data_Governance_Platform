package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ImageExportHandler bundles multi-modal dataset export endpoints:
// streaming final annotations as JSONL, COCO JSON, COCO JSON-LD, and YOLO-seg ZIP.
//
// Lives in mm_export_handler.go (not export_handler.go) because the V1 text
// export handler already owns export_handler.go.
type ImageExportHandler struct {
	payload     *repository.DB
	imgExport *service.ImageExportService
}

// NewImageExportHandler wires the dependencies.
func NewImageExportHandler(payload *repository.DB, imgExport *service.ImageExportService) *ImageExportHandler {
	return &ImageExportHandler{payload: payload, imgExport: imgExport}
}

// ExportDatasetFinalAnnotations handles GET /datasets/:id/final-annotations.jsonl.
// Streams one FinalAnnotation per line as JSONL. Optional ?since=<RFC3339>
// filters to rows with created_at >= since, supporting incremental exports.
func (h *ImageExportHandler) ExportDatasetFinalAnnotations(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	var since *time.Time
	if raw := c.Query("since"); raw != "" {
		t, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			Error(c, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = &t
	}
	taskIDs := parseTaskIDs(c)
	fname := exportFilename(id, "final.jsonl", taskIDs)
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	enc := json.NewEncoder(c.Writer)
	enc.SetEscapeHTML(false)
	_, err = h.payload.StreamFinalAnnotationsByDataset(c.Request.Context(), uint(id), since, taskIDs, func(fa *paymodel.FinalAnnotation) error {
		return enc.Encode(fa)
	})
	if err != nil {
		_ = enc.Encode(map[string]interface{}{"_export_error": err.Error()})
	}
}

// ExportCOCO handles GET /datasets/:id/export.coco.json.
func (h *ImageExportHandler) ExportCOCO(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	since, ok := parseSince(c)
	if !ok {
		Error(c, http.StatusBadRequest, "since must be RFC3339")
		return
	}
	taskIDs := parseTaskIDs(c)
	doc, err := h.imgExport.BuildCOCO(c.Request.Context(), uint(id), since, taskIDs)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	fname := exportFilename(id, "coco.json", taskIDs)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	c.JSON(http.StatusOK, doc)
}

// ExportJSONLD handles GET /datasets/:id/export.jsonld.
func (h *ImageExportHandler) ExportJSONLD(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	since, ok := parseSince(c)
	if !ok {
		Error(c, http.StatusBadRequest, "since must be RFC3339")
		return
	}
	taskIDs := parseTaskIDs(c)
	doc, err := h.imgExport.BuildJSONLD(c.Request.Context(), uint(id), since, taskIDs)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	fname := exportFilename(id, "annotations.jsonld", taskIDs)
	c.Header("Content-Type", "application/ld+json; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	c.JSON(http.StatusOK, doc)
}

// ExportYOLOSeg handles GET /datasets/:id/export.yolo-seg.zip. Streams a zip
// containing labels/<stem>.txt (normalized polygons) + data.yaml.
func (h *ImageExportHandler) ExportYOLOSeg(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	since, ok := parseSince(c)
	if !ok {
		Error(c, http.StatusBadRequest, "since must be RFC3339")
		return
	}
	taskIDs := parseTaskIDs(c)
	exp, err := h.imgExport.BuildYOLOSeg(c.Request.Context(), uint(id), since, taskIDs)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	fname := exportFilename(id, "yolo-seg.zip", taskIDs)
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	zw := zip.NewWriter(c.Writer)
	defer zw.Close()
	writeZipEntry := func(name, content string) error {
		w, werr := zw.Create(name)
		if werr != nil {
			return werr
		}
		_, werr = w.Write([]byte(content))
		return werr
	}
	_ = writeZipEntry("data.yaml", exp.DataYAML)
	for name, content := range exp.Files {
		if err := writeZipEntry(name, content); err != nil {
			return
		}
	}
}

// parseSince reads the optional ?since=<RFC3339> query param.
func parseSince(c *gin.Context) (*time.Time, bool) {
	raw := c.Query("since")
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, false
	}
	return &t, true
}

// parseTaskIDs reads the optional ?task_ids=1,2,3 query param.
func parseTaskIDs(c *gin.Context) []uint {
	raw := c.Query("task_ids")
	if raw == "" {
		return nil
	}
	var ids []uint
	for _, part := range strings.Split(raw, ",") {
		if id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64); err == nil {
			ids = append(ids, uint(id))
		}
	}
	return ids
}

// exportFilename builds a Content-Disposition filename that embeds the
// selection size when task_ids are present.
func exportFilename(datasetID uint64, suffix string, taskIDs []uint) string {
	if len(taskIDs) > 0 {
		return fmt.Sprintf("dataset-%d-selected%d-%s", datasetID, len(taskIDs), suffix)
	}
	return fmt.Sprintf("dataset-%d-%s", datasetID, suffix)
}
