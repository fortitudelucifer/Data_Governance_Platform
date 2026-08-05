package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/service"
)

// C1.4 — 病理区域导出为 GeoJSON(QuPath 原生格式)。
//
//	GET /tasks/:id/export.geojson              → FeatureCollection(读 FINALIZED 快照)
//	GET /tasks/:id/export.geojson?source=draft → 读实时轨迹,显式标 draft(核对用)
//
// 数据源纪律与 C3.4 NIfTI 一致:**默认只读 track_snapshots**(导出唯一真源),
// 没有快照直接 409 说清楚,不"悄悄读实时轨迹"把契约变空话。草稿导出必须显式要
// ?source=draft,且文件名 + 响应头都标 draft。
type SlideExportHandler struct {
	tasks  *service.AnnotationTaskService
	tracks *service.TrackService
}

func NewSlideExportHandler(t *service.AnnotationTaskService, tr *service.TrackService) *SlideExportHandler {
	return &SlideExportHandler{tasks: t, tracks: tr}
}

// ExportGeoJSON handles GET /tasks/:id/export.geojson.
func (h *SlideExportHandler) ExportGeoJSON(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	task, err := h.tasks.Get(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusNotFound, "task not found")
		return
	}

	draft := c.Query("source") == "draft"
	var tracks []paymodel.Track
	if draft {
		tracks, err = h.tracks.List(c.Request.Context(), taskID, "", "")
	} else {
		tracks, err = h.tracks.ListSnapshotTracks(c.Request.Context(), task.DatasetID, taskID)
		if err == nil && len(tracks) == 0 {
			Error(c, http.StatusConflict,
				"该任务还没有 FINALIZED 快照（导出的唯一真源）。通过 QA 后再导出；"+
					"若只是想核对当前区域，用 ?source=draft（结果会标记为 draft，不可作为交付物）。")
			return
		}
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	blob, err := service.BuildSlideGeoJSON(tracks)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// region 数量放响应头,便于 UI/脚本核对(空 FeatureCollection 也是合法导出)。
	c.Header("X-Region-Count", strconv.Itoa(countFeatures(blob)))
	if draft {
		c.Header("X-Export-Source", "draft")
	} else {
		c.Header("X-Export-Source", "snapshot")
	}
	c.Header("Content-Disposition", "attachment; filename="+service.SlideGeoJSONFilename(taskID, draft))
	c.Data(http.StatusOK, "application/geo+json", blob)
}

// countFeatures 数导出的 region 个数(只解 features 数组长度,不碰几何)。
func countFeatures(blob []byte) int {
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(blob, &fc); err != nil {
		return 0
	}
	return len(fc.Features)
}
