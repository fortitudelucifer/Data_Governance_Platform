package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DB wraps the GORM connection to the relational database (PostgreSQL).
type DB struct {
	DB *gorm.DB
}

// SetDatasetIndustryTags replaces the industry tag associations for a dataset.
func (r *DB) SetDatasetIndustryTags(ctx context.Context, datasetID uint, tagIDs []uint) error {
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("dataset_id = ?", datasetID).Delete(&dbmodel.DatasetIndustryTag{}).Error; err != nil {
			return err
		}
		for _, tagID := range tagIDs {
			dt := dbmodel.DatasetIndustryTag{DatasetID: datasetID, TagID: tagID}
			if err := tx.Create(&dt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// NewDB connects to PostgreSQL and applies pending goose migrations.
// Postgres 是唯一方言（执行方案-06 D1/M1）：
// schema 也不再有 AutoMigrate 这第二份真源（M3/P-H3）——dev 上绿、prod 上炸的
// 「两份 schema 静默漂移」从结构上消失。dsn 形如
// postgres://user:pass@host:5432/data_governance?sslmode=disable。
func NewDB(dsn string) (*DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// PH-2 连接池上限（M8：不再有「仅非 .db」的条件——只有一种数据库了）。
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(80)
		sqlDB.SetMaxIdleConns(25)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	repo := &DB{DB: db}

	if err := RunMigrations(repo.DB); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return repo, nil
}

// CreateUser inserts a new user record into the database.
func (r *DB) CreateUser(ctx context.Context, user *dbmodel.User) error {
	return r.DB.WithContext(ctx).Create(user).Error
}

// ---------------------------------------------------------------------------
// User
// ---------------------------------------------------------------------------

// FindUserByUsername looks up a user by username.
func (r *DB) FindUserByUsername(ctx context.Context, username string) (*dbmodel.User, error) {
	var user dbmodel.User
	if err := r.DB.WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// FindUserByID looks up a user by their primary key ID.
func (r *DB) FindUserByID(ctx context.Context, id uint) (*dbmodel.User, error) {
	var user dbmodel.User
	if err := r.DB.WithContext(ctx).First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// ListUsersWithCount returns a page of users and the total count.
func (r *DB) ListUsersWithCount(ctx context.Context, page, pageSize int) ([]dbmodel.User, int64, error) {
	var users []dbmodel.User
	var total int64
	offset := (page - 1) * pageSize
	db := r.DB.WithContext(ctx)
	if err := db.Model(&dbmodel.User{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Offset(offset).Limit(pageSize).Order("created_at DESC").Find(&users).Error; err != nil {
		return nil, 0, err
	}
	return users, total, nil
}

// SaveUser persists all fields of an existing user record.
func (r *DB) SaveUser(ctx context.Context, user *dbmodel.User) error {
	return r.DB.WithContext(ctx).Save(user).Error
}

// UpdateUserFields applies a partial update to the user identified by id.
// Returns the number of rows affected and any error.
func (r *DB) UpdateUserFields(ctx context.Context, id uint, updates map[string]interface{}) (int64, error) {
	result := r.DB.WithContext(ctx).Model(&dbmodel.User{}).Where("id = ?", id).Updates(updates)
	return result.RowsAffected, result.Error
}

// DeleteUser removes a user by id (PH-12)。返回受影响行数。
func (r *DB) DeleteUser(ctx context.Context, id uint) (int64, error) {
	result := r.DB.WithContext(ctx).Where("id = ?", id).Delete(&dbmodel.User{})
	return result.RowsAffected, result.Error
}

// ---------------------------------------------------------------------------
// Category
// ---------------------------------------------------------------------------

// CreateCategory inserts a new dataset category.
func (r *DB) CreateCategory(ctx context.Context, cat *dbmodel.DatasetCategory) error {
	return r.DB.WithContext(ctx).Create(cat).Error
}

// CategoryWithCount wraps a category with its associated dataset count.
type CategoryWithCount struct {
	dbmodel.DatasetCategory
	DatasetCount int64 `json:"dataset_count"`
}

// ListCategories returns all categories with the number of datasets in each.
func (r *DB) ListCategories(ctx context.Context) ([]CategoryWithCount, error) {
	var results []CategoryWithCount
	err := r.DB.WithContext(ctx).Model(&dbmodel.DatasetCategory{}).
		Select("dataset_categories.*, COALESCE(dc.cnt, 0) AS dataset_count").
		Joins("LEFT JOIN (SELECT category_id, COUNT(*) AS cnt FROM datasets GROUP BY category_id) dc ON dc.category_id = dataset_categories.id").
		Scan(&results).Error
	return results, err
}

// UpdateCategory updates an existing category by ID.
func (r *DB) UpdateCategory(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.DatasetCategory{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteCategory removes a category by ID.
func (r *DB) DeleteCategory(ctx context.Context, id uint) error {
	return r.DB.WithContext(ctx).Delete(&dbmodel.DatasetCategory{}, id).Error
}

// ---------------------------------------------------------------------------
// Tag
// ---------------------------------------------------------------------------

// CreateTag inserts a new tag.
func (r *DB) CreateTag(ctx context.Context, tag *dbmodel.Tag) error {
	return r.DB.WithContext(ctx).Create(tag).Error
}

// ListTags returns all tags. If tagType is not nil, only tags of that type are returned.
func (r *DB) ListTags(ctx context.Context, tagType *string) ([]dbmodel.Tag, error) {
	var tags []dbmodel.Tag
	query := r.DB.WithContext(ctx).Model(&dbmodel.Tag{})
	if tagType != nil && *tagType != "" {
		query = query.Where("type = ?", *tagType)
	}
	err := query.Find(&tags).Error
	return tags, err
}

// UpdateTag updates an existing tag by ID.
func (r *DB) UpdateTag(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Tag{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteTag removes a tag by ID.
func (r *DB) DeleteTag(ctx context.Context, id uint) error {
	return r.DB.WithContext(ctx).Delete(&dbmodel.Tag{}, id).Error
}

// FindTagByName looks up a tag by name and type.
func (r *DB) FindTagByName(ctx context.Context, name string, tagType string) (*dbmodel.Tag, error) {
	var tag dbmodel.Tag
	if err := r.DB.WithContext(ctx).Where("name = ? AND type = ?", name, tagType).First(&tag).Error; err != nil {
		return nil, err
	}
	return &tag, nil
}

// ---------------------------------------------------------------------------
// Dataset
// ---------------------------------------------------------------------------

// CreateDataset inserts a new dataset record.
func (r *DB) CreateDataset(ctx context.Context, ds *dbmodel.Dataset) error {
	return r.DB.WithContext(ctx).Create(ds).Error
}

// nameLike builds a case-insensitive substring match on datasets.name.
//
// LIKE wildcards in the user's input must be escaped, or a search for "100%"
// matches every row.
func nameLike(name string) (clause, arg string, ok bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(name))
	return `LOWER(datasets.name) LIKE ? ESCAPE '\'`, "%" + esc + "%", true
}

// DatasetFilter holds optional filters for listing datasets.
type DatasetFilter struct {
	CategoryID *uint
	TagIDs     []uint
	// Name is a case-insensitive substring match. Search must happen in SQL:
	// filtering the *current page* client-side means a dataset on page 5 can
	// never be found (真踩过：e2e 换到净库后，搜索直接搜不到目标数据集)。
	Name string
}

type DatasetListSort struct {
	By    string
	Order string
}

type DatasetPage struct {
	Items    []dbmodel.Dataset `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}

type DatasetOption struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

type DatasetListItem struct {
	ID   uint
	Name string
}

// ListDatasets returns datasets with optional category and tag filtering.
// Results include preloaded Category and Tags associations.
func (r *DB) ListDatasets(ctx context.Context, filter DatasetFilter) ([]dbmodel.Dataset, error) {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Dataset{})

	if filter.CategoryID != nil {
		query = query.Where("datasets.category_id = ?", *filter.CategoryID)
	}

	if clause, arg, ok := nameLike(filter.Name); ok {
		query = query.Where(clause, arg)
	}

	if len(filter.TagIDs) > 0 {
		query = query.Where("datasets.id IN (?)",
			r.DB.WithContext(ctx).Table("dataset_tags").
				Select("dataset_id").
				Where("tag_id IN ?", filter.TagIDs).
				Group("dataset_id").
				Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs)),
		)
	}

	var datasets []dbmodel.Dataset
	err := query.Preload("Category").Preload("Tags").Preload("IndustryTags").Preload("DatasetFunction").Find(&datasets).Error
	return datasets, err
}

func (r *DB) CountDatasets(ctx context.Context, filter DatasetFilter) (int64, error) {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Dataset{})

	if filter.CategoryID != nil {
		query = query.Where("datasets.category_id = ?", *filter.CategoryID)
	}

	if clause, arg, ok := nameLike(filter.Name); ok {
		query = query.Where(clause, arg)
	}

	if len(filter.TagIDs) > 0 {
		query = query.Where("datasets.id IN (?)",
			r.DB.WithContext(ctx).Table("dataset_tags").
				Select("dataset_id").
				Where("tag_id IN ?", filter.TagIDs).
				Group("dataset_id").
				Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs)),
		)
	}

	var total int64
	err := query.Count(&total).Error
	return total, err
}

func (r *DB) ListDatasetListItems(ctx context.Context, filter DatasetFilter) ([]DatasetListItem, error) {
	query := r.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Select("datasets.id", "datasets.name")

	if filter.CategoryID != nil {
		query = query.Where("datasets.category_id = ?", *filter.CategoryID)
	}

	if clause, arg, ok := nameLike(filter.Name); ok {
		query = query.Where(clause, arg)
	}

	if len(filter.TagIDs) > 0 {
		query = query.Where("datasets.id IN (?)",
			r.DB.WithContext(ctx).Table("dataset_tags").
				Select("dataset_id").
				Where("tag_id IN ?", filter.TagIDs).
				Group("dataset_id").
				Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs)),
		)
	}

	var items []DatasetListItem
	err := query.Find(&items).Error
	return items, err
}

func (r *DB) ListDatasetsPage(ctx context.Context, filter DatasetFilter, sortOpt DatasetListSort, page int, pageSize int) ([]dbmodel.Dataset, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}

	query := r.DB.WithContext(ctx).Model(&dbmodel.Dataset{})

	if filter.CategoryID != nil {
		query = query.Where("datasets.category_id = ?", *filter.CategoryID)
	}

	if clause, arg, ok := nameLike(filter.Name); ok {
		query = query.Where(clause, arg)
	}

	if len(filter.TagIDs) > 0 {
		query = query.Where("datasets.id IN (?)",
			r.DB.WithContext(ctx).Table("dataset_tags").
				Select("dataset_id").
				Where("tag_id IN ?", filter.TagIDs).
				Group("dataset_id").
				Having("COUNT(DISTINCT tag_id) = ?", len(filter.TagIDs)),
		)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	order := "asc"
	if strings.EqualFold(sortOpt.Order, "descending") || strings.EqualFold(sortOpt.Order, "desc") {
		order = "desc"
	}

	switch sortOpt.By {
	case "doc_count":
		query = query.Order("datasets.doc_count " + order)
	case "annotation_type":
		query = query.Order("datasets.annotation_type " + order)
	case "case_type":
		query = query.Order("datasets.case_type " + order)
	case "created_at":
		query = query.Order("datasets.created_at " + order)
	case "updated_at":
		query = query.Order("datasets.updated_at " + order)
	default:
		query = query.Order("datasets.id asc")
	}

	offset := (page - 1) * pageSize
	var datasets []dbmodel.Dataset
	err := query.Offset(offset).Limit(pageSize).Preload("Category").Preload("Tags").Preload("IndustryTags").Preload("DatasetFunction").Find(&datasets).Error
	return datasets, total, err
}

func (r *DB) ListDatasetOptions(ctx context.Context) ([]DatasetOption, error) {
	var options []DatasetOption
	err := r.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Select("id", "name").Find(&options).Error
	return options, err
}

func (r *DB) FindDatasetsByIDs(ctx context.Context, ids []uint) ([]dbmodel.Dataset, error) {
	if len(ids) == 0 {
		return []dbmodel.Dataset{}, nil
	}

	var datasets []dbmodel.Dataset
	if err := r.DB.WithContext(ctx).Where("id IN ?", ids).Preload("Category").Preload("Tags").Preload("IndustryTags").Preload("DatasetFunction").Find(&datasets).Error; err != nil {
		return nil, err
	}

	indexByID := make(map[uint]int, len(ids))
	for i, id := range ids {
		indexByID[id] = i
	}

	sort.SliceStable(datasets, func(i, j int) bool {
		return indexByID[datasets[i].ID] < indexByID[datasets[j].ID]
	})

	return datasets, nil
}

// UpdateDataset updates an existing dataset by ID.
func (r *DB) UpdateDataset(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteDataset removes a dataset by ID.
func (r *DB) DeleteDataset(ctx context.Context, id uint) error {
	return r.DB.WithContext(ctx).Delete(&dbmodel.Dataset{}, id).Error
}

// UpdateDocCount sets the doc_count for a dataset.
func (r *DB) UpdateDocCount(ctx context.Context, datasetID uint, count int) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Where("id = ?", datasetID).Update("doc_count", count).Error
}

// SyncDatasetCounters recomputes all dashboard counter columns for a single
// dataset from the underlying document repository. It is safe to call after
// any document mutation and is the source of truth for dashboard statistics.
func (r *DB) SyncDatasetCounters(ctx context.Context, docDB DocumentDB, datasetID uint) error {
	stats, err := docDB.GetDashboardStats(ctx, &datasetID)
	if err != nil {
		return fmt.Errorf("aggregate dataset %d stats failed: %w", datasetID, err)
	}
	updates := map[string]interface{}{
		"doc_count":              stats.DocCount,
		"not_annotated_count":    stats.StageDistribution["not_annotated"],
		"auto_annotating_count":  stats.StageDistribution["auto_annotating"],
		"auto_annotated_count":   stats.StageDistribution["auto_annotated"],
		"auto_failed_count":      stats.StageDistribution["auto_failed"],
		"refining_count":         stats.StageDistribution["refining"],
		"refined_count":          stats.StageDistribution["refined"],
		"reviewed_count":         stats.StageDistribution["reviewed"],
		"qa_total":               stats.QATotal,
	}
	return r.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Where("id = ?", datasetID).UpdateColumns(updates).Error
}

// SyncAllDatasetCounters recomputes counter columns for every dataset. Useful
// for one-off backfills or admin repair operations.
func (r *DB) SyncAllDatasetCounters(ctx context.Context, docDB DocumentDB) error {
	var ids []uint
	if err := r.DB.WithContext(ctx).Model(&dbmodel.Dataset{}).Order("id ASC").Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("list dataset ids failed: %w", err)
	}
	for _, id := range ids {
		if err := r.SyncDatasetCounters(ctx, docDB, id); err != nil {
			return err
		}
	}
	return nil
}

// FindDatasetByID retrieves a dataset by its primary key, preloading
// Category, Tags, and DatasetFunction.
func (r *DB) FindDatasetByID(ctx context.Context, id uint) (*dbmodel.Dataset, error) {
	var ds dbmodel.Dataset
	if err := r.DB.WithContext(ctx).Preload("Category").Preload("Tags").Preload("IndustryTags").Preload("DatasetFunction").First(&ds, id).Error; err != nil {
		return nil, err
	}
	return &ds, nil
}

// ---------------------------------------------------------------------------
// Dataset-Tag association
// ---------------------------------------------------------------------------

// SetDatasetTags replaces the tag associations for a dataset.
// It deletes all existing associations and inserts the new set within a
// transaction.
func (r *DB) SetDatasetTags(ctx context.Context, datasetID uint, tagIDs []uint) error {
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete existing associations
		if err := tx.Where("dataset_id = ?", datasetID).Delete(&dbmodel.DatasetTag{}).Error; err != nil {
			return err
		}
		// Insert new associations
		for _, tagID := range tagIDs {
			dt := dbmodel.DatasetTag{DatasetID: datasetID, TagID: tagID}
			if err := tx.Create(&dt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Audit Log
// ---------------------------------------------------------------------------

// CreateAuditLog inserts a new audit log entry.
func (r *DB) CreateAuditLog(ctx context.Context, log *dbmodel.AuditLog) error {
	return r.DB.WithContext(ctx).Create(log).Error
}

// CreateAnnotationLog inserts a new annotation log entry.
func (r *DB) CreateAnnotationLog(ctx context.Context, log *dbmodel.AnnotationLog) error {
	return r.DB.WithContext(ctx).Create(log).Error
}

// AuditLogFilter holds optional filters for querying audit logs.
type AuditLogFilter struct {
	StartTime *time.Time
	EndTime   *time.Time
	Action    *string
	Page      int
	PageSize  int
}

// AuditLogResult contains paginated audit log results.
type AuditLogResult struct {
	Logs  []dbmodel.AuditLog
	Total int64
}

// QueryAuditLogs returns audit logs matching the given filter with pagination.
func (r *DB) QueryAuditLogs(ctx context.Context, filter AuditLogFilter) (*AuditLogResult, error) {
	query := r.DB.WithContext(ctx).Model(&dbmodel.AuditLog{})

	if filter.StartTime != nil {
		query = query.Where("created_at >= ?", *filter.StartTime)
	}
	if filter.EndTime != nil {
		query = query.Where("created_at <= ?", *filter.EndTime)
	}
	if filter.Action != nil {
		query = query.Where("action = ?", *filter.Action)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 {
		pageSize = 20
	}

	var logs []dbmodel.AuditLog
	err := query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&logs).Error
	if err != nil {
		return nil, err
	}

	return &AuditLogResult{Logs: logs, Total: total}, nil
}
