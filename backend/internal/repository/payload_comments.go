package repository

// payload_comments.go — 审核批注的 Postgres 载荷仓储
// (执行方案-07)。

import (
	"context"
	"errors"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// ErrReviewCommentNotFound is returned when the comment id does not exist.
var ErrReviewCommentNotFound = errors.New("review comment not found")

// InsertReviewComment stores a new comment and returns its hex id.
func (r *DB) InsertReviewComment(ctx context.Context, c *paymodel.ReviewComment) (string, error) {
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Status == "" {
		c.Status = paymodel.ReviewCommentOpen
	}
	c.ID = NewHexID()
	payload, err := marshalPayload(c)
	if err != nil {
		return "", err
	}
	err = r.DB.WithContext(ctx).Exec(
		`INSERT INTO review_comments (id, task_id, dataset_id, asset_id, status, author_id, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?)`,
		c.ID, c.TaskID, c.DatasetID, c.AssetID, c.Status, c.AuthorID, payload, c.CreatedAt, c.UpdatedAt,
	).Error
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

// ListReviewCommentsByTask returns a task's comments, oldest first.
func (r *DB) ListReviewCommentsByTask(ctx context.Context, taskID uint) ([]paymodel.ReviewComment, error) {
	return listPayloads[paymodel.ReviewComment](ctx, r.DB,
		`SELECT payload FROM review_comments WHERE task_id = ? ORDER BY created_at`, taskID)
}

// FindReviewComment loads one comment by id, scoped to its task.
//
// ⚠️ task_id 必须进 WHERE(#5):批注挂在 /tasks/:id/review-comments/:cid 下,
// 但 resolve/delete 只按 cid 查改 → 拿 B 任务的 cid 打 A 的接口能跨任务改审核
// 闸门/删记录。所有按 cid 的读改都带上 task_id,跨任务直接 NotFound。
func (r *DB) FindReviewComment(ctx context.Context, taskID uint, id string) (*paymodel.ReviewComment, error) {
	c, err := firstPayload[paymodel.ReviewComment](ctx, r.DB,
		`SELECT payload FROM review_comments WHERE id = ? AND task_id = ? LIMIT 1`, id, taskID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrReviewCommentNotFound
	}
	return c, nil
}

// SetReviewCommentStatus flips a comment between open and resolved (task-scoped, #5).
func (r *DB) SetReviewCommentStatus(ctx context.Context, taskID uint, id string, status string, userID uint) error {
	now := time.Now().UTC()
	var (
		query string
		args  []any
	)
	if status == paymodel.ReviewCommentResolved {
		delta, err := jsonDelta(map[string]any{
			"status": status, "updated_at": now, "resolved_by": userID, "resolved_at": now,
		})
		if err != nil {
			return err
		}
		query = `UPDATE review_comments SET status = ?, updated_at = ?, payload = payload || ?::jsonb WHERE id = ? AND task_id = ?`
		args = []any{status, now, delta, id, taskID}
	} else {
		delta, err := jsonDelta(map[string]any{"status": status, "updated_at": now})
		if err != nil {
			return err
		}
		query = `UPDATE review_comments
		         SET status = ?, updated_at = ?, payload = (payload - 'resolved_by' - 'resolved_at') || ?::jsonb
		         WHERE id = ? AND task_id = ?`
		args = []any{status, now, delta, id, taskID}
	}
	res := r.DB.WithContext(ctx).Exec(query, args...)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrReviewCommentNotFound
	}
	return nil
}

// DeleteReviewComment removes a comment by id, scoped to its task (#5).
func (r *DB) DeleteReviewComment(ctx context.Context, taskID uint, id string) error {
	res := r.DB.WithContext(ctx).Exec(`DELETE FROM review_comments WHERE id = ? AND task_id = ?`, id, taskID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrReviewCommentNotFound
	}
	return nil
}

// CountOpenReviewComments reports how many comments still await the annotator.
func (r *DB) CountOpenReviewComments(ctx context.Context, taskID uint) (int64, error) {
	var n int64
	err := r.DB.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM review_comments WHERE task_id = ? AND status = ?`,
		taskID, paymodel.ReviewCommentOpen).Scan(&n).Error
	return n, err
}
