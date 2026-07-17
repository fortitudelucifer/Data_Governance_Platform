package api

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"text-annotation-platform/internal/api/middleware"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AssetHandler exposes HTTP endpoints for image asset upload, listing,
// detail and binary streaming.
type AssetHandler struct {
	svc  *service.AssetService
	task *service.AnnotationTaskService
}

// assetListItem is the per-row shape of GET /datasets/:id/assets. The Task
// field is nil when no annotation task exists yet for the asset.
type assetListItem struct {
	dbmodel.Asset
	Task *dbmodel.AnnotationTask `json:"task"`
}

// NewAssetHandler wires the asset service into Gin handlers.
// taskSvc may be nil; when present, each listing row includes the asset's
// latest annotation task so the frontend needs only one round-trip.
func NewAssetHandler(svc *service.AssetService, taskSvc *service.AnnotationTaskService) *AssetHandler {
	return &AssetHandler{svc: svc, task: taskSvc}
}

// Upload handles POST /datasets/:id/assets. The request must be
// multipart/form-data with the binary in the "file" field.
func (h *AssetHandler) Upload(c *gin.Context) {
	datasetIDStr := c.Param("id")
	datasetID, err := strconv.ParseUint(datasetIDStr, 10, 64)
	if err != nil || datasetID == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		Error(c, http.StatusBadRequest, "file field missing")
		return
	}
	defer file.Close()

	uc := middleware.GetUserContext(c)
	uploaderID := uint(0)
	if uc != nil {
		uploaderID = uc.UserID
	}

	declaredMIME := ""
	if header != nil {
		declaredMIME = header.Header.Get("Content-Type")
	}

	// allow_duplicate 参数已随 M6 移除：(dataset_id, sha256) 现在是数据库唯一约束，
	// 「同数据集同内容第二行」在 schema 层面就不存在，无从绕过。
	res, err := h.svc.UploadImage(c.Request.Context(), file, service.UploadOptions{
		DatasetID:    uint(datasetID),
		UploaderID:   uploaderID,
		OriginalName: filename(header),
		DeclaredMIME: declaredMIME,
	})
	if err != nil {
		if errors.Is(err, service.ErrDatasetNotImage) {
			Error(c, http.StatusBadRequest, "dataset modality is not image")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// List handles GET /datasets/:id/assets.
// When the task service is available each item includes a "task" field with
// the asset's latest annotation task, eliminating a second round-trip.
func (h *AssetHandler) List(c *gin.Context) {
	datasetIDStr := c.Param("id")
	datasetID, err := strconv.ParseUint(datasetIDStr, 10, 64)
	if err != nil || datasetID == 0 {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}
	page, pageSize := ParsePageParams(c)

	dsID := uint(datasetID)
	filter := repository.AssetFilter{DatasetID: &dsID}
	if v := c.Query("qc_status"); v != "" {
		filter.QCStatus = &v
	}
	if v := c.Query("modality"); v != "" {
		filter.Modality = &v
	}

	assets, total, err := h.svc.ListAssets(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// Build the response rows, optionally enriched with task state.
	rows := make([]assetListItem, len(assets))
	for i, a := range assets {
		rows[i] = assetListItem{Asset: a}
	}

	if h.task != nil && len(assets) > 0 {
		ids := make([]uint, len(assets))
		for i, a := range assets {
			ids[i] = a.ID
		}
		// Use a generous page size: some assets may have been reprocessed and
		// carry multiple task rows. len(ids)*10 caps at 1000 for a 100-item page.
		taskPageSize := len(ids) * 10
		if taskPageSize < 200 {
			taskPageSize = 200
		}
		tasks, _, _ := h.task.List(c.Request.Context(),
			repository.AnnotationTaskFilter{AssetIDs: ids},
			1, taskPageSize)
		// Keep the highest-version task per asset.
		latest := make(map[uint]*dbmodel.AnnotationTask, len(tasks))
		for i := range tasks {
			t := &tasks[i]
			if prev, ok := latest[t.AssetID]; !ok || t.Version >= prev.Version {
				latest[t.AssetID] = t
			}
		}
		for i := range rows {
			rows[i].Task = latest[rows[i].ID]
		}
	}

	RespondPage(c, rows, total, page, pageSize)
}

// Detail handles GET /assets/:id.
func (h *AssetHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	asset, err := h.svc.GetAsset(c.Request.Context(), uint(id))
	if err != nil {
		if repository.IsAssetNotFound(err) {
			Error(c, http.StatusNotFound, "asset not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, asset)
}

// Delete hard-deletes a sample (DELETE /assets/:id): source blob + derivatives +
// annotation tasks + all payload annotation/track rows + the asset row.
func (h *AssetHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.DeleteAsset(c.Request.Context(), uint(id)); err != nil {
		if errors.Is(err, service.ErrAssetNotFound) {
			Error(c, http.StatusNotFound, "asset not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Body streams the asset binary content (GET /assets/:id/body). The caller
// must already be authenticated.
func (h *AssetHandler) Body(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	asset, err := h.svc.GetAsset(c.Request.Context(), uint(id))
	if err != nil {
		if repository.IsAssetNotFound(err) {
			Error(c, http.StatusNotFound, "asset not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// PH-1：对象存储支持预签名（MinIO）时，302 重定向到直连 URL，让浏览器直接从
	// 对象存储取字节（原生 Range / 视频 seek），不再让大文件穿过应用进程。
	if url, perr := h.svc.PresignAssetBody(c.Request.Context(), asset, 15*time.Minute); perr == nil && url != "" {
		c.Redirect(http.StatusFound, url)
		return
	}

	rc, err := h.svc.OpenAssetBody(c.Request.Context(), asset)
	if err != nil {
		// A genuinely-absent blob (e.g. an old row whose file is no longer in
		// this store) is a 404, not a server error — avoids false "500" noise.
		if errors.Is(err, service.ErrObjectNotFound) {
			Error(c, http.StatusNotFound, "asset blob not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()

	// 缓存头：ETag 用内容哈希（内容寻址，永不变）→ 浏览器长缓存 + 条件请求 304。
	if asset.SHA256 != "" {
		c.Header("ETag", `"`+asset.SHA256+`"`)
		c.Header("Cache-Control", "private, max-age=86400")
	}
	if asset.MIME != "" {
		c.Header("Content-Type", asset.MIME)
	}

	// 本地驱动 Get 返回 *os.File（可 seek）→ http.ServeContent 免费获得
	// Range(206) / If-Range / If-Modified-Since / If-None-Match(304) / Content-Length。
	// 视频拖时间轴 seek 在"穿应用进程"路径下也能工作的关键。
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, asset.OriginalName, asset.CreatedAt, rs)
		return
	}

	// 兜底：不可 seek 的流——直出，无 Range。
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		_ = err // 连接级错误，状态已发出无法改写
	}
}

// derivativeMIME maps a derivative kind to its content type.
var derivativeMIME = map[string]string{
	dbmodel.DerivativeWaveform:   "application/json",
	dbmodel.DerivativeFrameIndex: "application/json",
	dbmodel.DerivativeThumbnail:  "image/jpeg",
	dbmodel.DerivativePlayback:   "video/mp4",
}

// Derivative handles GET /assets/:id/derivative/:kind — serves a derived
// artifact (waveform peaks / frame index / thumbnail) produced by the
// media-worker (T0.3). Presign-redirects when the store supports it, else
// streams with Range/cache headers.
func (h *AssetHandler) Derivative(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	kind := c.Param("kind")
	mime, ok := derivativeMIME[kind]
	if !ok {
		Error(c, http.StatusBadRequest, "unknown derivative kind")
		return
	}
	d, err := h.svc.GetDerivative(c.Request.Context(), uint(id), kind)
	if err != nil {
		Error(c, http.StatusNotFound, "derivative not found")
		return
	}
	if url, perr := h.svc.PresignURI(c.Request.Context(), d.StorageURI, 15*time.Minute); perr == nil && url != "" {
		c.Redirect(http.StatusFound, url)
		return
	}
	rc, err := h.svc.OpenURI(c.Request.Context(), d.StorageURI)
	if err != nil {
		if errors.Is(err, service.ErrObjectNotFound) {
			Error(c, http.StatusNotFound, "derivative blob not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()
	c.Header("Cache-Control", "private, max-age=86400")
	c.Header("Content-Type", mime)
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, kind, d.UpdatedAt, rs)
		return
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		_ = err
	}
}

// filename extracts the original filename from a multipart header, defending
// against nil headers.
func filename(h *multipart.FileHeader) string {
	if h == nil {
		return ""
	}
	return h.Filename
}
