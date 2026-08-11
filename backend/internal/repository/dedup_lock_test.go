package repository

import (
	"context"
	"testing"
	"time"

	"text-annotation-platform/internal/testutil"
)

// #20 (dataset_id, sha) advisory 锁必须真正串行化:一个事务持锁时,另一个事务拿同一把
// 锁应阻塞到前者提交——这正是"上传 dedup 读期间并发删除删不完"的机制。不同内容不互斥。
func TestDedupAdvisoryLock_Serializes(t *testing.T) {
	repo := &DB{DB: testutil.DB(t, RunMigrations)}
	ctx := context.Background()
	const ds = uint(7)
	const sha = "deadbeefcafe"

	tx1 := repo.DB.Begin()
	if err := repo.AcquireDedupUploadLockTx(ctx, tx1, ds, sha); err != nil {
		t.Fatalf("tx1 acquire: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		tx1.Commit() // 释放锁
		close(released)
	}()

	// 第二个事务拿同一把锁 → 应阻塞到 tx1 提交(≈300ms)。
	start := time.Now()
	tx2 := repo.DB.Begin()
	if err := repo.AcquireDedupUploadLockTx(ctx, tx2, ds, sha); err != nil {
		t.Fatalf("tx2 acquire: %v", err)
	}
	waited := time.Since(start)
	tx2.Commit()
	<-released

	if waited < 200*time.Millisecond {
		t.Fatalf("#20 回归:第二个事务应被 advisory 锁阻塞到第一个提交(≈300ms),实际只等了 %v", waited)
	}

	// 不同 (ds,sha):不互斥,立刻拿到。
	tx3 := repo.DB.Begin()
	s2 := time.Now()
	if err := repo.AcquireDedupUploadLockTx(ctx, tx3, ds, "different-content"); err != nil {
		t.Fatalf("tx3 acquire: %v", err)
	}
	if d := time.Since(s2); d > 100*time.Millisecond {
		t.Fatalf("不同内容的锁不该互斥,却等了 %v", d)
	}
	tx3.Commit()
}
