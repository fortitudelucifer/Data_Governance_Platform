package repository

import (
	"context"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// media_repo.go — repository methods for the derived-asset pipeline (plan_v2
// T0.3): leasing audio/video assets due for preprocessing and recording the
// produced derivatives.

// LeaseDuePreprocessAssets atomically claims up to limit audio/video/volume
// assets that are QC-passed and pending/failed-and-due for preprocessing.
// Claimed rows are flipped to "running" with a lease + bumped attempts, so a
// concurrent worker (or a second instance) skips them until the lease expires.
//
// ⚠️ 这里的模态白名单必须与 MediaWorker.derive 的分支、以及两条上传路径的
// "入队 pending" 条件**三处一致**。少改一处的症状是:资产 pending 挂着、
// worker 永远不认领、派生物永远不出现,**没有任何报错**(E2E 实测踩到:
// volume 已入队、derive 分支也写了,唯独这里的白名单没加,卡了整条竖切)。
func (r *DB) LeaseDuePreprocessAssets(ctx context.Context, leaseUntil time.Time, limit int) ([]dbmodel.Asset, error) {
	if limit <= 0 {
		return nil, nil
	}
	now := time.Now()
	var assets []dbmodel.Asset
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		base := func(q *gorm.DB) *gorm.DB {
			return q.Model(&dbmodel.Asset{}).
				Select("id").
				Where("modality IN ?", dbmodel.PreprocessModalities()).
				Where("qc_status = ?", dbmodel.QCStatusPassed).
				// #11 认领集三条,分别修两个漏洞:
				//  · pending:租约到期/从未租(NULL)可领。
				//  · running:worker **崩溃后卡在 running**——旧查询只认 pending/failed,租约
				//    过期也不重领,资产永久卡死。显式认领"running 且租约过期"。
				//  · failed:**必须 next_attempt_at IS NOT NULL** 才领。旧查询把 NULL 当"立即
				//    到期",而 MaxRetries 后的终止失败正是写 failed+next_at=NULL → 被当立即
				//    到期无限无退避重试。带 next_at 的才是"排了退避重试"的,才领。
				Where(`(
					(preprocess_status = ? AND (preprocess_lease_until IS NULL OR preprocess_lease_until <= ?))
					OR (preprocess_status = ? AND preprocess_lease_until IS NOT NULL AND preprocess_lease_until <= ?)
					OR (preprocess_status = ? AND preprocess_next_attempt_at IS NOT NULL AND preprocess_next_attempt_at <= ? AND (preprocess_lease_until IS NULL OR preprocess_lease_until <= ?))
				)`,
					dbmodel.PreprocessPending, now,
					dbmodel.PreprocessRunning, now,
					dbmodel.PreprocessFailed, now, now).
				Order("id asc").
				Limit(limit)
		}
		// FOR UPDATE SKIP LOCKED：多 worker 并发抢占互不阻塞。
		var ids []uint
		q := base(tx.Session(&gorm.Session{})).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if err := q.Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Model(&dbmodel.Asset{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"preprocess_status":      dbmodel.PreprocessRunning,
				"preprocess_lease_until": leaseUntil,
				"preprocess_attempts":    gorm.Expr("preprocess_attempts + 1"),
			}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Order("id asc").Find(&assets).Error
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

// MarkPreprocessReady flips an asset to ready and clears error/lease.
func (r *DB) MarkPreprocessReady(ctx context.Context, id uint) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"preprocess_status":          dbmodel.PreprocessReady,
			"preprocess_error":           "",
			"preprocess_lease_until":     nil,
			"preprocess_next_attempt_at": nil,
		}).Error
}

// MarkPreprocessFailed records an error and schedules the next retry (backoff).
// retryAt nil means terminal failure (no further auto-retry).
func (r *DB) MarkPreprocessFailed(ctx context.Context, id uint, errMsg string, retryAt *time.Time) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"preprocess_status":          dbmodel.PreprocessFailed,
			"preprocess_error":           errMsg,
			"preprocess_lease_until":     nil,
			"preprocess_next_attempt_at": retryAt,
		}).Error
}

// MarkPreprocessRejected marks a terminal preprocessing failure (e.g. an
// unsupported codec). Status "rejected" is NOT in the worker claim set
// (pending/failed), so it is never re-picked — no retry storm.
func (r *DB) MarkPreprocessRejected(ctx context.Context, id uint, errMsg string) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"preprocess_status":          dbmodel.PreprocessRejected,
			"preprocess_error":           errMsg,
			"preprocess_lease_until":     nil,
			"preprocess_next_attempt_at": nil,
		}).Error
}

// UpdateAssetMediaMeta backfills probed audio/video metadata. width/height are
// the *display* dims (rotation already applied by the probe) and matter beyond
// the player: the COCO/YOLO exporters normalise coordinates by them.
func (r *DB) UpdateAssetMediaMeta(ctx context.Context, id uint, durationMs *int64, fps *float64, sampleRate *int, width, height *int) error {
	updates := map[string]interface{}{}
	if durationMs != nil {
		updates["duration_ms"] = *durationMs
	}
	if fps != nil {
		updates["fps"] = *fps
	}
	if sampleRate != nil {
		updates["sample_rate"] = *sampleRate
	}
	if width != nil && *width > 0 {
		updates["width"] = *width
	}
	if height != nil && *height > 0 {
		updates["height"] = *height
	}
	if len(updates) == 0 {
		return nil
	}
	return r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).Where("id = ?", id).Updates(updates).Error
}

// UpsertDerivative inserts or updates the derivative for (asset_id, kind).
func (r *DB) UpsertDerivative(ctx context.Context, d *dbmodel.AssetDerivative) error {
	return r.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "asset_id"}, {Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{"version", "params_hash", "storage_uri", "status", "size_bytes", "sha256", "error", "updated_at"}),
	}).Create(d).Error
}

// ListDerivatives returns all derivatives for an asset.
func (r *DB) ListDerivatives(ctx context.Context, assetID uint) ([]dbmodel.AssetDerivative, error) {
	var out []dbmodel.AssetDerivative
	err := r.DB.WithContext(ctx).Where("asset_id = ?", assetID).Order("kind asc").Find(&out).Error
	return out, err
}

// GetDerivative returns one derivative by (asset_id, kind), or gorm.ErrRecordNotFound.
func (r *DB) GetDerivative(ctx context.Context, assetID uint, kind string) (*dbmodel.AssetDerivative, error) {
	var d dbmodel.AssetDerivative
	if err := r.DB.WithContext(ctx).Where("asset_id = ? AND kind = ?", assetID, kind).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}
