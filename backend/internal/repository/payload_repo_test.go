package repository

// 载荷仓储(执行方案-07)的语义锁:这些不是"能存能取"的冒烟,而是把载荷层
// 最容易走样的几条行为逐一钉死——乐观锁 stale=409、快照幂等重放、
// 每任务单 active、裁决清除不动 version、payload 与提升列不漂移。
// 跑在真 Postgres 上(testutil.DB:独立 schema + 真 goose 迁移)。

import (
	"context"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/testutil"
)

// seedTaskFixture creates dataset → asset → task and returns the repo + ids.
// 载荷表的外键是真的,编造 id 插不进去——这正是 P-H1(嫁接)的葬礼。
func seedTaskFixture(t *testing.T) (*DB, uint, uint, uint) {
	t.Helper()
	repo := &DB{DB: testutil.DB(t, RunMigrations)}
	ctx := context.Background()

	ds := &dbmodel.Dataset{Name: "payload-fixture", Modality: dbmodel.ModalityVideo}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	asset := &dbmodel.Asset{DatasetID: ds.ID, Modality: "video", SHA256: strings.Repeat("ef", 32), QCStatus: dbmodel.QCStatusPassed}
	if err := repo.DB.Create(asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	task := &dbmodel.AnnotationTask{AssetID: asset.ID, DatasetID: ds.ID, State: dbmodel.TaskStateHumanPending}
	if err := repo.CreateAnnotationTask(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return repo, ds.ID, asset.ID, task.ID
}

func newTrack(taskID, dsID, assetID uint, num int) *paymodel.Track {
	return &paymodel.Track{
		TaskID: taskID, DatasetID: dsID, AssetID: assetID,
		TrackID: num, Label: "car", Source: paymodel.TrackSourceHuman,
		Keyframes: []paymodel.Keyframe{{Frame: 0, TsMs: 0, Bbox: []float64{1, 2, 3, 4}}},
	}
}

// 乐观锁:stale version 返回 applied=false(上层 409),库里不产生第二个版本。
func TestPayload_TrackOptimisticLock(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	tr := newTrack(taskID, dsID, assetID, 1)
	if err := repo.InsertTrack(ctx, tr); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ok, err := repo.UpdateTrackByVersion(ctx, tr.ID, 1, map[string]any{"label": "person"}, 9)
	if err != nil || !ok {
		t.Fatalf("first update should apply (ok=%v err=%v)", ok, err)
	}
	// 用旧 version 再改 → stale。
	ok, err = repo.UpdateTrackByVersion(ctx, tr.ID, 1, map[string]any{"label": "bike"}, 9)
	if err != nil {
		t.Fatalf("stale update err: %v", err)
	}
	if ok {
		t.Fatalf("stale version must NOT apply(否则两个标注员互相覆盖而无 409)")
	}

	got, err := repo.FindTrackByID(ctx, tr.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	// payload 与提升列必须同步:version=2、label=person,且关键帧原样保留。
	if got.Version != 2 || got.Label != "person" {
		t.Fatalf("payload drifted: version=%d label=%q, want 2/person", got.Version, got.Label)
	}
	if len(got.Keyframes) != 1 || got.Keyframes[0].Bbox[2] != 3 {
		t.Fatalf("keyframes lost through merge: %+v", got.Keyframes)
	}
	if got.UpdatedBy != 9 {
		t.Fatalf("updated_by = %d, want 9", got.UpdatedBy)
	}
}

// 同任务同 track_id 的第二条活跃 track 必须被唯一索引拒绝——导出以 track_id
// 为键,重复会把两个物体静默合并(B3 修过的真 bug,现在是 schema 的属性)。
// 变异验证:去掉 000002 里的 ux_tracks_task_num_active,本测试必须红。
func TestPayload_DuplicateActiveTrackNumberRejected(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	if err := repo.InsertTrack(ctx, newTrack(taskID, dsID, assetID, 7)); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := repo.InsertTrack(ctx, newTrack(taskID, dsID, assetID, 7)); err == nil {
		t.Fatalf("duplicate active track_id must violate ux_tracks_task_num_active")
	}
	// 归档后同号可以复用(部分索引只盖 active)。
	tracks, _ := repo.ListActiveTracksByTask(ctx, taskID, "", "")
	if err := repo.SetTrackActive(ctx, tracks[0].ID, false, 1); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := repo.InsertTrack(ctx, newTrack(taskID, dsID, assetID, 7)); err != nil {
		t.Fatalf("re-use after archive should pass: %v", err)
	}
}

// 快照幂等:同一 (final_annotation_id, track_id) 重放 N 次恰好一行,id 不变。
// 变异验证:去掉 UNIQUE(final_annotation_id, track_id),本测试必须红。
func TestPayload_SnapshotUpsertIdempotent(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	mk := func() *paymodel.TrackSnapshot {
		return &paymodel.TrackSnapshot{
			TaskID: taskID, DatasetID: dsID, AssetID: assetID,
			FinalAnnotationID: "fa-1", TrackID: 3, Label: "car",
			Keyframes: []paymodel.Keyframe{{Frame: 0, TsMs: 0, Bbox: []float64{1, 2, 3, 4}}},
		}
	}
	first := mk()
	if err := repo.UpsertTrackSnapshot(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	replay := mk()
	replay.Label = "car-replayed"
	if err := repo.UpsertTrackSnapshot(ctx, replay); err != nil {
		t.Fatalf("replay upsert: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay must keep the persisted id: %s vs %s", replay.ID, first.ID)
	}
	var n int64
	repo.DB.Raw(`SELECT COUNT(*) FROM track_snapshots WHERE final_annotation_id = 'fa-1' AND track_id = 3`).Scan(&n)
	if n != 1 {
		t.Fatalf("snapshot rows = %d, want exactly 1(finalize 必须可安全重放)", n)
	}

	got := 0
	if _, err := repo.StreamTrackSnapshotsByDataset(ctx, dsID, nil, func(s *paymodel.TrackSnapshot) error {
		got++
		if s.ID != first.ID || s.Label != "car-replayed" {
			t.Fatalf("stream sees id=%s label=%s", s.ID, s.Label)
		}
		return nil
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got != 1 {
		t.Fatalf("streamed %d snapshots, want 1", got)
	}
}

// 每任务至多一份 active 人工标注:Upsert 归档旧行、部分唯一索引兜底。
func TestPayload_SingleActiveHumanAnnotation(t *testing.T) {
	repo, _, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	for v := 1; v <= 3; v++ {
		ha := &paymodel.HumanAnnotation{TaskID: taskID, AssetID: assetID, Version: v, QAStatus: "draft"}
		if err := repo.UpsertActiveHumanAnnotation(ctx, ha); err != nil {
			t.Fatalf("upsert v%d: %v", v, err)
		}
	}
	var active int64
	repo.DB.Raw(`SELECT COUNT(*) FROM human_annotations WHERE task_id = ? AND is_active`, taskID).Scan(&active)
	if active != 1 {
		t.Fatalf("active rows = %d, want 1", active)
	}
	got, err := repo.FindActiveHumanAnnotation(ctx, taskID)
	if err != nil || got == nil || got.Version != 3 {
		t.Fatalf("active should be v3, got %+v (err=%v)", got, err)
	}
	// 历史版本保留(审计)。
	var total int64
	repo.DB.Raw(`SELECT COUNT(*) FROM human_annotations WHERE task_id = ?`, taskID).Scan(&total)
	if total != 3 {
		t.Fatalf("total rows = %d, want 3(旧版本要留档)", total)
	}
}

// 裁决:SetTrackReview 不动 version;清除把四个键从 payload 里真正拿掉。
func TestPayload_TrackReviewDoesNotTouchVersion(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	tr := newTrack(taskID, dsID, assetID, 1)
	if err := repo.InsertTrack(ctx, tr); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.SetTrackReview(ctx, tr.ID, paymodel.TrackReviewRejected, "框歪了", 5); err != nil {
		t.Fatalf("set review: %v", err)
	}
	n, _ := repo.CountRejectedActiveTracks(ctx, taskID)
	if n != 1 {
		t.Fatalf("rejected count = %d, want 1", n)
	}
	got, _ := repo.FindTrackByID(ctx, tr.ID)
	if got.Version != 1 {
		t.Fatalf("review must not bump version(标注员会平白丢乐观锁), got %d", got.Version)
	}
	if got.ReviewStatus != paymodel.TrackReviewRejected || got.ReviewNote != "框歪了" {
		t.Fatalf("review fields not persisted: %+v", got)
	}

	if err := repo.ClearTrackReviews(ctx, taskID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = repo.FindTrackByID(ctx, tr.ID)
	if got.ReviewStatus != "" || got.ReviewedBy != nil {
		t.Fatalf("verdict should be wiped, got %+v", got)
	}
	if n, _ := repo.CountRejectedActiveTracks(ctx, taskID); n != 0 {
		t.Fatalf("rejected count after clear = %d", n)
	}
}

// AIRun 幂等键 (task, capability, run_id):重放更新而不重复;id 保持首次值。
func TestPayload_AIRunUpsertIdempotent(t *testing.T) {
	repo, _, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	mk := func(status string) *paymodel.AIRun {
		return &paymodel.AIRun{
			RunID: "run-1", TaskID: taskID, AssetID: assetID,
			CapabilityType: "video.detect_track", Status: status,
		}
	}
	first := mk("running")
	if err := repo.UpsertAIRun(ctx, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := repo.UpsertAIRun(ctx, mk("success")); err != nil {
		t.Fatalf("replay: %v", err)
	}
	runs, err := repo.FindAIRunsByTask(ctx, taskID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %d (err=%v), want 1", len(runs), err)
	}
	if runs[0].Status != "success" || runs[0].ID != first.ID {
		t.Fatalf("replay should update in place keeping id: %+v", runs[0])
	}
}

// 批注:插入返回 id、resolve/reopen 的键增删、开放计数。
func TestPayload_ReviewCommentLifecycle(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	frame := 12
	id, err := repo.InsertReviewComment(ctx, &paymodel.ReviewComment{
		TaskID: taskID, DatasetID: dsID, AssetID: assetID, Body: "第 12 帧漏标", Frame: &frame, AuthorID: 5,
	})
	if err != nil || id == "" {
		t.Fatalf("insert: id=%q err=%v", id, err)
	}
	if n, _ := repo.CountOpenReviewComments(ctx, taskID); n != 1 {
		t.Fatalf("open = %d, want 1", n)
	}
	if err := repo.SetReviewCommentStatus(ctx, id, paymodel.ReviewCommentResolved, 7); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	c, err := repo.FindReviewComment(ctx, id)
	if err != nil || c.ResolvedBy == nil || *c.ResolvedBy != 7 {
		t.Fatalf("resolved_by should be 7: %+v (err=%v)", c, err)
	}
	if err := repo.SetReviewCommentStatus(ctx, id, paymodel.ReviewCommentOpen, 7); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c, _ = repo.FindReviewComment(ctx, id)
	if c.ResolvedBy != nil || c.ResolvedAt != nil {
		t.Fatalf("reopen must clear resolved_*: %+v", c)
	}
	if err := repo.DeleteReviewComment(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.DeleteReviewComment(ctx, id); err != ErrReviewCommentNotFound {
		t.Fatalf("double delete should be not-found, got %v", err)
	}
}

// P3 预演:删资产的载荷清理 + 外键级联 —— 嫁接不可能。
func TestPayload_DeleteMultiModalByAssetAndCascade(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	if err := repo.InsertTrack(ctx, newTrack(taskID, dsID, assetID, 1)); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := repo.UpsertActiveHumanAnnotation(ctx, &paymodel.HumanAnnotation{TaskID: taskID, AssetID: assetID, Version: 1}); err != nil {
		t.Fatalf("ha: %v", err)
	}
	if err := repo.InsertTraceLog(ctx, &paymodel.TraceLog{TaskID: taskID, CapabilityType: "x"}); err != nil {
		t.Fatalf("trace: %v", err)
	}

	if err := repo.DeleteMultiModalByAsset(ctx, assetID, []uint{taskID}); err != nil {
		t.Fatalf("delete payloads: %v", err)
	}
	for _, tab := range []string{"annotation_tracks", "human_annotations", "trace_logs"} {
		var n int64
		repo.DB.Raw(`SELECT COUNT(*) FROM ` + tab).Scan(&n)
		if n != 0 {
			t.Fatalf("%s 残留 %d 行", tab, n)
		}
	}
}
