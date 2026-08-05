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
	// #4 deactivate + insert 一个事务。并发双写(都基于 v4 写 v5)时,后写者会:
	// 先 deactivate 先写者的 v5、再 INSERT 自己的 v5 → 撞 ux_human_annotations_task_version
	// 唯一键 → **整个事务回滚**(含那步 deactivate)→ 先写者的 v5 原样保住,后写者
	// 拿到 ErrOptimisticConflict(409)。active 部分唯一索引挡不住这个丢更新,因为
	// 后写者是先把先写者置 inactive 再插的;真正的护栏是 (task_id,version) 唯一键。
	err = r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	if isUniqueViolation(err) {
		return ErrOptimisticConflict
	}
	return err
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

// InsertFinalAnnotation writes an immutable FinalAnnotation row **idempotently
// per (task_id, version)** (#8). 旧实现是裸 INSERT,注释却声称幂等——双 Finalize
// (重放 / QA 竞态)会造两行同版本终稿,FindLatest 的 ORDER BY version 在同版本间
// 不确定,还会牵连快照按错的 final_annotation_id 落。
//
// 终稿不可变:冲突时 **DO NOTHING、保留既有那一份**,并把 fa.ID 回填为库里的既有
// id(RETURNING 冲突不返回行,故再补一次 SELECT)——调用方(finalize 要把 fa.ID 写
// 进 track_snapshots 与 annotation_tasks)拿到的一定是权威 id,不是这次新生成、又没落
// 库的幽灵 id。
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
	var persistedID string
	err = r.DB.WithContext(ctx).Raw(
		`INSERT INTO final_annotations (id, task_id, asset_id, dataset_id, version, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?)
		 ON CONFLICT (task_id, version) DO NOTHING
		 RETURNING id`,
		fa.ID, fa.TaskID, fa.AssetID, fa.DatasetID, fa.Version, payload, fa.CreatedAt,
	).Scan(&persistedID).Error
	if err != nil {
		return err
	}
	if persistedID == "" {
		// 冲突(DO NOTHING 不返回行):既有终稿已在,取它的 id。
		if err := r.DB.WithContext(ctx).Raw(
			`SELECT id FROM final_annotations WHERE task_id = ? AND version = ? LIMIT 1`,
			fa.TaskID, fa.Version,
		).Scan(&persistedID).Error; err != nil {
			return err
		}
	}
	if persistedID != "" {
		fa.ID = persistedID
	}
	return nil
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
