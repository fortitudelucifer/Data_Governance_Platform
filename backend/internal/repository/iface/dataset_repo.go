package iface

import (
	"context"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// DBDatasetRepo is the minimal relational-DB interface required by DatasetService.
// The concrete implementation is *repository.DB.
type DBDatasetRepo interface {
	// Dataset CRUD
	CreateDataset(ctx context.Context, ds *dbmodel.Dataset) error
	FindDatasetByID(ctx context.Context, id uint) (*dbmodel.Dataset, error)
	FindDatasetsByIDs(ctx context.Context, ids []uint) ([]dbmodel.Dataset, error)
	ListDatasetListItems(ctx context.Context, filter repository.DatasetFilter) ([]repository.DatasetListItem, error)
	ListDatasetsPage(ctx context.Context, filter repository.DatasetFilter, sortOpt repository.DatasetListSort, page, pageSize int) ([]dbmodel.Dataset, int64, error)
	ListDatasetOptions(ctx context.Context) ([]repository.DatasetOption, error)
	UpdateDataset(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteDataset(ctx context.Context, id uint) error
	UpdateDocCount(ctx context.Context, datasetID uint, count int) error
	SetDatasetTags(ctx context.Context, datasetID uint, tagIDs []uint) error
	SetDatasetIndustryTags(ctx context.Context, datasetID uint, tagIDs []uint) error

	// Category CRUD
	CreateCategory(ctx context.Context, cat *dbmodel.DatasetCategory) error
	ListCategories(ctx context.Context) ([]repository.CategoryWithCount, error)
	UpdateCategory(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteCategory(ctx context.Context, id uint) error

	// Tag CRUD
	CreateTag(ctx context.Context, tag *dbmodel.Tag) error
	ListTags(ctx context.Context, tagType *string) ([]dbmodel.Tag, error)
	UpdateTag(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteTag(ctx context.Context, id uint) error
	FindTagByName(ctx context.Context, name, tagType string) (*dbmodel.Tag, error)
}
