package service

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"

	"gorm.io/gorm"
)

// flakyStore fails the first failN Delete/DeletePrefix calls, then succeeds,
// recording every URI it was asked to delete.
type flakyStore struct {
	mu      sync.Mutex
	failN   int
	deleted []string
}

func (s *flakyStore) tryDelete(uri string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failN > 0 {
		s.failN--
		return errors.New("object store transient failure")
	}
	s.deleted = append(s.deleted, uri)
	return nil
}
func (s *flakyStore) Delete(ctx context.Context, uri string) error       { return s.tryDelete(uri) }
func (s *flakyStore) DeletePrefix(ctx context.Context, uri string) error { return s.tryDelete(uri) }

// The rest of ObjectStore is unused by the janitor; stub to satisfy the interface.
func (s *flakyStore) Put(context.Context, PutRequest) (PutResult, error) { return PutResult{}, nil }
func (s *flakyStore) PutAt(context.Context, string, io.Reader, int64, string) (PutResult, error) {
	return PutResult{}, nil
}
func (s *flakyStore) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (s *flakyStore) Stat(context.Context, string) (StatResult, error)   { return StatResult{}, nil }
func (s *flakyStore) Exists(context.Context, string) (bool, error)       { return false, nil }
func (s *flakyStore) URIForKey(key string) string                       { return key }
func (s *flakyStore) Driver() string                                    { return "flaky" }
func (s *flakyStore) PresignGetURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

// 底座:对象存储临时失败时,janitor 必须**重试到成功**,绝不丢弃 outbox 行(否则 blob
// 永久泄漏)。跑真 Postgres(migration 000005)。
func TestObjectGCWorker_RetriesUntilDeleted(t *testing.T) {
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	ctx := context.Background()

	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		return db.EnqueueObjectGCTx(ctx, tx, []dbmodel.ObjectGC{{StorageURI: "flaky://x/y", Reason: "test"}})
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	store := &flakyStore{failN: 2} // 头两次删除失败
	// Interval 极小 → 失败重排的 backoff 也极小,下一轮能立刻再领。
	w := NewObjectGCWorker(ObjectGCConfig{Interval: time.Millisecond, Batch: 10, LeaseTTL: time.Millisecond, MaxBackoff: time.Millisecond}, db, store)

	// 最多几轮:前两轮失败(行仍在队列、attempts 累加),第三轮成功(删对象 + 删行)。
	var depth int64
	for i := 0; i < 10; i++ {
		w.RunOnce(ctx)
		depth, _ = db.CountObjectGC(ctx)
		if depth == 0 {
			break
		}
		time.Sleep(3 * time.Millisecond) // 等 backoff 过期,下一轮可再领
	}
	if depth != 0 {
		t.Fatalf("janitor 应重试到成功清空 outbox,仍剩 %d 行", depth)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleted) != 1 || store.deleted[0] != "flaky://x/y" {
		t.Fatalf("对象应最终被删一次,得到 %v", store.deleted)
	}
}
