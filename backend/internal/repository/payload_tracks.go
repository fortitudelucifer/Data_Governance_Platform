package repository

// payload_tracks.go — 视频 track / FINALIZED 快照 / 提交轮次的 Postgres 载荷仓储
// (执行方案-07)。
//
// 语义逐条对齐旧实现:乐观锁 stale 返回 applied=false(上层发 409)、
// 快照按 (final_annotation_id, track_id) 幂等重放、SetTrackReview 刻意不动
// version(裁决是审核员元数据,不能让标注员丢乐观锁)。

import (
	"context"
	"errors"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"

	"gorm.io/gorm"
)

// ---- Tracks -------------------------------------------------------------------

// ListActiveTracksByTask returns active tracks for a task, optionally filtered
// by source (ai|human) and/or label. Ordered by track_id ascending.
func (r *DB) ListActiveTracksByTask(ctx context.Context, taskID uint, source, label string) ([]paymodel.Track, error) {
	q := `SELECT payload FROM annotation_tracks WHERE task_id = ? AND is_active`
	args := []any{taskID}
	if source != "" {
		q += ` AND source = ?`
		args = append(args, source)
	}
	if label != "" {
		q += ` AND label = ?`
		args = append(args, label)
	}
	q += ` ORDER BY track_id`
	return listPayloads[paymodel.Track](ctx, r.DB, q, args...)
}

// CountActiveTracksByTask counts active tracks for a task (上限校验).
func (r *DB) CountActiveTracksByTask(ctx context.Context, taskID uint) (int64, error) {
	var n int64
	err := r.DB.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM annotation_tracks WHERE task_id = ? AND is_active`, taskID).Scan(&n).Error
	return n, err
}

// MaxTrackNumber returns the highest track_id used on a task (active or not),
// so a new track can be assigned max+1. Returns 0 when none exist.
func (r *DB) MaxTrackNumber(ctx context.Context, taskID uint) (int, error) {
	var n int
	err := r.DB.WithContext(ctx).Raw(
		`SELECT COALESCE(MAX(track_id), 0) FROM annotation_tracks WHERE task_id = ?`, taskID).Scan(&n).Error
	return n, err
}

// ActiveTrackNumberTaken reports whether an active track already uses this
// logical track_id(导出以 track_id 为键;重复会静默合并两个物体)。
// schema 上另有部分唯一索引 ux_tracks_task_num_active 作最终防线。
func (r *DB) ActiveTrackNumberTaken(ctx context.Context, taskID uint, trackID int) (bool, error) {
	var n int64
	err := r.DB.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM annotation_tracks WHERE task_id = ? AND track_id = ? AND is_active`,
		taskID, trackID).Scan(&n).Error
	return n > 0, err
}

// FindTrackByID returns a single track by its string id; nil when absent.
func (r *DB) FindTrackByID(ctx context.Context, id string) (*paymodel.Track, error) {
	return firstPayload[paymodel.Track](ctx, r.DB,
		`SELECT payload FROM annotation_tracks WHERE id = ? LIMIT 1`, id)
}

// InsertTrack inserts a new track, assigning id / timestamps / version=1 /
// is_active=true. Mutates t in place(与旧实现一致)。
func (r *DB) InsertTrack(ctx context.Context, t *paymodel.Track) error {
	if t.ID == "" {
		t.ID = NewHexID()
	}
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	if t.Version == 0 {
		t.Version = 1
	}
	t.IsActive = true
	payload, err := marshalPayload(t)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO annotation_tracks (id, task_id, dataset_id, asset_id, track_id, label, source, is_active, version, review_status, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, TRUE, ?, NULLIF(?, ''), ?::jsonb, ?, ?)`,
		t.ID, t.TaskID, t.DatasetID, t.AssetID, t.TrackID, t.Label, t.Source,
		t.Version, t.ReviewStatus, payload, t.CreatedAt, t.UpdatedAt,
	).Error
}

// UpdateTrackByVersion applies mutable-field updates to an active track under an
// optimistic lock:匹配 {id, version==expected, is_active},合并字段并 version+1。
// stale 时返回 applied=false(无错误)→ 上层发 409。
// set 的键 = payload 顶层字段名;提升列(label/track_id)若在 set 里
// 同步更新,payload 与列在同一条 UPDATE 里维护。
func (r *DB) UpdateTrackByVersion(ctx context.Context, id string, expectedVersion int, set map[string]any, updatedBy uint) (bool, error) {
	now := time.Now()
	set["updated_at"] = now
	set["updated_by"] = updatedBy
	delta, err := jsonDelta(set)
	if err != nil {
		return false, err
	}
	res := r.DB.WithContext(ctx).Exec(
		`UPDATE annotation_tracks
		 SET version    = version + 1,
		     updated_at = ?,
		     label      = COALESCE(?, label),
		     track_id   = COALESCE(?, track_id),
		     payload    = payload || ?::jsonb || jsonb_build_object('version', version + 1)
		 WHERE id = ? AND version = ? AND is_active`,
		now, optString(set, "label"), optInt(set, "track_id"), delta, id, expectedVersion,
	)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// optString / optInt pull an optional promoted-column value out of the delta map.
func optString(m map[string]any, k string) *string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	return nil
}

func optInt(m map[string]any, k string) *int {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case int:
			return &n
		case int64:
			i := int(n)
			return &i
		case float64:
			i := int(n)
			return &i
		}
	}
	return nil
}

// SetTrackActive flips a track's is_active flag (archive on adopt/delete).
func (r *DB) SetTrackActive(ctx context.Context, id string, active bool, updatedBy uint) error {
	now := time.Now()
	delta, err := jsonDelta(map[string]any{"is_active": active, "updated_by": updatedBy, "updated_at": now})
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`UPDATE annotation_tracks SET is_active = ?, updated_at = ?, payload = payload || ?::jsonb WHERE id = ?`,
		active, now, delta, id,
	).Error
}

// SetTrackReview records a reviewer's per-track verdict. An empty status clears
// the verdict. 刻意不动 version:裁决是审核员元数据,不是标注内容。
func (r *DB) SetTrackReview(ctx context.Context, id, status, note string, reviewerID uint) error {
	now := time.Now().UTC()
	var res *gorm.DB
	if status == "" {
		clear, err := jsonDelta(map[string]any{"updated_at": now})
		if err != nil {
			return err
		}
		res = r.DB.WithContext(ctx).Exec(
			`UPDATE annotation_tracks
			 SET review_status = NULL, updated_at = ?,
			     payload = (payload - 'review_status' - 'review_note' - 'reviewed_by' - 'reviewed_at') || ?::jsonb
			 WHERE id = ?`,
			now, clear, id,
		)
	} else {
		delta, err := jsonDelta(map[string]any{
			"review_status": status, "review_note": note,
			"reviewed_by": reviewerID, "reviewed_at": now, "updated_at": now,
		})
		if err != nil {
			return err
		}
		res = r.DB.WithContext(ctx).Exec(
			`UPDATE annotation_tracks
			 SET review_status = ?, updated_at = ?, payload = payload || ?::jsonb
			 WHERE id = ?`,
			status, now, delta, id,
		)
	}
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CountRejectedActiveTracks reports how many active tracks are marked rejected
// — the gate on passing the whole task.
func (r *DB) CountRejectedActiveTracks(ctx context.Context, taskID uint) (int64, error) {
	var n int64
	err := r.DB.WithContext(ctx).Raw(
		`SELECT COUNT(*) FROM annotation_tracks WHERE task_id = ? AND is_active AND review_status = ?`,
		taskID, paymodel.TrackReviewRejected).Scan(&n).Error
	return n, err
}

// ClearTrackReviews wipes every per-track verdict on a task(返工后下一轮审核
// 从干净状态开始)。
func (r *DB) ClearTrackReviews(ctx context.Context, taskID uint) error {
	// 无条件对全任务行清裁决键:对本就没有这些键的行,`- 'key'` 是 no-op,
	// 语义与旧的 $exists 过滤一致(且避开 jsonb `?` 操作符与占位符的冲突)。
	return r.DB.WithContext(ctx).Exec(
		`UPDATE annotation_tracks
		 SET review_status = NULL,
		     payload = payload - 'review_status' - 'review_note' - 'reviewed_by' - 'reviewed_at'
		 WHERE task_id = ?`,
		taskID,
	).Error
}

// ---- Snapshots (FINALIZED, export source) ---------------------------------

// UpsertTrackSnapshot idempotently writes a FINALIZED track snapshot keyed by
// (final_annotation_id, track_id) so finalize/reconcile can replay safely.
// 冲突时保留既有 id;s.ID 回填为库里实际的 id(RETURNING)。
func (r *DB) UpsertTrackSnapshot(ctx context.Context, s *paymodel.TrackSnapshot) error {
	if s.ID == "" {
		s.ID = NewHexID()
	}
	if s.FinalizedAt.IsZero() {
		s.FinalizedAt = time.Now()
	}
	payload, err := marshalPayload(s)
	if err != nil {
		return err
	}
	var persistedID string
	err = r.DB.WithContext(ctx).Raw(
		`INSERT INTO track_snapshots (id, task_id, dataset_id, asset_id, final_annotation_id, track_id, label, finalized_at, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)
		 ON CONFLICT (final_annotation_id, track_id)
		 DO UPDATE SET payload      = EXCLUDED.payload || jsonb_build_object('id', track_snapshots.id),
		               label        = EXCLUDED.label,
		               finalized_at = EXCLUDED.finalized_at
		 RETURNING id`,
		s.ID, s.TaskID, s.DatasetID, s.AssetID, s.FinalAnnotationID, s.TrackID,
		s.Label, s.FinalizedAt, payload,
	).Scan(&persistedID).Error
	if err != nil {
		return err
	}
	s.ID = persistedID
	return nil
}

// StreamTrackSnapshotsByDataset iterates FINALIZED track snapshots for a dataset
// (optionally task-filtered), invoking writeFn per snapshot(视频导出器用)。
func (r *DB) StreamTrackSnapshotsByDataset(ctx context.Context, datasetID uint, taskIDs []uint, writeFn func(*paymodel.TrackSnapshot) error) (int, error) {
	q := `SELECT payload FROM track_snapshots WHERE dataset_id = ?`
	args := []any{datasetID}
	if len(taskIDs) > 0 {
		q += ` AND task_id IN ?`
		args = append(args, taskIDs)
	}
	q += ` ORDER BY task_id, track_id`
	return streamPayloads[paymodel.TrackSnapshot](ctx, r.DB, q, writeFn, args...)
}

// ---- Rounds(返工 diff 的原料)---------------------------------------------

// ErrTrackRoundNotFound is returned when the requested submission round does not exist.
var ErrTrackRoundNotFound = errors.New("submission round not found")

// MaxTrackRound returns the highest round captured for a task (0 = none yet).
func (r *DB) MaxTrackRound(ctx context.Context, taskID uint) (int, error) {
	var n int
	err := r.DB.WithContext(ctx).Raw(
		`SELECT COALESCE(MAX(round), 0) FROM track_rounds WHERE task_id = ?`, taskID).Scan(&n).Error
	return n, err
}

// InsertTrackRound stores a submission round. UNIQUE(task_id, round) 让双提交
// 竞态变成插入冲突而不是重复轮次(与旧唯一索引同语义)。
func (r *DB) InsertTrackRound(ctx context.Context, rd *paymodel.TrackRound) error {
	rd.ID = NewHexID()
	if rd.SubmittedAt.IsZero() {
		rd.SubmittedAt = time.Now()
	}
	payload, err := marshalPayload(rd)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO track_rounds (id, task_id, round, track_count, submitted_at, payload)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb)`,
		rd.ID, rd.TaskID, rd.Round, rd.TrackCount, rd.SubmittedAt, payload,
	).Error
}

// FindTrackRound loads one round by number.
func (r *DB) FindTrackRound(ctx context.Context, taskID uint, round int) (*paymodel.TrackRound, error) {
	rd, err := firstPayload[paymodel.TrackRound](ctx, r.DB,
		`SELECT payload FROM track_rounds WHERE task_id = ? AND round = ? LIMIT 1`, taskID, round)
	if err != nil {
		return nil, err
	}
	if rd == nil {
		return nil, ErrTrackRoundNotFound
	}
	return rd, nil
}

// ListTrackRoundMeta returns round metadata (no track payload), newest first —
// 轮次选择器不需要整车关键帧(payload - 'tracks' 与旧投影同语义)。
func (r *DB) ListTrackRoundMeta(ctx context.Context, taskID uint) ([]paymodel.TrackRound, error) {
	return listPayloads[paymodel.TrackRound](ctx, r.DB,
		`SELECT payload - 'tracks' FROM track_rounds WHERE task_id = ? ORDER BY round DESC`, taskID)
}
