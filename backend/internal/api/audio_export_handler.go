package api

import (
	"archive/zip"
	"fmt"
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AudioExportHandler serves audio-annotation exports built from FINALIZED
// annotations: WebVTT / SRT (one file per audio → zip) and RTTM / CSV / JSONL
// (single combined file). Gated the same as image export (reviewer/admin).
type AudioExportHandler struct {
	audioExport *service.AudioExportService
}

// NewAudioExportHandler wires the dependencies.
func NewAudioExportHandler(audioExport *service.AudioExportService) *AudioExportHandler {
	return &AudioExportHandler{audioExport: audioExport}
}

// audioFormats is the set of accepted ?format= values.
var audioFormats = map[string]bool{
	"webvtt": true, "srt": true, "rttm": true, "csv": true, "jsonl": true,
}

// ExportAudio handles GET /datasets/:id/export.audio?format=webvtt|srt|rttm|csv|jsonl.
// Optional ?task_ids=1,2,3 restricts to a selection.
func (h *AudioExportHandler) ExportAudio(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	format := c.Query("format")
	if format == "" {
		format = "webvtt"
	}
	if !audioFormats[format] {
		Error(c, http.StatusBadRequest, "format must be one of webvtt|srt|rttm|csv|jsonl")
		return
	}
	taskIDs := parseTaskIDs(c)
	ctx := c.Request.Context()

	if h.audioExport.IsPerFile(format) {
		files, ferr := h.audioExport.BuildZip(ctx, uint(id), taskIDs, format)
		if ferr != nil {
			Error(c, http.StatusInternalServerError, ferr.Error())
			return
		}
		fname := exportFilename(id, "audio-"+format+".zip", taskIDs)
		c.Header("Content-Type", "application/zip")
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
		zw := zip.NewWriter(c.Writer)
		defer zw.Close()
		for name, content := range files {
			w, werr := zw.Create(name)
			if werr != nil {
				return
			}
			if _, werr = w.Write([]byte(content)); werr != nil {
				return
			}
		}
		return
	}

	suffix, contentType, content, serr := h.audioExport.BuildSingle(ctx, uint(id), taskIDs, format)
	if serr != nil {
		Error(c, http.StatusInternalServerError, serr.Error())
		return
	}
	fname := exportFilename(id, suffix, taskIDs)
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	c.String(http.StatusOK, content)
}
