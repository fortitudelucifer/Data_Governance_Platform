package api

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
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
		// 客户端可修正的拒绝一律 400 + 指路文案(护栏契约,与 multipart Init 对齐):
		// 数据集模态不对、以及 fail-closed 医学闸门的「必须声明 data_source」/「临床去
		// 标识管线未配置」——这些都是调用方能修的,错误文本本身就是指路。只有真正的
		// 服务端接线故障(闸门没接上,ErrDeidGateNotWired)或未预期错误才 500。
		switch {
		case errors.Is(err, service.ErrDatasetNotImage),
			errors.Is(err, service.ErrMedicalSourceUndeclared),
			errors.Is(err, service.ErrClinicalDeidNotConfigured):
			Error(c, http.StatusBadRequest, err.Error())
		default:
			Error(c, http.StatusInternalServerError, err.Error())
		}
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

// derivativeMIME maps a derivative kind to its content type. Single-file
// derivatives only — prefix kinds (volume_slices, dzi_tiles) are served per-file
// by their own endpoints (Slice / tile), not here.
var derivativeMIME = map[string]string{
	dbmodel.DerivativeWaveform:   "application/json",
	dbmodel.DerivativeFrameIndex: "application/json",
	dbmodel.DerivativeThumbnail:  "image/jpeg",
	dbmodel.DerivativePlayback:   "video/mp4",
	dbmodel.DerivativeVolumeMeta: "application/json",
	dbmodel.DerivativeSlideMeta:  "application/json",
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

// Slice serves one z-slice PNG of a volume asset (GET /assets/:id/slice/:z,
// 执行方案-04 · C3.1). The volume_slices derivative's storage_uri is a directory
// PREFIX (C0.2); this appends the {z:04d}.png filename and presign-redirects or
// streams it. Out-of-range z → 404 (the object simply doesn't exist).
func (h *AssetHandler) Slice(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	z, err := strconv.Atoi(c.Param("z"))
	if err != nil || z < 0 {
		Error(c, http.StatusBadRequest, "invalid slice index")
		return
	}
	d, err := h.svc.GetDerivative(c.Request.Context(), uint(id), dbmodel.DerivativeVolumeSlices)
	if err != nil {
		Error(c, http.StatusNotFound, "volume slices not ready")
		return
	}
	// storage_uri is the prefix (trailing slash); slices are {z:04d}.png under it.
	sliceURI := strings.TrimSuffix(d.StorageURI, "/") + fmt.Sprintf("/%04d.png", z)
	if url, perr := h.svc.PresignURI(c.Request.Context(), sliceURI, 15*time.Minute); perr == nil && url != "" {
		c.Redirect(http.StatusFound, url)
		return
	}
	rc, err := h.svc.OpenURI(c.Request.Context(), sliceURI)
	if err != nil {
		if errors.Is(err, service.ErrObjectNotFound) {
			Error(c, http.StatusNotFound, "slice not found")
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()
	c.Header("Cache-Control", "private, max-age=86400")
	c.Header("Content-Type", "image/png")
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(c.Writer, c.Request, fmt.Sprintf("%04d.png", z), d.UpdatedAt, rs)
		return
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, rc); err != nil {
		_ = err
	}
}

// Tile handles GET /assets/:id/tile/:level/:col/:row — one WSI pyramid tile,
// extracted on demand from the original slide (C1.2b).
//
// Unlike Slice (which serves a pre-derived static PNG), a WSI has **no**
// pre-generated tiles: C1.1c decided against DZI. We seek into the original SVS,
// pull the one tile, splice its JPEGTables header, and stream it as a
// self-contained JPEG. Reads a few KB regardless of slide size (ExtractTile
// never materialises the offset array), so a gigapixel slide costs the same per
// tile as a small one.
func (h *AssetHandler) Tile(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return
	}
	level, lErr := strconv.Atoi(c.Param("level"))
	col, cErr := strconv.Atoi(c.Param("col"))
	row, rErr := strconv.Atoi(c.Param("row"))
	if lErr != nil || cErr != nil || rErr != nil || level < 0 || col < 0 || row < 0 {
		Error(c, http.StatusBadRequest, "invalid tile coordinates")
		return
	}

	asset, err := h.svc.GetAsset(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, "asset not found")
		return
	}
	// Only slides carry a tile pyramid. Running ExtractTile on a NIfTI/video
	// would misparse its bytes as TIFF — reject rather than return garbage.
	if asset.Modality != dbmodel.ModalityWSI {
		Error(c, http.StatusBadRequest, "asset is not a whole-slide image")
		return
	}

	rc, err := h.svc.OpenURI(c.Request.Context(), asset.StorageURI)
	if err != nil {
		Error(c, http.StatusNotFound, "slide bytes not found")
		return
	}
	defer rc.Close()
	rs, ok := rc.(io.ReadSeeker)
	if !ok {
		// Every current object-store driver returns a seekable reader (local
		// *os.File, MinIO *minio.Object over HTTP Range). If one ever doesn't,
		// buffering a gigapixel slide would OOM — fail loudly instead.
		Error(c, http.StatusInternalServerError, "slide storage is not seekable")
		return
	}

	tile, err := service.ExtractTile(rs, level, col, row)
	if err != nil {
		// Out-of-range tile is the viewer's bug (asking past the grid), not a
		// server fault — 404 so the client stops requesting it.
		Error(c, http.StatusNotFound, err.Error())
		return
	}
	// Tiles are immutable content of an immutable asset → cache hard.
	c.Header("Cache-Control", "private, max-age=604800, immutable")
	c.Data(http.StatusOK, "image/jpeg", tile)
}

// filename extracts the original filename from a multipart header, defending
// against nil headers.
func filename(h *multipart.FileHeader) string {
	if h == nil {
		return ""
	}
	return h.Filename
}
