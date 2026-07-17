package repository

// payload_annotations.go — 人工标注 / 终稿标注的 Postgres 载荷仓储
// (执行方案-07)。

import (
	"context"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"

	"gorm.io/gorm"
)

// ---- Human annotation -------------------------------------------------------

// UpsertActiveHumanAnnotation marks any prior active rows for the task inactive
// and inserts the supplied document as the new active version, in one
// transaction. 「每任务至多一份 active」曾是应用层约定,现在还有部分唯一索引
// ux_human_annotations_active 兜底——并发双写会撞约束而不是留下两份 active。
func (r *DB) UpsertActiveHumanAnnotation(ctx context.Context, ha *paymodel.HumanAnnotation) error {
	now := time.Now()
	if ha.CreatedAt.IsZero() {
		ha.CreatedAt = now
	}
	ha.UpdatedAt = now
	ha.IsActive = true
	if ha.ID == "" {
		ha.ID = NewHexID()
	}
	payload, err := marshalPayload(ha)
	if err != nil {
		return err
	}
	deactivate, err := jsonDelta(map[string]any{"is_active": false, "updated_at": now})
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE human_annotations
			 SET is_active = FALSE, updated_at = now(), payload = payload || ?::jsonb
			 WHERE task_id = ? AND is_active`,
			deactivate, ha.TaskID,
		).Error; err != nil {
			return err
		}
		return tx.Exec(
			`INSERT INTO human_annotations (id, task_id, asset_id, is_active, version, qa_status, payload, created_at, updated_at)
			 VALUES (?, ?, ?, TRUE, ?, ?, ?::jsonb, ?, ?)`,
			ha.ID, ha.TaskID, ha.AssetID, ha.Version, ha.QAStatus, payload, ha.CreatedAt, ha.UpdatedAt,
		).Error
	})
}

// FindActiveHumanAnnotation returns the active human annotation for a task.
func (r *DB) FindActiveHumanAnnotation(ctx context.Context, taskID uint) (*paymodel.HumanAnnotation, error) {
	return firstPayload[paymodel.HumanAnnotation](ctx, r.DB,
		`SELECT payload FROM human_annotations WHERE task_id = ? AND is_active LIMIT 1`, taskID)
}

// UpdateHumanAnnotationQAStatus updates the qa_status / reviewer / note for the
// active human annotation of a task.
func (r *DB) UpdateHumanAnnotationQAStatus(ctx context.Context, taskID uint, qaStatus string, reviewerID *uint, reviewNote string) error {
	delta := map[string]any{"qa_status": qaStatus, "review_note": reviewNote, "updated_at": time.Now()}
	if reviewerID != nil {
		delta["reviewer_id"] = *reviewerID
	}
	d, err := jsonDelta(delta)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`UPDATE human_annotations
		 SET qa_status = ?, updated_at = now(), payload = payload || ?::jsonb
		 WHERE task_id = ? AND is_active`,
		qaStatus, d, taskID,
	).Error
}

// ---- Final annotation --------------------------------------------------------

// InsertFinalAnnotation writes a new immutable FinalAnnotation row.
func (r *DB) InsertFinalAnnotation(ctx context.Context, fa *paymodel.FinalAnnotation) error {
	if fa.CreatedAt.IsZero() {
		fa.CreatedAt = time.Now()
	}
	if fa.ID == "" {
		fa.ID = NewHexID()
	}
	payload, err := marshalPayload(fa)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO final_annotations (id, task_id, asset_id, dataset_id, version, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?)`,
		fa.ID, fa.TaskID, fa.AssetID, fa.DatasetID, fa.Version, payload, fa.CreatedAt,
	).Error
}

// FindLatestFinalAnnotation returns the highest-version FinalAnnotation for a task.
func (r *DB) FindLatestFinalAnnotation(ctx context.Context, taskID uint) (*paymodel.FinalAnnotation, error) {
	return firstPayload[paymodel.FinalAnnotation](ctx, r.DB,
		`SELECT payload FROM final_annotations WHERE task_id = ? ORDER BY version DESC LIMIT 1`, taskID)
}

// StreamFinalAnnotationsByDataset iterates FinalAnnotation rows for a dataset
// (created_at ascending) and invokes writeFn for each.
func (r *DB) StreamFinalAnnotationsByDataset(ctx context.Context, datasetID uint, sinceTime *time.Time, taskIDs []uint, writeFn func(*paymodel.FinalAnnotation) error) (int, error) {
	q := `SELECT payload FROM final_annotations WHERE dataset_id = ?`
	args := []any{datasetID}
	if sinceTime != nil {
		q += ` AND created_at >= ?`
		args = append(args, *sinceTime)
	}
	if len(taskIDs) > 0 {
		q += ` AND task_id IN ?`
		args = append(args, taskIDs)
	}
	q += ` ORDER BY created_at`
	return streamPayloads[paymodel.FinalAnnotation](ctx, r.DB, q, writeFn, args...)
}
