package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// VideoExportHandler serves video track exports built from FINALIZED snapshots.
// Zip formats: CVAT-XML / MOT (one file per video) and YOLO (one txt per
// annotated frame). Single-document formats: JSONL / COCO / Datumaro. Every
// format streams — a 1h video expands to millions of per-frame rows. Gated
// rolesReview (reviewer/admin), same as image/audio export.
type VideoExportHandler struct {
	videoExport *service.VideoExportService
}

// NewVideoExportHandler wires the dependencies.
func NewVideoExportHandler(videoExport *service.VideoExportService) *VideoExportHandler {
	return &VideoExportHandler{videoExport: videoExport}
}

var videoFormats = map[string]bool{
	"cvat": true, "mot": true, "jsonl": true,
	"coco": true, "yolo": true, "datumaro": true,
}

const videoFormatList = "cvat|mot|yolo|coco|datumaro|jsonl"

// ExportVideo handles GET /datasets/:id/export.video?format=<videoFormatList>&task_ids=...
func (h *VideoExportHandler) ExportVideo(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	format := c.Query("format")
	if format == "" {
		format = "cvat"
	}
	if !videoFormats[format] {
		Error(c, http.StatusBadRequest, "format must be one of "+videoFormatList)
		return
	}
	taskIDs := parseTaskIDs(c)
	ctx := c.Request.Context()

	if h.videoExport.IsPerFile(format) {
		// Stream each file straight into the zip: MOT on long video expands to
		// millions of rows and must never be buffered whole (B3.2).
		fname := exportFilename(id, "video-"+format+".zip", taskIDs)
		c.Header("Content-Type", "application/zip")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
		zw := zip.NewWriter(c.Writer)
		defer zw.Close()
		if serr := h.videoExport.StreamZip(ctx, uint(id), taskIDs, format, func(name string) (io.Writer, error) {
			return zw.Create(name)
		}); serr != nil {
			// Headers are already sent; the truncated zip signals the failure.
			return
		}
		return
	}

	// jsonl/coco/datumaro: one document, also streamed — COCO on a long video is
	// one annotation per frame per track. begin() runs after the last failure
	// point, so headers are only sent once the export is certain to succeed.
	if serr := h.videoExport.StreamSingle(ctx, uint(id), taskIDs, format, func(suffix, contentType string) (io.Writer, error) {
		fname := exportFilename(id, suffix, taskIDs)
		c.Header("Content-Type", contentType)
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
		c.Status(http.StatusOK)
		return c.Writer, nil
	}); serr != nil {
		if !c.Writer.Written() {
			Error(c, http.StatusInternalServerError, serr.Error())
		}
		return
	}
}
