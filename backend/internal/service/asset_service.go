package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"text-annotation-platform/internal/cache"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"gorm.io/gorm"
)

const assetMetaTTL = 60 * time.Minute

// AssetService is the entry point for image asset uploads and metadata
// management. It composes QCService + ObjectStore + repositories and returns
// stable IDs to the caller.
//
// P0 only handles images. Audio / video are reserved at the database layer
// (plan_v1/01 §10) but not exercised here.
type AssetService struct {
	db    *repository.DB
	store ObjectStore
	qc    *QCService
	tasks *AnnotationTaskService
	cache *cache.Cache // nil = no Redis
}

// NewAssetService composes the dependencies. The QC service may be nil; in
// that case defaults are used. The task service may be nil if callers do not
// want each upload to spawn an annotation task automatically.
func NewAssetService(dbRepo *repository.DB, store ObjectStore, qc *QCService) *AssetService {
	if qc == nil {
		qc = NewQCService(QCConfig{})
	}
	return &AssetService{db: dbRepo, store: store, qc: qc}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *AssetService) WithCache(c *cache.Cache) *AssetService {
	s.cache = c
	return s
}

// DeleteAsset hard-deletes a sample and everything derived from it: annotation
// tasks, payload rows (annotations/tracks/results), derivative rows + their
// blobs, the source blob, and the asset row. Blob deletion is skipped when
// another asset shares the content hash (the store is content-addressed).
// Returns ErrAssetNotFound when the asset does not exist.
func (s *AssetService) DeleteAsset(ctx context.Context, id uint) error {
	asset, err := s.db.FindAssetByID(ctx, id)
	if err != nil {
		if repository.IsAssetNotFound(err) { // .First() → ErrRecordNotFound; make delete idempotent (404)
			return ErrAssetNotFound
		}
		return fmt.Errorf("find asset: %w", err)
	}
	if asset == nil {
		return ErrAssetNotFound
	}
	taskIDs, err := s.db.FindAnnotationTaskIDsByAsset(ctx, id)
	if err != nil {
		return fmt.Errorf("find tasks: %w", err)
	}
	// 载荷行(标注/track/快照/AI 结果)。曾经是可选注入的清理(nil 就跳过
	// ——一个静默漏清的口子);现在载荷与资产同库,无条件清,外键级联再兜一层。
	if err := s.db.DeleteMultiModalByAsset(ctx, id, taskIDs); err != nil {
		return fmt.Errorf("payload cleanup: %w", err)
	}
	// Only touch blobs if no other asset references the same content hash.
	shared := false
	if n, e := s.db.CountAssetsBySHA256Except(ctx, asset.SHA256, id); e == nil && n > 0 {
		shared = true
	}
	// Derivative blobs (share the sha-addressed path) + rows.
	if derivs, e := s.db.ListDerivatives(ctx, id); e == nil {
		if !shared {
			for _, d := range derivs {
				if d.StorageURI != "" {
					_ = s.store.Delete(ctx, d.StorageURI) // best-effort
				}
			}
		}
	}
	if err := s.db.DeleteDerivativesByAsset(ctx, id); err != nil {
		return fmt.Errorf("delete derivatives: %w", err)
	}
	// Annotation tasks.
	if err := s.db.DeleteAnnotationTasksByAsset(ctx, id); err != nil {
		return fmt.Errorf("delete tasks: %w", err)
	}
	// Source blob (guarded) + cache.
	if !shared && asset.StorageURI != "" {
		_ = s.store.Delete(ctx, asset.StorageURI) // best-effort (may be locked mid-preprocess)
	}
	if s.cache != nil {
		// 只剩 asset:{id}（GetAsset 的元数据缓存，有 TTL）。SHA256 去重缓存已整个
		// 拿掉——去重只问数据库，所以删资产时不再有「幽灵键」需要一并清理。
		// 老 Redis 库里残留的 asset:sha256:* 键已无人读取，是惰性垃圾。
		s.cache.Delete(ctx, "asset:"+strconv.FormatUint(uint64(id), 10))
	}
	// Asset row last.
	if err := s.db.DeleteAsset(ctx, id); err != nil {
		return fmt.Errorf("delete asset row: %w", err)
	}
	return nil
}

// BindTaskService wires in the annotation task service so successful uploads
// automatically spawn a CREATED / ROUTING task. Optional.
func (s *AssetService) BindTaskService(t *AnnotationTaskService) {
	s.tasks = t
}

// UploadOptions configures a single upload call.
//
// 曾经有个 AllowDuplicate 选项（绕过去重、同内容再插一行）。M6 之后它没法存在：
// (dataset_id, sha256) 的唯一性成了数据库约束，「同数据集同内容第二行」在 schema
// 层面就是非法的。要在一个数据集里重复用同一段素材 → 复制文件改一个字节，或者
// 建第二个数据集。
type UploadOptions struct {
	DatasetID    uint
	UploaderID   uint
	OriginalName string
	DeclaredMIME string
}

// UploadResult is returned by UploadImage.
type UploadResult struct {
	Asset        *dbmodel.Asset          `json:"asset"`
	Report       *QCReport               `json:"qc_report"`
	Deduplicated bool                    `json:"deduplicated"`
	Task         *dbmodel.AnnotationTask `json:"task,omitempty"`
}

// ErrDatasetNotImage indicates the target dataset's modality is not image.
var ErrDatasetNotImage = errors.New("dataset modality is not image")

// ErrAssetNotFound is returned by DeleteAsset when the asset does not exist.
var ErrAssetNotFound = errors.New("asset not found")

// UploadImage runs QC, uploads the asset to the ObjectStore, and persists the
// relational row. SHA256 dedup is honoured per (dataset_id, sha256). Validation
// failures are persisted as QC_FAILED rows so the operator can inspect them
// from the asset list.
func (s *AssetService) UploadImage(ctx context.Context, body io.Reader, opts UploadOptions) (*UploadResult, error) {
	if opts.DatasetID == 0 {
		return nil, errors.New("dataset_id required")
	}

	// Verify the dataset exists and is image-modality. Auto-promote text
	// datasets is intentionally NOT done here — operators must opt in.
	ds, err := s.db.FindDatasetByID(ctx, opts.DatasetID)
	if err != nil {
		return nil, fmt.Errorf("dataset lookup: %w", err)
	}
	// 资产上传适用于图片/音频/视频数据集；文本数据集走文档导入，不接受资产上传。
	if ds.Modality == dbmodel.ModalityText || ds.Modality == "" {
		return nil, ErrDatasetNotImage
	}

	report, clean, err := s.qc.Inspect(body, opts.DeclaredMIME)
	if err != nil {
		return nil, err
	}

	// SHA256 dedup. If an asset row already exists with the same SHA in the
	// same dataset and QC passed, return the existing row — the caller can
	// upsert tasks idempotently against it.
	//
	// 去重只问数据库，不问缓存。这里曾经先读一个持久（无 TTL）的 Redis 键
	// asset:sha256:{datasetID}:{sha}，命中就直接返回缓存里的资产行——**从不校验
	// 那一行还在不在库里**。于是任何「Redis 比库活得久」的场景都会让上传返回
	// 200 + deduplicated:true，却从不 INSERT，任务也永远建不出来，且不报错：
	//   · 两个环境共用一个 Redis db，dataset id 一撞就串（迁库时踩过）；
	//   · 换个空库做净环境自检、但没 flush Redis —— dataset id 从 1 重来，
	//     直接吃到上一个库的键。**项目自己推荐的测试流程就是触发器。**
	// 这次查询只是省一次对象存储写入的快路径；**正确性由数据库唯一约束保证**
	// （见下方 CreateAssetDedup，M6）——快路径漏掉的并发窗口由约束兜底。
	if report.Status == qcPassed && report.SHA256 != "" {
		existing, err := s.db.FindAssetBySHA256(ctx, opts.DatasetID, report.SHA256)
		if err == nil && existing != nil {
			return &UploadResult{Asset: existing, Report: report, Deduplicated: true}, nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("dedup lookup: %w", err)
		}
	}

	asset := &dbmodel.Asset{
		DatasetID:    opts.DatasetID,
		Modality:     ds.Modality,
		OriginalName: opts.OriginalName,
		MIME:         report.MIME,
		SHA256:       report.SHA256,
		SizeBytes:    report.SizeBytes,
		Width:        report.Width,
		Height:       report.Height,
		QCStatus:     report.Status,
		UploaderID:   opts.UploaderID,
	}

	if reportJSON, jerr := json.Marshal(report); jerr == nil {
		asset.QCReport = dbmodel.JSON(reportJSON)
	}
	if report.Features != nil {
		if featJSON, jerr := json.Marshal(report.Features); jerr == nil {
			asset.Features = dbmodel.JSON(featJSON)
		}
	}

	// Even if QC failed, we persist the row (StorageURI empty / minimal) so
	// the operator can see the rejection. Only successful uploads hit the
	// ObjectStore.
	if report.Status == qcPassed {
		put, err := s.store.Put(ctx, PutRequest{
			DatasetID:    opts.DatasetID,
			OriginalName: opts.OriginalName,
			MIME:         report.MIME,
			SHA256:       report.SHA256,
			Size:         int64(len(clean)),
			Body:         bytes.NewReader(clean),
		})
		if err != nil {
			return nil, fmt.Errorf("object store put: %w", err)
		}
		asset.StorageURI = put.StorageURI
		if put.SHA256 != "" {
			asset.SHA256 = put.SHA256
		}
	}

	// T0.3: queue audio/video for derived-asset preprocessing (waveform peaks /
	// frame index / thumbnail). The media-worker polls PreprocessStatus=pending.
	if report.Status == qcPassed && (ds.Modality == dbmodel.ModalityAudio || ds.Modality == dbmodel.ModalityVideo) {
		asset.PreprocessStatus = dbmodel.PreprocessPending
	}

	// 唯一性属于数据库（M6）：ON CONFLICT (dataset_id, sha256) WHERE
	// qc_status='passed' DO NOTHING。上面的快路径查过一次，但「查」与「写」之间
	// 的窗口里另一个并发请求可能已经插了同一份内容——此时这里拿到 0 行，
	// 改取那一行返回，**表里绝不会出现第二行**。blob 是内容寻址的，重复 Put
	// 落在同一个键上，无需回滚。
	inserted, err := s.db.CreateAssetDedup(ctx, asset)
	if err != nil {
		return nil, fmt.Errorf("create asset row: %w", err)
	}
	if !inserted {
		existing, ferr := s.db.FindAssetBySHA256(ctx, opts.DatasetID, asset.SHA256)
		if ferr != nil || existing == nil {
			return nil, fmt.Errorf("dedup conflict but existing row unreadable: %w", ferr)
		}
		return &UploadResult{Asset: existing, Report: report, Deduplicated: true}, nil
	}

	res := &UploadResult{Asset: asset, Report: report, Deduplicated: false}
	// QC 通过即建标注任务：图片走 L1 路由进 AI；音频/视频在任务服务里直接进
	// HUMAN_PENDING（不走图片 L1 router）。详见 plan_v2 执行方案-00 T0.1。
	if s.tasks != nil && asset.QCStatus == dbmodel.QCStatusPassed {
		if task, err := s.tasks.CreateForAsset(ctx, asset, CreateTaskOptions{}); err == nil {
			res.Task = task
		}
	}
	return res, nil
}

// GetAsset returns the asset row by id.
// Result is cached under "asset:{id}" for 60 minutes; assets are immutable
// once uploaded (content-addressed via SHA256).
func (s *AssetService) GetAsset(ctx context.Context, id uint) (*dbmodel.Asset, error) {
	key := "asset:" + strconv.FormatUint(uint64(id), 10)
	if s.cache != nil {
		var v dbmodel.Asset
		if hit, _ := s.cache.GetJSON(ctx, key, &v); hit {
			return &v, nil
		}
	}
	asset, err := s.db.FindAssetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, key, asset, assetMetaTTL)
	}
	return asset, nil
}

// ListAssets returns paginated assets.
func (s *AssetService) ListAssets(ctx context.Context, filter repository.AssetFilter, page, pageSize int) ([]dbmodel.Asset, int64, error) {
	return s.db.ListAssetsPage(ctx, filter, page, pageSize)
}

// OpenAssetBody returns a reader for the asset body. Callers must Close the
// reader.
func (s *AssetService) OpenAssetBody(ctx context.Context, asset *dbmodel.Asset) (io.ReadCloser, error) {
	if asset.StorageURI == "" {
		return nil, errors.New("asset has no storage uri (qc failed?)")
	}
	return s.store.Get(ctx, asset.StorageURI)
}

// PresignAssetBody returns a time-limited direct-download URL for the asset body
// when the object store supports it (MinIO). Empty string ⇒ stream via
// OpenAssetBody（本地驱动）。
func (s *AssetService) PresignAssetBody(ctx context.Context, asset *dbmodel.Asset, expiry time.Duration) (string, error) {
	if asset.StorageURI == "" {
		return "", errors.New("asset has no storage uri (qc failed?)")
	}
	return s.store.PresignGetURL(ctx, asset.StorageURI, expiry)
}

// GetDerivative returns the derived artifact row for (assetID, kind) (T0.3).
func (s *AssetService) GetDerivative(ctx context.Context, assetID uint, kind string) (*dbmodel.AssetDerivative, error) {
	return s.db.GetDerivative(ctx, assetID, kind)
}

// ListDerivatives returns all derived artifacts for an asset (T0.3).
func (s *AssetService) ListDerivatives(ctx context.Context, assetID uint) ([]dbmodel.AssetDerivative, error) {
	return s.db.ListDerivatives(ctx, assetID)
}

// PresignURI / OpenURI serve an arbitrary stored object (e.g. a derivative) by
// its storage URI, reusing the object-store presign / stream paths.
func (s *AssetService) PresignURI(ctx context.Context, storageURI string, expiry time.Duration) (string, error) {
	return s.store.PresignGetURL(ctx, storageURI, expiry)
}

func (s *AssetService) OpenURI(ctx context.Context, storageURI string) (io.ReadCloser, error) {
	return s.store.Get(ctx, storageURI)
}

// RegisterAssetOpts describes an already-stored object to register as an asset.
type RegisterAssetOpts struct {
	DatasetID    uint
	Modality     string
	StorageURI   string
	OriginalName string
	MIME         string
	SHA256       string
	SizeBytes    int64
	UploaderID   uint
}

// RegisterAsset persists an object that is already in the store (e.g. assembled
// by a multipart upload) as an asset: honours sha256 dedup, creates the
// annotation task and enqueues audio/video preprocessing. Mirrors the tail of
// UploadImage so the multipart path reuses the same lifecycle (T0.2).
func (s *AssetService) RegisterAsset(ctx context.Context, opts RegisterAssetOpts) (*UploadResult, error) {
	if opts.SHA256 != "" {
		existing, err := s.db.FindAssetBySHA256(ctx, opts.DatasetID, opts.SHA256)
		if err == nil && existing != nil {
			return &UploadResult{Asset: existing, Deduplicated: true}, nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("dedup lookup: %w", err)
		}
	}
	asset := &dbmodel.Asset{
		DatasetID:    opts.DatasetID,
		Modality:     opts.Modality,
		StorageURI:   opts.StorageURI,
		OriginalName: opts.OriginalName,
		MIME:         opts.MIME,
		SHA256:       opts.SHA256,
		SizeBytes:    opts.SizeBytes,
		QCStatus:     qcPassed,
		UploaderID:   opts.UploaderID,
	}
	if opts.Modality == dbmodel.ModalityAudio || opts.Modality == dbmodel.ModalityVideo {
		asset.PreprocessStatus = dbmodel.PreprocessPending
	}
	// 与 UploadImage 同款（M6）：并发注册同一内容时由唯一约束兜底，输家取现存行。
	inserted, err := s.db.CreateAssetDedup(ctx, asset)
	if err != nil {
		return nil, fmt.Errorf("create asset row: %w", err)
	}
	if !inserted {
		existing, ferr := s.db.FindAssetBySHA256(ctx, opts.DatasetID, opts.SHA256)
		if ferr != nil || existing == nil {
			return nil, fmt.Errorf("dedup conflict but existing row unreadable: %w", ferr)
		}
		return &UploadResult{Asset: existing, Deduplicated: true}, nil
	}
	res := &UploadResult{Asset: asset}
	if s.tasks != nil {
		if task, err := s.tasks.CreateForAsset(ctx, asset, CreateTaskOptions{}); err == nil {
			res.Task = task
		}
	}
	return res, nil
}
