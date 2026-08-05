package service

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"context"
	"encoding/json"
	"testing"
	"time"
)

// C0.6b 服务端草稿。本地 IndexedDB 已把崩溃丢失窗口压到≈0；这一层解决的是
// **换机器续作**。所以测试盯的是"存得回、过期不复活、不越权、超限不静默截断"。

func newDraftSvc(t *testing.T, ttl time.Duration) *DraftService {
	t.Helper()
	return NewDraftService(newMemStore(), ttl)
}

func env(taskID, userID uint, ops string) DraftEnvelope {
	return DraftEnvelope{
		TaskID: taskID, UserID: userID,
		Ops:      json.RawMessage(ops),
		Baseline: json.RawMessage(`{}`),
		OpCount:  3,
	}
}

func TestDraftRoundTrip(t *testing.T) {
	s := newDraftSvc(t, time.Hour)
	ctx := context.Background()
	if err := s.Put(ctx, env(7, 42, `[{"op":"dab"}]`)); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(ctx, 7, 42)
	if err != nil || got == nil {
		t.Fatalf("get: %v, %v", got, err)
	}
	if string(got.Ops) != `[{"op":"dab"}]` || got.OpCount != 3 {
		t.Errorf("取回的内容不对: %+v", got)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt 必须由服务端盖章（客户端时钟不可信，且它决定过期判断）")
	}
}

func TestDraftIsPerUser(t *testing.T) {
	// 编辑锁已经串行化了编辑者，但草稿仍按 (task,user) 隔离：交接时绝不该把
	// 上一个人画到一半的笔画塞给下一个人。
	s := newDraftSvc(t, time.Hour)
	ctx := context.Background()
	if err := s.Put(ctx, env(7, 42, `["A"]`)); err != nil {
		t.Fatal(err)
	}
	other, err := s.Get(ctx, 7, 99)
	if err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Errorf("别的用户不该看到这份草稿: %+v", other)
	}
}

func TestDraftPerTask(t *testing.T) {
	s := newDraftSvc(t, time.Hour)
	ctx := context.Background()
	_ = s.Put(ctx, env(7, 42, `["A"]`))
	if got, _ := s.Get(ctx, 8, 42); got != nil {
		t.Error("别的任务不该看到这份草稿")
	}
}

func TestDraftOverwrite(t *testing.T) {
	s := newDraftSvc(t, time.Hour)
	ctx := context.Background()
	_ = s.Put(ctx, env(7, 42, `["旧"]`))
	_ = s.Put(ctx, env(7, 42, `["新"]`))
	got, _ := s.Get(ctx, 7, 42)
	if string(got.Ops) != `["新"]` {
		t.Errorf("覆盖失败，取回 %s", got.Ops)
	}
}

func TestDraftExpiresAndIsRemoved(t *testing.T) {
	// 几个月前的草稿被当成"未保存的改动"提示出来，只会制造困惑。
	// 过期即视为不存在**并删除**，不留垃圾。
	s := newDraftSvc(t, time.Millisecond)
	ctx := context.Background()
	if err := s.Put(ctx, env(7, 42, `["旧"]`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	got, err := s.Get(ctx, 7, 42)
	if err != nil || got != nil {
		t.Errorf("过期草稿不该返回: %+v", got)
	}
	// 再取一次仍然是空——且底层对象应已被删掉（不留垃圾）。
	if ok, _ := s.store.Exists(ctx, draftKey(7, 42)); ok {
		t.Error("过期草稿应被删除，而不是留在存储里")
	}
}

func TestDraftDeleteAfterSave(t *testing.T) {
	// 保存成功后不清草稿 → 下次进来误报"有未保存改动"。
	// 一个总是狼来了的告警，等真出事时没人会看。
	s := newDraftSvc(t, time.Hour)
	ctx := context.Background()
	_ = s.Put(ctx, env(7, 42, `["A"]`))
	if err := s.Delete(ctx, 7, 42); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, 7, 42); got != nil {
		t.Error("删除后不该还能取到")
	}
	// 幂等：再删一次不报错。
	if err := s.Delete(ctx, 7, 42); err != nil {
		t.Errorf("重复删除应幂等: %v", err)
	}
}

func TestDraftTooLargeIsRejectedNotTruncated(t *testing.T) {
	// **截断的操作日志会回放成一个错误的掩膜**——看着像模像样但不是标注员画的。
	// 所以宁可拒绝，也绝不静默截断。
	s := newDraftSvc(t, time.Hour)
	big := make([]byte, DraftMaxBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	e := env(7, 42, string(mustDraftJSON(t, string(big))))
	err := s.Put(context.Background(), e)
	if err == nil {
		t.Fatal("超限必须报错")
	}
	var tooBig *DraftTooLargeError
	if !asErr(err, &tooBig) {
		t.Fatalf("应返回 DraftTooLargeError，得到 %T", err)
	}
	if got, _ := s.Get(context.Background(), 7, 42); got != nil {
		t.Error("被拒的草稿不该留下半截内容")
	}
}

func TestDraftMissingReturnsNilNotError(t *testing.T) {
	// 读不到草稿绝不能挡住开工——正常打开任务即可，只是没有可恢复的内容。
	s := newDraftSvc(t, time.Hour)
	got, err := s.Get(context.Background(), 123, 456)
	if err != nil || got != nil {
		t.Errorf("不存在的草稿应返回 (nil, nil)，得到 (%v, %v)", got, err)
	}
}

func mustDraftJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asErr[T error](err error, target *T) bool {
	for err != nil {
		if v, ok := err.(T); ok {
			*target = v
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// memStore 是 ObjectStore 的内存实现，只实现草稿用到的方法。
// 用假存储而不是真 MinIO：这里测的是草稿的**语义**（隔离、过期、幂等、拒绝超限），
// 不是驱动的正确性；驱动另有其测试。
//
// ⚠️ **它必须复现 key 与 storageURI 的区别**：PutAt 收裸 key，Get/Delete 收带
// scheme 的 URI。第一版把两者当成同一个东西，于是 DraftService 里"写用 key、
// 读用 key"的真 bug 被完全盖住——单测全绿，真环境里 PUT 返回 saved:true 而
// GET 恒为空。测试替身简化掉的那个区别，恰好就是 bug 藏身的地方。
type memStore struct{ m map[string][]byte }

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

// URIForKey 与真驱动一样加 scheme 前缀；Get/Delete 只认这种形式。
func (s *memStore) URIForKey(key string) string { return "mem://" + key }


func (s *memStore) Put(ctx context.Context, req PutRequest) (PutResult, error) {
	return PutResult{}, errNotImpl
}
func (s *memStore) PutAt(ctx context.Context, key string, body io.Reader, size int64, mime string) (PutResult, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return PutResult{}, err
	}
	s.m[key] = b
	return PutResult{StorageURI: key, Size: int64(len(b))}, nil
}
func (s *memStore) Get(ctx context.Context, uri string) (io.ReadCloser, error) {
	if !strings.HasPrefix(uri, "mem://") {
		return nil, errBareKey // 真驱动也会拒绝裸 key（scheme 解析失败）
	}
	b, ok := s.m[strings.TrimPrefix(uri, "mem://")]
	if !ok {
		return nil, errNotImpl
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (s *memStore) Stat(ctx context.Context, uri string) (StatResult, error) {
	b, ok := s.m[strings.TrimPrefix(uri, "mem://")]
	if !ok {
		return StatResult{}, errNotImpl
	}
	return StatResult{Size: int64(len(b))}, nil
}
func (s *memStore) Delete(ctx context.Context, uri string) error {
	if !strings.HasPrefix(uri, "mem://") {
		return errBareKey
	}
	delete(s.m, strings.TrimPrefix(uri, "mem://"))
	return nil
}
func (s *memStore) Exists(ctx context.Context, uri string) (bool, error) {
	_, ok := s.m[strings.TrimPrefix(uri, "mem://")]
	return ok, nil
}
func (s *memStore) Driver() string { return "mem" }
func (s *memStore) PresignGetURL(ctx context.Context, uri string, d time.Duration) (string, error) {
	return "", nil
}

var (
	errNotImpl = errors.New("mem store: not found / not implemented")
	// 裸 key 必须失败——真驱动 uriToAbs 解析不出 scheme 就报错。
	errBareKey = errors.New("mem store: 需要带 scheme 的 storageURI，收到裸 key")
)
