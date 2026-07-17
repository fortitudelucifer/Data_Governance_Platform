package iface

import (
	"context"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// DBAnnotationTaskRepo is the minimal relational-DB interface required by
// AnnotationTaskService. The concrete implementation is *repository.DB.
type DBAnnotationTaskRepo interface {
	CreateAnnotationTask(ctx context.Context, task *dbmodel.AnnotationTask) error
	FindAnnotationTaskByID(ctx context.Context, id uint) (*dbmodel.AnnotationTask, error)
	UpdateAnnotationTask(ctx context.Context, id uint, updates map[string]interface{}) error
	BatchUpdateAnnotationTasks(ctx context.Context, ids []uint, updates map[string]interface{}) (int64, error)
	ListAnnotationTasksPage(ctx context.Context, filter repository.AnnotationTaskFilter, page, pageSize int) ([]dbmodel.AnnotationTask, int64, error)
	FindAdjacentTaskIDsByUser(ctx context.Context, userID, currentTaskID uint) (prevID, nextID *uint, err error)
	FindAdjacentAssetIDs(ctx context.Context, datasetID, currentAssetID uint) (prevID, nextID *uint, err error)
}
