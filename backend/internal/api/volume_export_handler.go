package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	paymodel "text-annotation-platform/internal/model/payload"

	"text-annotation-platform/internal/service"
)

// VolumeExportHandler serves NIfTI label maps for a volume annotation task (C3.4).
//
// Two shapes, because one of them is necessarily lossy:
//
//	GET /tasks/:id/export.nifti           → seg.nii.gz, one multi-label volume
//	GET /tasks/:id/export.nifti?split=1   → zip of per-segment binary volumes
//
// Overlapping segments ("tumour inside liver" — the normal case in medicine)
// cannot both survive in a single label map. When that happens we still serve
// the file, but the loss is reported in the X-Overlap-Note header and in
// labels.json. Silently dropping annotator work would break rule 1 of
// 00《稠密几何存储契约》.
type VolumeExportHandler struct {
	assets *service.AssetService
	tracks *service.TrackService
	tasks  *service.AnnotationTaskService
}

func NewVolumeExportHandler(a *service.AssetService, t *service.TrackService, k *service.AnnotationTaskService) *VolumeExportHandler {
	return &VolumeExportHandler{assets: a, tracks: t, tasks: k}
}

// ExportNIfTI handles GET /tasks/:id/export.nifti.
func (h *VolumeExportHandler) ExportNIfTI(c *gin.Context) {
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

	meta, err := h.volumeMeta(c, task.AssetID)
	if err != nil {
		// A volume task without volume_meta means derivation never ran or
		// failed. Say which, rather than emitting an empty label map that
		// silently has the wrong geometry.
		Error(c, http.StatusConflict, "体数据几何信息(volume_meta)不可用，无法确定导出几何："+err.Error())
		return
	}

	// ⚠️ 数据源纪律（CLAUDE.md《数据模型要点》）：**导出的唯一真源是 track_snapshots**
	// （QA 通过时冻结），永远不读 `annotation_tracks`——后者是原地覆盖的，会漂。
	//
	// 但工作台在 QA 之前也需要能导出去核对（这正是 3D Slicer 跨软件验证的用法）。
	// 折中不是"没快照就悄悄读实时轨迹"——那等于把契约变成一句空话，而且导出的东西
	// 是不是终稿从文件上看不出来。所以：草稿导出必须**显式**要 `?source=draft`，
	// 且在响应头和文件名上标明 draft。默认路径没有快照就直接 409 说清楚。
	draft := c.Query("source") == "draft"
	var tracks []paymodel.Track
	if draft {
		tracks, err = h.tracks.List(c.Request.Context(), taskID, "", "")
	} else {
		tracks, err = h.tracks.ListSnapshotTracks(c.Request.Context(), task.DatasetID, taskID)
		if err == nil && len(tracks) == 0 {
			Error(c, http.StatusConflict,
				"该任务还没有 FINALIZED 快照（导出的唯一真源）。通过 QA 后再导出；"+
					"若只是想核对当前草稿，用 ?source=draft（结果会标记为 draft，不可作为交付物）。")
			return
		}
	}
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	segs, err := service.BuildVolumeSegments(tracks, meta.Dims)
	if err != nil {
		Error(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if len(segs) == 0 {
		Error(c, http.StatusNotFound, "该任务没有 voxel_mask 分割可导出")
		return
	}

	affine := service.AffineFromMeta(meta)
	_, overlaps := service.CombineLabelVolume(segs, meta.Dims)
	note := service.BuildOverlapNote(overlaps)

	man := service.VolumeExportManifest{
		AssetID: task.AssetID, TaskID: taskID,
		Dims: meta.Dims, Spacing: meta.Spacing,
		Segments: segs, Overlaps: overlaps, OverlapNote: note,
		Convention: "RAS+（NIfTI；源文件的 sform/qform 原样保留，未做仲裁）",
	}

	// 草稿导出必须**自我标识**：文件离开系统后没人记得它是从哪个入口出来的，
	// 而"拿草稿当交付物"正是这条纪律要防的事。文件名 + 响应头 + descrip 三处都标。
	suffix, descrip := "", "dg-seg"
	if draft {
		suffix, descrip = "_draft", "dg-seg DRAFT (pre-QA, not deliverable)"
		c.Header("X-Export-Source", "draft")
		man.Convention += " · DRAFT（未通过 QA，不可作为交付物）"
	} else {
		c.Header("X-Export-Source", "snapshot")
	}

	if note != "" {
		// Header must be latin-1 safe; the note is Chinese. Percent-encode it and
		// tell the client so, rather than emitting a mangled header.
		c.Header("X-Overlap-Note-Encoding", "percent-utf8")
		c.Header("X-Overlap-Note", url.PathEscape(note))
	}

	if c.Query("split") == "1" {
		blob, err := service.WriteVolumeExportZip(segs, man, meta.Dims, affine, orientOf(meta))
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=task_%d_segments%s.zip", taskID, suffix))
		c.Data(http.StatusOK, "application/zip", blob)
		return
	}

	labels, _ := service.CombineLabelVolume(segs, meta.Dims)
	labelJSON, _ := json.Marshal(labelIndex(segs))
	gz, err := service.WriteNIfTIGz(service.NIfTIVolume{
		Dims: meta.Dims, Affine: affine, Labels: labels,
		MaxLabel: service.MaxLabelValue(segs), Descrip: descrip,
		// 把源文件的朝向块原样带过去。少了它，标签图会在优先 qform 的工具
		// （3D Slicer/ITK）里与原图错开——两个文件各自都合法，只是叠不上。
		Orient: orientOf(meta),
		// #21 label→组织名 也嵌进 NIfTI 扩展,让含义随文件走(响应头在文件转发/离线后丢失)。
		LabelMapJSON: labelJSON,
	})
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// 单文件形式仍在响应头带一份 label→name(在线读取用);离线/转发时靠上面的 NIfTI 扩展。
	if labelJSON != nil {
		c.Header("X-Label-Map-Encoding", "percent-utf8")
		c.Header("X-Label-Map", url.PathEscape(string(labelJSON)))
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=task_%d_seg%s.nii.gz", taskID, suffix))
	c.Data(http.StatusOK, "application/gzip", gz)
}

// orientOf 取源影像的朝向块；旧派生物（C3.4 之前）没有这一块，返回 nil 让
// 写入器退回 sform-only。**不要**在这里现编一个——编出来的朝向和源文件不一致，
// 正是要防的那个错。
func orientOf(m *service.VolumeMeta) *service.NIfTIOrientation {
	if m == nil || !m.Orient.HasOrientation() {
		return nil
	}
	o := m.Orient
	return &o
}

func labelIndex(segs []service.VolumeSegment) map[string]string {
	out := make(map[string]string, len(segs))
	for _, s := range segs {
		out[strconv.Itoa(s.Value)] = s.Label
	}
	return out
}

// volumeMeta reads and parses the asset's volume_meta derivative.
func (h *VolumeExportHandler) volumeMeta(c *gin.Context, assetID uint) (*service.VolumeMeta, error) {
	ctx := c.Request.Context()
	d, err := h.assets.GetDerivative(ctx, assetID, "volume_meta")
	if err != nil {
		return nil, fmt.Errorf("派生物未就绪: %w", err)
	}
	rc, err := h.assets.OpenURI(ctx, d.StorageURI)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var m service.VolumeMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("volume_meta 解析失败: %w", err)
	}
	if m.Dims[0] <= 0 || m.Dims[1] <= 0 || m.Dims[2] <= 0 {
		return nil, fmt.Errorf("volume_meta 的 dims 非法: %v", m.Dims)
	}
	return &m, nil
}
