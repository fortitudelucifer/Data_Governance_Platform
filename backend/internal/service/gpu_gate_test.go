package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGPUGate_SerialisesToConcurrency(t *testing.T) {
	g := NewGPUGate(1, 10)
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if got := g.Stats().InFlight; got != 1 {
		t.Fatalf("inflight = %d, want 1", got)
	}

	// 第二个必须等；给它一个短 ctx 证明它确实被挡住了。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := g.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("并发上限为 1 时第二个应被挡住，got %v", err)
	}

	g.Release()
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatalf("释放后应能立即拿到: %v", err)
	}
	g.Release()
}

// 候诊室满了要立刻拒绝，而不是让 50 个 goroutine 各自挂着 HTTP 请求排队。
func TestGPUGate_RejectsWhenWaitingRoomFull(t *testing.T) {
	g := NewGPUGate(1, 2) // 1 在跑 + 2 在等 = 3 个上限
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer g.Release()

	// 塞满候诊室
	blocked := make(chan error, 2)
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); blocked <- g.Acquire(ctx) }()
	}
	waitUntil(t, func() bool { return g.Stats().Waiting == 2 }, "两个等待者就位")

	// 第三个来的时候候诊室已满 → 立刻拒绝，而不是阻塞
	start := time.Now()
	err := g.Acquire(context.Background())
	if !errors.Is(err, ErrGPUQueueFull) {
		t.Fatalf("队列满时应返回 ErrGPUQueueFull, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("应当立即拒绝而非阻塞，却花了 %v", elapsed)
	}
	if !g.Stats().Full() {
		t.Error("Stats().Full() 应为 true")
	}

	cancel() // 放走两个等待者
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-blocked; !errors.Is(err, context.Canceled) {
			t.Errorf("等待者应因 ctx 取消退出, got %v", err)
		}
	}
	if got := g.Stats().Waiting; got != 0 {
		t.Errorf("等待者退出后 waiting = %d, want 0", got)
	}
}

// 被拒绝的调用不能消耗名额——否则一次拒绝就永久缩小候诊室。
func TestGPUGate_RejectionDoesNotLeakWaitingSlot(t *testing.T) {
	g := NewGPUGate(1, 1)
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer g.Release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = g.Acquire(ctx) }()
	waitUntil(t, func() bool { return g.Stats().Waiting == 1 }, "等待者就位")

	for i := 0; i < 5; i++ { // 反复被拒
		if err := g.Acquire(context.Background()); !errors.Is(err, ErrGPUQueueFull) {
			t.Fatalf("第 %d 次应被拒, got %v", i, err)
		}
	}
	if got := g.Stats().Waiting; got != 1 {
		t.Fatalf("5 次拒绝后 waiting = %d, want 1（拒绝泄漏了名额）", got)
	}
	cancel()
	<-done
}

// maxWait <= 0 = 不限候诊室（保留旧的纯串行语义）。
func TestGPUGate_UnboundedWhenMaxWaitZero(t *testing.T) {
	g := NewGPUGate(1, 0)
	if err := g.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer g.Release()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = g.Acquire(ctx) }()
	}
	waitUntil(t, func() bool { return g.Stats().Waiting == 20 }, "20 个等待者全部入队（无上限）")
	if g.Stats().Full() {
		t.Error("maxWait=0 时 Full() 永远应为 false")
	}
	cancel()
	wg.Wait()
}

// 并发压力下 inflight 绝不能超过 concurrency。
func TestGPUGate_NeverExceedsConcurrency(t *testing.T) {
	const concurrency = 3
	g := NewGPUGate(concurrency, 100)

	var mu sync.Mutex
	cur, peak := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Acquire(context.Background()); err != nil {
				return
			}
			mu.Lock()
			cur++
			if cur > peak {
				peak = cur
			}
			mu.Unlock()

			time.Sleep(time.Millisecond)

			mu.Lock()
			cur--
			mu.Unlock()
			g.Release()
		}()
	}
	wg.Wait()
	if peak > concurrency {
		t.Fatalf("并发峰值 %d 超过上限 %d", peak, concurrency)
	}
	if got := g.Stats().InFlight; got != 0 {
		t.Errorf("全部释放后 inflight = %d, want 0", got)
	}
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("超时等待：%s", what)
}
