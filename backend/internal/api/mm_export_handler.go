package api

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// exportError maps export-service errors to HTTP status (#3): a selection that
// named tasks with no finalized snapshot is a client error (409), not a 500.
func exportError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrSelectedTasksMissing) {
		Error(c, http.StatusConflict, err.Error())
		return
	}
	Error(c, http.StatusInternalServerError, err.Error())
}

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
	taskIDs, err := parseTaskIDs(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	fname := exportFilename(id, "final.jsonl", taskIDs)
	// #21 生成到临时文件 + checksum,成功才回传。旧代码流式直写 c.Writer,中途出错就
	// 往 ndjson 里塞一条 {"_export_error":...} 记录再返回 200——业务 schema 被污染、下游
	// 无从分辨完整与否。改成:失败零字节送出 → 干净的 500。
	if err := serveGeneratedArtifact(c, fname, "application/x-ndjson; charset=utf-8", func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_, e := h.payload.StreamFinalAnnotationsByDataset(c.Request.Context(), uint(id), since, taskIDs, func(fa *paymodel.FinalAnnotation) error {
			return enc.Encode(fa)
		})
		return e
	}); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
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
	taskIDs, err := parseTaskIDs(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := h.imgExport.BuildCOCO(c.Request.Context(), uint(id), since, taskIDs)
	if err != nil {
		exportError(c, err)
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
	taskIDs, err := parseTaskIDs(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := h.imgExport.BuildJSONLD(c.Request.Context(), uint(id), since, taskIDs)
	if err != nil {
		exportError(c, err)
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
	taskIDs, err := parseTaskIDs(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	exp, err := h.imgExport.BuildYOLOSeg(c.Request.Context(), uint(id), since, taskIDs)
	if err != nil {
		exportError(c, err)
		return
	}
	fname := exportFilename(id, "yolo-seg.zip", taskIDs)
	// #21 zip 生成到临时文件 + checksum,成功才回传。旧代码把 zip 直写 c.Writer,某个
	// 条目写失败时(header 已发)只能 return,留下**截断的 zip 却是 200**,下游解压报错
	// 却不知是传输问题还是数据问题。现在失败零字节送出 → 干净 500;成功带 X-Content-SHA256。
	if err := serveGeneratedArtifact(c, fname, "application/zip", func(out io.Writer) error {
		zw := zip.NewWriter(out)
		writeZipEntry := func(name, content string) error {
			w, werr := zw.Create(name)
			if werr != nil {
				return werr
			}
			_, werr = w.Write([]byte(content))
			return werr
		}
		if e := writeZipEntry("data.yaml", exp.DataYAML); e != nil {
			return e
		}
		for name, content := range exp.Files {
			if e := writeZipEntry(name, content); e != nil {
				return e
			}
		}
		return zw.Close() // #21 Close 错误必检:zip 中央目录在 Close 时写,吞掉=损坏包
	}); err != nil {
		exportError(c, err)
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
//
// #2 fail-**closed**:参数**出现**但含空项 / 非法值 / 溢出 → 返回 error(上层 400)。
// 绝不把"给了个坏参数"静默降级成 nil —— 因为 nil 在下游=「无筛选=导出整集」,这是
// 最阴的 fail-open:请求方以为在导一个子集,实际拿到了全量(task_ids=abc → 空 → 全导;
// task_ids=101,abc → 悄悄只剩 101)。只有参数**根本没出现**才返回 (nil, nil)=真·无筛选。
func parseTaskIDs(c *gin.Context) ([]uint, error) {
	raw := c.Query("task_ids")
	if raw == "" {
		return nil, nil // 未提供该参数 = 无筛选(合法)
	}
	parts := strings.Split(raw, ",")
	ids := make([]uint, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			return nil, fmt.Errorf("task_ids 含空项(如尾随逗号)：%q", raw)
		}
		id, err := strconv.ParseUint(p, 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("task_ids 含非法/越界任务号：%q", part)
		}
		ids = append(ids, uint(id))
	}
	return ids, nil
}

// exportFilename builds a Content-Disposition filename that embeds the
// selection size when task_ids are present.
func exportFilename(datasetID uint64, suffix string, taskIDs []uint) string {
	if len(taskIDs) > 0 {
		return fmt.Sprintf("dataset-%d-selected%d-%s", datasetID, len(taskIDs), suffix)
	}
	return fmt.Sprintf("dataset-%d-%s", datasetID, suffix)
}
