package repository

import (
	"context"
	"testing"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/testutil"

	"gorm.io/gorm"
)

// 存储生命周期底座:GC outbox 的语义锁——事务内入队与关系删除原子、领取带租约、
// 完成删行、失败重排。跑真 Postgres(migration 000005)。
func TestObjectGC_EnqueueClaimResolveReschedule(t *testing.T) {
	repo := &DB{DB: testutil.DB(t, RunMigrations)}
	ctx := context.Background()

	// 事务内入队(模拟"删关系行 + 登记待删对象"同事务)。
	err := repo.DB.Transaction(func(tx *gorm.DB) error {
		return repo.EnqueueObjectGCTx(ctx, tx, []dbmodel.ObjectGC{
			{StorageURI: "local://a/source", Reason: "t"},
			{StorageURI: "local://a/slices/", IsPrefix: true, Reason: "t"},
		})
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if n, _ := repo.CountObjectGC(ctx); n != 2 {
		t.Fatalf("queue depth = %d, want 2", n)
	}

	// 领取:两条都到期(next_attempt_at 默认 now),都应被领到,且被 park 到未来(租约)。
	lease := time.Now().Add(5 * time.Minute)
	rows, err := repo.ClaimDueObjectGC(ctx, lease, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("claim = %d rows (err=%v), want 2", len(rows), err)
	}
	if rows[0].Attempts != 1 {
		t.Fatalf("claim 应 attempts+1,得到 %d", rows[0].Attempts)
	}
	// 领取后立刻再领应为空(都被 park 到 5 分钟后,不会被第二个 janitor 重复处理)。
	again, _ := repo.ClaimDueObjectGC(ctx, lease, 10)
	if len(again) != 0 {
		t.Fatalf("已领取的行不该被重复领取,得到 %d", len(again))
	}

	// 成功:resolve 删行。失败:reschedule 保留行、记错误、排到过去(下轮可再领)。
	if err := repo.ResolveObjectGC(ctx, rows[0].ID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := repo.RescheduleObjectGC(ctx, rows[1].ID, "store down", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if n, _ := repo.CountObjectGC(ctx); n != 1 {
		t.Fatalf("resolve 后应剩 1 行,得到 %d", n)
	}
	// 重排到过去 → 下一轮能再领到(重试直到成功)。
	retry, _ := repo.ClaimDueObjectGC(ctx, lease, 10)
	if len(retry) != 1 || retry[0].ID != rows[1].ID {
		t.Fatalf("重排的行应可再次领取,得到 %+v", retry)
	}
	if retry[0].Attempts != 2 || retry[0].LastError != "store down" {
		t.Fatalf("重试应 attempts=2 且记录错误,得到 attempts=%d err=%q", retry[0].Attempts, retry[0].LastError)
	}
}
