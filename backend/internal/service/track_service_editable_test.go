package service

import (
	"context"
	"errors"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
)

// #15 后端强制:已冻结的交付物(FINALIZED/EXPORTED)不接受标注写入/删除——终稿后改 live
// track 会与 QA 冻结的快照漂移。前端有编辑锁/任务态门禁,后端这道是所有模态共用的兜底。
// 变异:去掉 Upsert/Delete 里的 taskAcceptsWrites 检查,本测试变红。
func TestUpsert_RejectsWritesToFrozenTask(t *testing.T) {
	repo, _, taskID := seedVolumeTask(t) // 建在 HUMAN_PENDING(可编辑)
	svc := &TrackService{db: repo, payload: repo, limits: DefaultTrackLimits()}
	ctx := context.Background()
	req := TrackUpsertRequest{Kind: paymodel.TrackKindBBox,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{1, 2, 3, 4}}}}

	// 可编辑态放行。
	if _, err := svc.Upsert(ctx, taskID, 1, req); err != nil {
		t.Fatalf("HUMAN_PENDING 应放行, got %v", err)
	}
	// 冻结成 FINALIZED → 写入 / 删除都必须被拒。
	if err := repo.DB.Exec(`UPDATE annotation_tasks SET state = ? WHERE id = ?`, dbmodel.TaskStateFinalized, taskID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Upsert(ctx, taskID, 1, req); !errors.Is(err, ErrTaskNotEditable) {
		t.Fatalf("FINALIZED 写入应被拒(ErrTaskNotEditable), got %v", err)
	}
	if err := svc.Delete(ctx, taskID, "whatever", 0, 1); !errors.Is(err, ErrTaskNotEditable) {
		t.Fatalf("FINALIZED 删除应被拒, got %v", err)
	}
}
