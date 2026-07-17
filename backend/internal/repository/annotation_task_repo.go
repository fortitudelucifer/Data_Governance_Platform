package repository

import (
	"context"
	"fmt"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateAnnotationTask inserts a new AnnotationTask row.
func (r *DB) CreateAnnotationTask(ctx context.Context, task *dbmodel.AnnotationTask) error {
	return r.DB.WithContext(ctx).Create(task).Error
}

// FindAnnotationTaskByID returns the task by primary key.
func (r *DB) FindAnnotationTaskByID(ctx context.Context, id uint) (*dbmodel.AnnotationTask, error) {
	var task dbmodel.AnnotationTask
	if err := r.DB.WithContext(ctx).First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// AnnotationTaskFilter is the query filter for listing tasks.
type AnnotationTaskFilter struct {
	DatasetID  *uint
	State      *string
	AssetID    *uint
	AssetIDs   []uint // batch lookup by multiple asset IDs (takes precedence over AssetID)
	JobID      *uint
	AssigneeID *uint // admin query: tasks where assignee_id = X (AND logic)
	MineUserID *uint // "my tasks": tasks where assignee_id = X OR reviewer_id = X (OR logic)
}

// ListAnnotationTasksPage returns paginated tasks ordered by id desc.
func (r *DB) ListAnnotationTasksPage(ctx context.Context, filter AnnotationTaskFilter, page, pageSize int) ([]dbmodel.AnnotationTask, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}

	q := r.DB.WithContext(ctx).Model(&dbmodel.AnnotationTask{})
	if filter.DatasetID != nil {
		q = q.Where("dataset_id = ?", *filter.DatasetID)
	}
	if filter.State != nil && *filter.State != "" {
		q = q.Where("state = ?", *filter.State)
	}
	if len(filter.AssetIDs) > 0 {
		q = q.Where("asset_id IN ?", filter.AssetIDs)
	} else if filter.AssetID != nil {
		q = q.Where("asset_id = ?", *filter.AssetID)
	}
	if filter.JobID != nil {
		q = q.Where("job_id = ?", *filter.JobID)
	}
	if filter.MineUserID != nil {
		q = q.Where("(assignee_id = ? OR reviewer_id = ?)", *filter.MineUserID, *filter.MineUserID)
	} else if filter.AssigneeID != nil {
		q = q.Where("assignee_id = ?", *filter.AssigneeID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}
	var tasks []dbmodel.AnnotationTask
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error; err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, total, nil
}

// UpdateAnnotationTask applies partial updates to a task row.
func (r *DB) UpdateAnnotationTask(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.AnnotationTask{}).Where("id = ?", id).Updates(updates).Error
}

// BatchUpdateAnnotationTasks applies the same partial updates to all tasks
// whose IDs are in ids. Returns the number of rows affected.
func (r *DB) BatchUpdateAnnotationTasks(ctx context.Context, ids []uint, updates map[string]interface{}) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.DB.WithContext(ctx).Model(&dbmodel.AnnotationTask{}).Where("id IN ?", ids).Updates(updates)
	return res.RowsAffected, res.Error
}

// FindAdjacentTaskIDsByUser returns the task IDs immediately before and after
// currentTaskID among tasks where the given user is assignee or reviewer,
// ordered by task.id ascending. Either pointer is nil when no neighbour exists.
func (r *DB) FindAdjacentTaskIDsByUser(ctx context.Context, userID, currentTaskID uint) (prevID, nextID *uint, err error) {
	var prev, next dbmodel.AnnotationTask
	scope := r.DB.WithContext(ctx).Model(&dbmodel.AnnotationTask{}).
		Select("id").
		Where("assignee_id = ? OR reviewer_id = ?", userID, userID)
	if e := scope.Where("id < ?", currentTaskID).Order("id DESC").Limit(1).First(&prev).Error; e == nil {
		prevID = &prev.ID
	}
	if e := scope.Where("id > ?", currentTaskID).Order("id ASC").Limit(1).First(&next).Error; e == nil {
		nextID = &next.ID
	}
	return prevID, nextID, nil
}

// CASUpdateState performs a compare-and-set on the state column. Returns the
// number of rows affected (0 if expected state did not match).
func (r *DB) CASUpdateState(ctx context.Context, id uint, expected, next string, extra map[string]interface{}) (int64, error) {
	updates := map[string]interface{}{"state": next, "updated_at": time.Now()}
	for k, v := range extra {
		updates[k] = v
	}
	res := r.DB.WithContext(ctx).Model(&dbmodel.AnnotationTask{}).
		Where("id = ? AND state = ?", id, expected).
		Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// LeaseDueTasks atomically picks up to limit tasks whose state matches one of
// expectedStates and whose next_attempt_at is <= now (or NULL), assigns a
// lease until leaseUntil, and returns them. The caller must release / advance
// state when work completes.
func (r *DB) LeaseDueTasks(ctx context.Context, expectedStates []string, leaseUntil time.Time, limit int) ([]dbmodel.AnnotationTask, error) {
	if len(expectedStates) == 0 || limit <= 0 {
		return nil, nil
	}
	var tasks []dbmodel.AnnotationTask
	now := time.Now()
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Select candidate IDs first.
		// FOR UPDATE SKIP LOCKED：多 worker 并发 lease 互不阻塞。
		var ids []uint
		q := tx.Model(&dbmodel.AnnotationTask{}).
			Select("id").
			Where("state IN ?", expectedStates).
			Where("(next_attempt_at IS NULL OR next_attempt_at <= ?)", now).
			Where("(lease_until IS NULL OR lease_until <= ?)", now).
			Order("id asc").
			Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if err := q.Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Model(&dbmodel.AnnotationTask{}).
			Where("id IN ?", ids).
			Update("lease_until", leaseUntil).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Find(&tasks).Error
	})
	return tasks, err
}
