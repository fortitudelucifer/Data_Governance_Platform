package repository

import (
	"context"
	"errors"
	"fmt"

	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateAsset inserts a new Asset row.
func (r *DB) CreateAsset(ctx context.Context, asset *dbmodel.Asset) error {
	return r.DB.WithContext(ctx).Create(asset).Error
}

// CreateAssetDedup inserts an Asset row with
//
//	ON CONFLICT (dataset_id, sha256) WHERE qc_status = 'passed' DO NOTHING
//
// against the partial unique index idx_assets_dataset_sha（M6）。返回是否真的
// 插入了：false = 撞上了同数据集同内容的已通过资产（asset.ID 保持 0，调用方
// 再按 (dataset_id, sha256) 取那一行）。唯一性由数据库保证——两个并发的相同
// 文件上传，恰好一个 INSERT 赢，另一个拿到 0 行，**不可能再插出两行**。
// 旧实现是「先查后写」，查与写之间就是竞态窗口（执行方案-06 §2.3）。
func (r *DB) CreateAssetDedup(ctx context.Context, asset *dbmodel.Asset) (bool, error) {
	res := r.DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "dataset_id"}, {Name: "sha256"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{gorm.Expr("qc_status = 'passed'")}},
		DoNothing:   true,
	}).Create(asset)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FindAssetBySHA256 looks up an asset row by (dataset_id, sha256).
// Used for SHA256-based deduplication on upload (plan_v1/02 §2.1).
func (r *DB) FindAssetBySHA256(ctx context.Context, datasetID uint, sha string) (*dbmodel.Asset, error) {
	var asset dbmodel.Asset
	err := r.DB.WithContext(ctx).Where("dataset_id = ? AND sha256 = ?", datasetID, sha).First(&asset).Error
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

// ListAssetIDsByDataset returns every asset id in a dataset (删数据集时逐资产
// 清理 blob / 载荷行用，M7)。
func (r *DB) ListAssetIDsByDataset(ctx context.Context, datasetID uint) ([]uint, error) {
	var ids []uint
	err := r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).
		Where("dataset_id = ?", datasetID).Order("id asc").Pluck("id", &ids).Error
	return ids, err
}

// FindAssetByID returns the Asset by primary key.
func (r *DB) FindAssetByID(ctx context.Context, id uint) (*dbmodel.Asset, error) {
	var asset dbmodel.Asset
	if err := r.DB.WithContext(ctx).First(&asset, id).Error; err != nil {
		return nil, err
	}
	return &asset, nil
}

// AssetFilter is the query filter for listing assets.
type AssetFilter struct {
	DatasetID *uint
	QCStatus  *string
	Modality  *string
}

// ListAssetsPage returns paginated assets ordered by id desc.
func (r *DB) ListAssetsPage(ctx context.Context, filter AssetFilter, page, pageSize int) ([]dbmodel.Asset, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}

	q := r.DB.WithContext(ctx).Model(&dbmodel.Asset{})
	if filter.DatasetID != nil {
		q = q.Where("dataset_id = ?", *filter.DatasetID)
	}
	if filter.QCStatus != nil && *filter.QCStatus != "" {
		q = q.Where("qc_status = ?", *filter.QCStatus)
	}
	if filter.Modality != nil && *filter.Modality != "" {
		q = q.Where("modality = ?", *filter.Modality)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count assets: %w", err)
	}

	var assets []dbmodel.Asset
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&assets).Error; err != nil {
		return nil, 0, fmt.Errorf("list assets: %w", err)
	}
	return assets, total, nil
}

// UpdateAsset applies partial updates to an asset row.
func (r *DB) UpdateAsset(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteAsset hard-deletes an asset row.
func (r *DB) DeleteAsset(ctx context.Context, id uint) error {
	return r.DB.WithContext(ctx).Delete(&dbmodel.Asset{}, id).Error
}

// FindAnnotationTaskIDsByAsset returns the annotation_task IDs for an asset.
func (r *DB) FindAnnotationTaskIDsByAsset(ctx context.Context, assetID uint) ([]uint, error) {
	var ids []uint
	err := r.DB.WithContext(ctx).Model(&dbmodel.AnnotationTask{}).
		Where("asset_id = ?", assetID).Pluck("id", &ids).Error
	return ids, err
}

// DeleteAnnotationTasksByAsset hard-deletes all annotation tasks of an asset.
func (r *DB) DeleteAnnotationTasksByAsset(ctx context.Context, assetID uint) error {
	return r.DB.WithContext(ctx).Where("asset_id = ?", assetID).Delete(&dbmodel.AnnotationTask{}).Error
}

// DeleteDerivativesByAsset hard-deletes all derivative rows of an asset.
func (r *DB) DeleteDerivativesByAsset(ctx context.Context, assetID uint) error {
	return r.DB.WithContext(ctx).Where("asset_id = ?", assetID).Delete(&dbmodel.AssetDerivative{}).Error
}

// CountAssetsBySHA256Except counts other assets that share a content hash (the
// object store is content-addressed by sha256, so a blob may be shared — don't
// delete it if another asset still references it).
func (r *DB) CountAssetsBySHA256Except(ctx context.Context, sha string, excludeID uint) (int64, error) {
	if sha == "" {
		return 0, nil
	}
	var n int64
	err := r.DB.WithContext(ctx).Model(&dbmodel.Asset{}).
		Where("sha256 = ? AND id <> ?", sha, excludeID).Count(&n).Error
	return n, err
}

// FindAdjacentAssetIDs returns the IDs of the assets immediately before and
// after currentAssetID within the same dataset, ordered by asset.id ascending.
// Either returned pointer is nil when no such neighbour exists.
func (r *DB) FindAdjacentAssetIDs(ctx context.Context, datasetID, currentAssetID uint) (prevID, nextID *uint, err error) {
	var prev, next dbmodel.Asset
	if e := r.DB.WithContext(ctx).
		Select("id").
		Where("dataset_id = ? AND id < ?", datasetID, currentAssetID).
		Order("id DESC").Limit(1).
		First(&prev).Error; e == nil {
		prevID = &prev.ID
	}
	if e := r.DB.WithContext(ctx).
		Select("id").
		Where("dataset_id = ? AND id > ?", datasetID, currentAssetID).
		Order("id ASC").Limit(1).
		First(&next).Error; e == nil {
		nextID = &next.ID
	}
	return prevID, nextID, nil
}

// IsAssetNotFound is a small helper for handler-level error mapping.
func IsAssetNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
