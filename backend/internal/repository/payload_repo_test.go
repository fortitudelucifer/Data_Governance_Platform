package repository

// 载荷仓储(执行方案-07)的语义锁:这些不是"能存能取"的冒烟,而是把载荷层
// 最容易走样的几条行为逐一钉死——乐观锁 stale=409、快照幂等重放、
// 每任务单 active、裁决清除不动 version、payload 与提升列不漂移。
// 跑在真 Postgres 上(testutil.DB:独立 schema + 真 goose 迁移)。

import (
	"context"
	"errors"
	"reflect"
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

// #12(病理线 review 逮到):导出读快照必须只取每个任务**最新一轮 finalize** 的快照。
// 返工重新定稿会新增一轮(新 final_annotation,version 递增),旧轮快照并不清除
// (按 (final_annotation_id, track_id) 保存,不同轮不同 fa_id 各留一行)。
// StreamTrackSnapshotsByDataset 若不按最新轮过滤 → **上一轮删掉的区域复活、改过的
// 区域出现两份**,而且不报错——导出交付物里最危险的静默错。
// 变异验证:去掉查询里的 final_annotation_id 子查询,本测试必红(A / B-old 混入)。
func TestPayload_ExportReadsOnlyLatestFinalizeRound(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	mkSnap := func(faID string, trackID int, label string) *paymodel.TrackSnapshot {
		return &paymodel.TrackSnapshot{
			TaskID: taskID, DatasetID: dsID, AssetID: assetID,
			FinalAnnotationID: faID, TrackID: trackID, Label: label, Kind: paymodel.TrackKindPolygon,
			Keyframes: []paymodel.Keyframe{{Frame: 0, Points: []float64{0, 0, 10, 0, 10, 10}}},
		}
	}

	// 第一轮定稿:区域 A(track 1)、B(track 2)。
	fa1 := &paymodel.FinalAnnotation{TaskID: taskID, AssetID: assetID, DatasetID: dsID, Version: 1}
	if err := repo.InsertFinalAnnotation(ctx, fa1); err != nil {
		t.Fatalf("fa1: %v", err)
	}
	for _, s := range []*paymodel.TrackSnapshot{mkSnap(fa1.ID, 1, "A"), mkSnap(fa1.ID, 2, "B-old")} {
		if err := repo.UpsertTrackSnapshot(ctx, s); err != nil {
			t.Fatalf("snap fa1: %v", err)
		}
	}

	// 返工后第二轮定稿:删掉 A、改了 B(track 2)、新增 C(track 3)。
	fa2 := &paymodel.FinalAnnotation{TaskID: taskID, AssetID: assetID, DatasetID: dsID, Version: 2}
	if err := repo.InsertFinalAnnotation(ctx, fa2); err != nil {
		t.Fatalf("fa2: %v", err)
	}
	for _, s := range []*paymodel.TrackSnapshot{mkSnap(fa2.ID, 2, "B-new"), mkSnap(fa2.ID, 3, "C")} {
		if err := repo.UpsertTrackSnapshot(ctx, s); err != nil {
			t.Fatalf("snap fa2: %v", err)
		}
	}

	var labels []string
	if _, err := repo.StreamTrackSnapshotsByDataset(ctx, dsID, []uint{taskID}, func(s *paymodel.TrackSnapshot) error {
		labels = append(labels, s.Label)
		return nil
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}

	got := map[string]bool{}
	for _, l := range labels {
		got[l] = true
	}
	if len(labels) != 2 || !got["B-new"] || !got["C"] || got["A"] || got["B-old"] {
		t.Fatalf("导出应只出最新一轮 {B-new, C},得到 %v(A 复活或 B 重复 = 跨轮快照混入)", labels)
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

	ok, err := repo.UpdateTrackByVersion(ctx, taskID, tr.ID, 1, map[string]any{"label": "person"}, 9)
	if err != nil || !ok {
		t.Fatalf("first update should apply (ok=%v err=%v)", ok, err)
	}
	// 用旧 version 再改 → stale。
	ok, err = repo.UpdateTrackByVersion(ctx, taskID, tr.ID, 1, map[string]any{"label": "bike"}, 9)
	if err != nil {
		t.Fatalf("stale update err: %v", err)
	}
	if ok {
		t.Fatalf("stale version must NOT apply(否则两个标注员互相覆盖而无 409)")
	}
	// #1 跨任务作用域:拿别的任务 id 打本任务(taskID+1)必须 0 命中(不改到别人的 track)。
	ok, err = repo.UpdateTrackByVersion(ctx, taskID+1, tr.ID, 2, map[string]any{"label": "hijack"}, 9)
	if err != nil {
		t.Fatalf("cross-task update err: %v", err)
	}
	if ok {
		t.Fatalf("#1 回归:带错 task_id 的更新绝不能命中(否则跨任务越权改 track)")
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
	// 快照总是指向一个已存在的 final_annotation(finalize 先插 fa 再写快照)。导出按
	// 最新轮过滤会正确排除孤儿快照,所以夹具也得先建 fa,否则读回来是空(见 #12)。
	if err := repo.InsertFinalAnnotation(ctx, &paymodel.FinalAnnotation{ID: "fa-1", TaskID: taskID, AssetID: assetID, DatasetID: dsID, Version: 1}); err != nil {
		t.Fatalf("seed final_annotation: %v", err)
	}

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

// cells 轨迹(病理 C2)的 instances[] 必须经载荷层 jsonb 往返无损,并随快照进入
// 导出真源(track_snapshots)。payload 是整块 marshal/unmarshal,但**没被断言的
// 字段等于没被锁**——这条把 instances(含 bbox/bbox-local RLE/class/score)存进去、
// 读回来逐字段相等、且整片拷进快照后仍无损钉死。
// 变异验证:给 paymodel.Keyframe.Instances 去掉 json tag(或改名),本测试必须红。
func TestPayload_CellsInstancesRoundTrip(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	cells := []paymodel.CellInstance{
		{Bbox: []float64{10, 10, 20, 20}, RLE: &paymodel.MaskRLE{Size: [2]int{20, 20}, Counts: "0f8Kd0"}, Class: "tumor", Score: 0.91},
		{Bbox: []float64{40, 42, 15, 16}, RLE: &paymodel.MaskRLE{Size: [2]int{16, 15}, Counts: "3aP1b"}, Class: "immune", Score: 0.5},
	}
	tr := &paymodel.Track{
		TaskID: taskID, DatasetID: dsID, AssetID: assetID,
		TrackID: 1, Label: "roi-1", Kind: paymodel.TrackKindCells, Source: paymodel.TrackSourceAI,
		Keyframes: []paymodel.Keyframe{{Frame: 0, TsMs: 0, Instances: cells}},
	}
	if err := repo.InsertTrack(ctx, tr); err != nil {
		t.Fatalf("insert cells track: %v", err)
	}

	// ① Insert → 读回:kind 与 instances 逐字段无损(bbox-local RLE 经 jsonb 往返不漂)。
	got, err := repo.FindTrackByID(ctx, tr.ID)
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Kind != paymodel.TrackKindCells {
		t.Fatalf("kind drifted through payload: %q", got.Kind)
	}
	if len(got.Keyframes) != 1 || !reflect.DeepEqual(got.Keyframes[0].Instances, cells) {
		t.Fatalf("instances drifted through payload round-trip:\n got  %+v\n want %+v", got.Keyframes[0].Instances, cells)
	}

	// ② 快照即导出真源:finalize 整片拷贝关键帧,instances 随之进 track_snapshots,
	//    StreamSnapshots 读出仍逐字段相等。
	snap := &paymodel.TrackSnapshot{
		TaskID: taskID, DatasetID: dsID, AssetID: assetID,
		FinalAnnotationID: "fa-cells", TrackID: 1, Label: "roi-1", Kind: paymodel.TrackKindCells,
		Keyframes: got.Keyframes, // 整片拷贝,与 final_annotation_service 一致
	}
	// 导出按最新轮过滤会排除孤儿快照,所以先建 fa(生产里 finalize 也是先 fa 后快照)。
	if err := repo.InsertFinalAnnotation(ctx, &paymodel.FinalAnnotation{ID: "fa-cells", TaskID: taskID, AssetID: assetID, DatasetID: dsID, Version: 1}); err != nil {
		t.Fatalf("seed final_annotation: %v", err)
	}
	if err := repo.UpsertTrackSnapshot(ctx, snap); err != nil {
		t.Fatalf("snapshot upsert: %v", err)
	}
	seen := 0
	if _, err := repo.StreamTrackSnapshotsByDataset(ctx, dsID, nil, func(s *paymodel.TrackSnapshot) error {
		seen++
		if len(s.Keyframes) != 1 || !reflect.DeepEqual(s.Keyframes[0].Instances, cells) {
			t.Fatalf("instances lost in snapshot(导出真源):\n got %+v", s.Keyframes[0].Instances)
		}
		return nil
	}); err != nil {
		t.Fatalf("stream snapshots: %v", err)
	}
	if seen != 1 {
		t.Fatalf("streamed %d snapshots, want 1", seen)
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
	second := mk("success")
	if err := repo.UpsertAIRun(ctx, second); err != nil {
		t.Fatalf("replay: %v", err)
	}
	// #11 冲突时对象的 id 必须被回写成库里的既有 id(RETURNING),否则调用方/Redis
	// 拿到这次新生成的幽灵 id、库里却是 first.ID。second 进来时是个全新 hex id,
	// upsert 后必须变成 first.ID。
	if second.ID != first.ID {
		t.Fatalf("#11 回归:冲突后对象 id 应回写为既有 id %s,得到 %s", first.ID, second.ID)
	}
	runs, err := repo.FindAIRunsByTask(ctx, taskID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %d (err=%v), want 1", len(runs), err)
	}
	if runs[0].Status != "success" || runs[0].ID != first.ID {
		t.Fatalf("replay should update in place keeping id: %+v", runs[0])
	}
}

// #8 终稿每 (task_id,version) 至多一份:双 Finalize 返回同一 id、库里只有一行。
func TestPayload_FinalAnnotationIdempotent(t *testing.T) {
	repo, dsID, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	mk := func() *paymodel.FinalAnnotation {
		return &paymodel.FinalAnnotation{TaskID: taskID, AssetID: assetID, DatasetID: dsID, Version: 1}
	}
	a, b := mk(), mk()
	if err := repo.InsertFinalAnnotation(ctx, a); err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	if err := repo.InsertFinalAnnotation(ctx, b); err != nil {
		t.Fatalf("second finalize (idempotent replay): %v", err)
	}
	// b 进来时是全新 id;幂等冲突后必须回写成 a 的 id(否则快照会按一个不存在的
	// final_annotation_id 落,导出读空)。
	if b.ID != a.ID {
		t.Fatalf("#8 回归:重复 Finalize 应返回既有 id %s,得到 %s", a.ID, b.ID)
	}
	latest, err := repo.FindLatestFinalAnnotation(ctx, taskID)
	if err != nil || latest == nil || latest.ID != a.ID {
		t.Fatalf("#8 回归:应只有一行终稿且 id=%s,得到 %+v (err=%v)", a.ID, latest, err)
	}
}

// #4 人工标注并发双写(都基于同一版本写下一版)撞唯一键 → 后写者拿 409、先写者内容保住。
func TestPayload_HumanAnnotationVersionConflict(t *testing.T) {
	repo, _, assetID, taskID := seedTaskFixture(t)
	ctx := context.Background()

	if err := repo.UpsertActiveHumanAnnotation(ctx, &paymodel.HumanAnnotation{TaskID: taskID, AssetID: assetID, Version: 1}); err != nil {
		t.Fatalf("v1: %v", err)
	}
	// 两个客户端都读到 v1、都写 v2。第一个成功。
	if err := repo.UpsertActiveHumanAnnotation(ctx, &paymodel.HumanAnnotation{TaskID: taskID, AssetID: assetID, Version: 2}); err != nil {
		t.Fatalf("first v2: %v", err)
	}
	// 第二个也写 v2 → 撞 ux_human_annotations_task_version → ErrOptimisticConflict,
	// 而不是把第一个的 v2 静默盖成 inactive。
	err := repo.UpsertActiveHumanAnnotation(ctx, &paymodel.HumanAnnotation{TaskID: taskID, AssetID: assetID, Version: 2})
	if !errors.Is(err, ErrOptimisticConflict) {
		t.Fatalf("#4 回归:并发同版本双写应返回 ErrOptimisticConflict,得到 %v", err)
	}
	// 先写者的 v2 仍是 active(没被后写者顶掉)。
	got, err := repo.FindActiveHumanAnnotation(ctx, taskID)
	if err != nil || got == nil || got.Version != 2 {
		t.Fatalf("#4 回归:先写者的 v2 应保住 active,得到 %+v (err=%v)", got, err)
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
	if err := repo.SetReviewCommentStatus(ctx, taskID, id, paymodel.ReviewCommentResolved, 7); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	c, err := repo.FindReviewComment(ctx, taskID, id)
	if err != nil || c.ResolvedBy == nil || *c.ResolvedBy != 7 {
		t.Fatalf("resolved_by should be 7: %+v (err=%v)", c, err)
	}
	// #5 跨任务作用域:拿别的 task_id 去 resolve/find/delete 本评论,一律 0 命中。
	if err := repo.SetReviewCommentStatus(ctx, taskID+1, id, paymodel.ReviewCommentOpen, 7); err != ErrReviewCommentNotFound {
		t.Fatalf("#5 回归:错 task_id 的 resolve 必须 NotFound,得到 %v", err)
	}
	if _, err := repo.FindReviewComment(ctx, taskID+1, id); err != ErrReviewCommentNotFound {
		t.Fatalf("#5 回归:错 task_id 的 find 必须 NotFound,得到 %v", err)
	}
	if err := repo.DeleteReviewComment(ctx, taskID+1, id); err != ErrReviewCommentNotFound {
		t.Fatalf("#5 回归:错 task_id 的 delete 必须 NotFound,得到 %v", err)
	}
	if err := repo.SetReviewCommentStatus(ctx, taskID, id, paymodel.ReviewCommentOpen, 7); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	c, _ = repo.FindReviewComment(ctx, taskID, id)
	if c.ResolvedBy != nil || c.ResolvedAt != nil {
		t.Fatalf("reopen must clear resolved_*: %+v", c)
	}
	if err := repo.DeleteReviewComment(ctx, taskID, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := repo.DeleteReviewComment(ctx, taskID, id); err != ErrReviewCommentNotFound {
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
