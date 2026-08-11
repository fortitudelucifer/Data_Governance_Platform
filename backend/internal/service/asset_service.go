package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
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
	// deidGate guards wsi/volume ingest (C-H1). nil is NOT a bypass: medical
	// uploads through an un-wired service are rejected (fail-closed).
	deidGate *DeidGate
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

// WithDeidGate injects the medical-ingest gate; call from main.go after
// construction. Without it, medical uploads are rejected outright.
func (s *AssetService) WithDeidGate(g *DeidGate) *AssetService {
	s.deidGate = g
	return s
}

// AdmitMedicalIngest runs the fail-closed medical gate for ds. Exposed so the
// multipart path can reject clinical uploads at Init — before any bytes are
// PUT to the temp prefix — rather than only at Complete (PHI-before-storage).
func (s *AssetService) AdmitMedicalIngest(ctx context.Context, ds *dbmodel.Dataset, actorID uint) error {
	return admitMedicalIngest(ctx, s.deidGate, ds, actorID)
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

	// #15/#17 关系删除 + "该删哪些 blob" 登记进 GC outbox **放进同一个数据库事务**。
	// 提交 = 关系行没了 + 待删对象已持久登记,两者原子。旧代码是 "逐步删关系 + 内存里
	// best-effort store.Delete":删一半崩溃 / 对象存储一抖 → blob 成 PHI 孤儿,无从回收。
	// 现在删对象失败只是"outbox 里等 janitor 重试",不再泄漏。
	var gcItems []dbmodel.ObjectGC
	err = s.db.DB.Transaction(func(tx *gorm.DB) error {
		// #20 与上传 dedup 决策串行化:拿 (dataset,sha) advisory 锁,并发上传若正持锁读
		// existing,本删除会阻塞到它提交——上传于是不会返回一个"正被删"的幽灵 id。
		if e := s.db.AcquireDedupUploadLockTx(ctx, tx, asset.DatasetID, asset.SHA256); e != nil {
			return fmt.Errorf("dedup lock: %w", e)
		}
		txRepo := s.db.WithTx(tx)
		// #16 源 blob 按**精确 storage_uri**(不是全局 SHA)计数;在事务内算,与删除一致。
		// 查询出错必须中止(宁可不删也不猜)。派生物是本资产专属,无条件删。
		sharedSource := false
		if asset.StorageURI != "" {
			n, e := txRepo.CountAssetsByStorageURIExcept(ctx, asset.StorageURI, id)
			if e != nil {
				return fmt.Errorf("source blob refcount: %w", e)
			}
			sharedSource = n > 0
		}
		derivs, e := txRepo.ListDerivatives(ctx, id)
		if e != nil {
			return fmt.Errorf("list derivatives: %w", e)
		}
		if e := txRepo.DeleteMultiModalByAsset(ctx, id, taskIDs); e != nil {
			return fmt.Errorf("payload cleanup: %w", e)
		}
		if e := txRepo.DeleteDerivativesByAsset(ctx, id); e != nil {
			return fmt.Errorf("delete derivatives: %w", e)
		}
		if e := txRepo.DeleteAnnotationTasksByAsset(ctx, id); e != nil {
			return fmt.Errorf("delete tasks: %w", e)
		}
		if e := txRepo.DeleteAsset(ctx, id); e != nil {
			return fmt.Errorf("delete asset row: %w", e)
		}
		// 派生物(含 #18 前缀型 volume_slices/)+ 未共享的源 blob 入队。
		for _, d := range derivs {
			if d.StorageURI == "" {
				continue
			}
			gcItems = append(gcItems, dbmodel.ObjectGC{
				StorageURI: d.StorageURI, IsPrefix: strings.HasSuffix(d.StorageURI, "/"),
				Reason: "delete asset derivative",
			})
		}
		if !sharedSource && asset.StorageURI != "" {
			gcItems = append(gcItems, dbmodel.ObjectGC{StorageURI: asset.StorageURI, Reason: "delete asset source"})
		}
		return s.db.EnqueueObjectGCTx(ctx, tx, gcItems)
	})
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}

	// 提交后走快路径:立刻尝试删对象;成功就顺手把 outbox 行消掉,失败留给 janitor 重试。
	// (Delete/DeletePrefix 幂等,janitor 再来发现已删也会 resolve。)
	for _, it := range gcItems {
		var de error
		if it.IsPrefix {
			de = s.store.DeletePrefix(ctx, it.StorageURI)
		} else {
			de = s.store.Delete(ctx, it.StorageURI)
		}
		if de == nil && it.ID != 0 {
			_ = s.db.ResolveObjectGC(ctx, it.ID)
		}
	}
	// #13 缓存失效放在**删行之后**,避免并发 GET 把即将删除的资产重新填回缓存。
	if s.cache != nil {
		s.cache.Delete(ctx, "asset:"+strconv.FormatUint(uint64(id), 10))
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

	// 医学模态(wsi/volume)先过 DeidGate(C-H1):必须发生在 QC/对象存储之前
	// ——文件一旦落进 storage/assets 就已泄露,事后清洗没有意义。
	if err := admitMedicalIngest(ctx, s.deidGate, ds, opts.UploaderID); err != nil {
		return nil, err
	}

	report, clean, err := s.qc.Inspect(body, opts.DeclaredMIME)
	if err != nil {
		return nil, err
	}
	// #5 内容种类必须与数据集模态匹配。旧代码无条件按 ds.Modality 入库:PNG 传进 video
	// 数据集会被当 video 处理(错误的预处理/零尺寸/错误任务路由)。magic 识别出的种类
	// (image/volume/wsi 可靠)与模态不符即拒;音视频 magic 覆盖不全的由 worker 的 ffprobe
	// 再验(canonical kind)。放行的失败态资产仍要挡住这种路由错误,所以在入库前就查。
	if cerr := checkModalityMIME(ds.Modality, report.MIME); cerr != nil {
		return nil, cerr
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
		// #20 dedup 读放进持 (dataset,sha) advisory 锁的事务里:并发 DeleteAsset 拿同一把锁,
		// 无法在本次读期间**删完**,所以这里返回的 existing 不会是一个正被删的幽灵 id。
		existing, derr := s.lockedDedupLookup(ctx, opts.DatasetID, report.SHA256)
		if derr != nil {
			return nil, fmt.Errorf("dedup lookup: %w", derr)
		}
		if existing != nil {
			return s.dedupHit(ctx, existing, report), nil
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
	// frame index / thumbnail); C3.1b: volume 同样入队(volume_meta + 逐切片 PNG16)。
	// media-worker 轮询 PreprocessStatus=pending。**两条上传路径要同时改**——
	// 只在 RegisterAsset(多段)里入队、漏了这条普通上传,会让小体积 .nii 传上来
	// 却永远没有派生物,查看器空白且不报错(E2E 实测踩到)。
	if report.Status == qcPassed && dbmodel.NeedsPreprocess(ds.Modality) {
		asset.PreprocessStatus = dbmodel.PreprocessPending
	}

	// 唯一性属于数据库（M6）：ON CONFLICT (dataset_id, sha256) WHERE
	// qc_status='passed' DO NOTHING。上面的快路径查过一次，但「查」与「写」之间
	// 的窗口里另一个并发请求可能已经插了同一份内容——此时这里拿到 0 行，
	// 改取那一行返回，**表里绝不会出现第二行**。blob 是内容寻址的，重复 Put
	// 落在同一个键上，无需回滚。
	inserted, err := s.db.CreateAssetDedup(ctx, asset)
	if err != nil {
		// #15 对象已写、资产行插入失败 → blob 成孤儿(可能是 PHI)。登记进 GC outbox
		// 让 janitor 回收(best-effort;插入失败多是约束/瞬时,DB 通常还在)。
		if asset.StorageURI != "" {
			_ = s.db.EnqueueObjectGC(ctx, []dbmodel.ObjectGC{{StorageURI: asset.StorageURI, Reason: "orphan: asset insert failed"}})
		}
		return nil, fmt.Errorf("create asset row: %w", err)
	}
	if !inserted {
		existing, ferr := s.db.FindAssetBySHA256(ctx, opts.DatasetID, asset.SHA256)
		if ferr != nil || existing == nil {
			return nil, fmt.Errorf("dedup conflict but existing row unreadable: %w", ferr)
		}
		return s.dedupHit(ctx, existing, report), nil
	}

	res := &UploadResult{Asset: asset, Report: report, Deduplicated: false}
	// #14 QC 通过即建标注任务,且**不吞错**。旧代码 `if err == nil` 把任务创建失败静默
	// 丢掉,返回成功 → 资产存在却没有任务、永远无法被标注,重传走 dedup 又不补 → 永久
	// 无任务资产。ensureTaskForAsset 幂等(先查后建),失败大声记日志(资产已在,任务可
	// 在下次 dedup 命中时补上,见下方两处),而不是假装成功。
	if task, err := s.ensureTaskForAsset(ctx, asset); err != nil {
		slog.Error("ensure task for asset failed (asset persisted, task will be retried on re-upload)", "asset_id", asset.ID, "error", err)
	} else {
		res.Task = task
	}
	return res, nil
}

// lockedDedupLookup reads the existing asset for (datasetID, sha) while holding the
// per-content advisory lock (#20), so a concurrent DeleteAsset can't finish during
// the read. Returns (nil, nil) when no live asset exists → caller falls through to
// the normal insert path (whose unique constraint is the real correctness backstop).
func (s *AssetService) lockedDedupLookup(ctx context.Context, datasetID uint, sha string) (*dbmodel.Asset, error) {
	var existing *dbmodel.Asset
	err := s.db.DB.Transaction(func(tx *gorm.DB) error {
		if e := s.db.AcquireDedupUploadLockTx(ctx, tx, datasetID, sha); e != nil {
			return e
		}
		ex, e := s.db.WithTx(tx).FindAssetBySHA256(ctx, datasetID, sha)
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		existing = ex
		return nil
	})
	return existing, err
}

// dedupHit builds a dedup UploadResult, **ensuring the deduped asset has a task**
// first (#14 self-heal): if the very first upload's task creation failed, the
// asset would otherwise stay task-less forever because re-uploads just short-circuit
// to dedup. report may be nil (RegisterAsset path carries no QC report).
func (s *AssetService) dedupHit(ctx context.Context, existing *dbmodel.Asset, report *QCReport) *UploadResult {
	if _, err := s.ensureTaskForAsset(ctx, existing); err != nil {
		slog.Error("ensure task on dedup-hit failed", "asset_id", existing.ID, "error", err)
	}
	return &UploadResult{Asset: existing, Report: report, Deduplicated: true}
}

// ensureTaskForAsset creates the initial annotation task for a QC-passed asset if
// it doesn't already have one (#14). **Idempotent** — safe to call on both the
// fresh upload and every dedup-hit, so a task-creation failure on the first upload
// is repaired on any later re-upload of the same content instead of leaving a
// permanently task-less asset. Returns (nil, nil) when tasks are disabled, the QC
// failed, or a task already exists.
func (s *AssetService) ensureTaskForAsset(ctx context.Context, asset *dbmodel.Asset) (*dbmodel.AnnotationTask, error) {
	if s.tasks == nil || asset == nil || asset.QCStatus != dbmodel.QCStatusPassed {
		return nil, nil
	}
	existing, err := s.db.FindAnnotationTaskIDsByAsset(ctx, asset.ID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, nil // 已有任务:幂等,不再建
	}
	return s.tasks.CreateForAsset(ctx, asset, CreateTaskOptions{})
}

// GetAsset returns the asset row by id, caching under "asset:{id}" for
// assetMetaTTL — but **only once the asset is settled** (#13).
//
// ⚠️ 资产**不是**上传即不可变的(旧注释是假注释):宽高/preprocess_status/qc_status 在
// 上传后还会被 media worker 改。首次 GET 若在 pending(0 尺寸)时把整行缓存 1 小时,worker
// 随后改成 ready+真尺寸,GET 仍返回 0 尺寸整整一小时 → 下游归一化除零。所以只缓存**已
// 定型**的资产:需预处理的模态要 preprocess ready,不需预处理的(image/text,dims 在 QC
// 就出)上传即定型。删除时的缓存失效见 DeleteAsset(删行之后再清)。
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
	if s.cache != nil && assetSettled(asset) {
		s.cache.SetJSON(ctx, key, asset, assetMetaTTL)
	}
	return asset, nil
}

// assetSettled reports whether an asset's cacheable fields have stopped changing.
func assetSettled(a *dbmodel.Asset) bool {
	for _, m := range dbmodel.PreprocessModalities() {
		if a.Modality == m {
			return a.PreprocessStatus == dbmodel.PreprocessReady
		}
	}
	return true // image/text: 无预处理,dims 在 QC 就定型
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
	// 医学模态先过 DeidGate(C-H1),与 UploadImage 同一道闸——multipart 路径
	// 不是后门。非医学模态零额外查询。
	if dbmodel.IsMedicalModality(opts.Modality) {
		ds, err := s.db.FindDatasetByID(ctx, opts.DatasetID)
		if err != nil {
			return nil, fmt.Errorf("dataset lookup: %w", err)
		}
		if err := admitMedicalIngest(ctx, s.deidGate, ds, opts.UploaderID); err != nil {
			return nil, err
		}
	}
	if opts.SHA256 != "" {
		existing, derr := s.lockedDedupLookup(ctx, opts.DatasetID, opts.SHA256) // #20 见 UploadImage
		if derr != nil {
			return nil, fmt.Errorf("dedup lookup: %w", derr)
		}
		if existing != nil {
			return s.dedupHit(ctx, existing, nil), nil
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
	if dbmodel.NeedsPreprocess(opts.Modality) {
		asset.PreprocessStatus = dbmodel.PreprocessPending
	}
	// 与 UploadImage 同款（M6）：并发注册同一内容时由唯一约束兜底，输家取现存行。
	inserted, err := s.db.CreateAssetDedup(ctx, asset)
	if err != nil {
		// #15 对象已写、资产行插入失败 → blob 成孤儿(可能是 PHI)。登记进 GC outbox
		// 让 janitor 回收(best-effort;插入失败多是约束/瞬时,DB 通常还在)。
		if asset.StorageURI != "" {
			_ = s.db.EnqueueObjectGC(ctx, []dbmodel.ObjectGC{{StorageURI: asset.StorageURI, Reason: "orphan: asset insert failed"}})
		}
		return nil, fmt.Errorf("create asset row: %w", err)
	}
	if !inserted {
		existing, ferr := s.db.FindAssetBySHA256(ctx, opts.DatasetID, opts.SHA256)
		if ferr != nil || existing == nil {
			return nil, fmt.Errorf("dedup conflict but existing row unreadable: %w", ferr)
		}
		return s.dedupHit(ctx, existing, nil), nil
	}
	res := &UploadResult{Asset: asset}
	if s.tasks != nil {
		if task, err := s.tasks.CreateForAsset(ctx, asset, CreateTaskOptions{}); err == nil {
			res.Task = task
		}
	}
	return res, nil
}
