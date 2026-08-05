package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// These tests exercise AIWorker's Stop semantics directly via the embedded
// WaitGroup, without spinning up the real tick loop (which requires a fully
// wired relational-DB + payload-store + RouterService stack). The graceful-shutdown contract is
// captured entirely in Stop's wait-vs-cancel race, which is what we verify.

// newTestAIWorker returns a stripped-down AIWorker suitable for Stop testing.
// The stopCh is initialised; the loop is never started so there is no leak.
func newTestAIWorker() *AIWorker {
	return &AIWorker{
		stopCh: make(chan struct{}),
	}
}

// TestAIWorker_Stop_WaitsForInFlightTask simulates a 200ms in-flight task by
// adding to the WaitGroup and releasing it after 200ms. Stop with a generous
// 30s ctx must return shortly after the task completes (well under 30s).
func TestAIWorker_Stop_WaitsForInFlightTask(t *testing.T) {
	w := newTestAIWorker()

	// Simulate a task running for 200ms.
	w.wg.Add(1)
	taskDuration := 200 * time.Millisecond
	go func() {
		time.Sleep(taskDuration)
		w.wg.Done()
	}()

	// Stop with a generous 30s grace deadline.
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err := w.Stop(stopCtx)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Stop returned %v, want nil for clean drain", err)
	}
	// Should return ~taskDuration, not waiting for the 30s ctx timeout.
	if elapsed > 3*time.Second {
		t.Errorf("Stop took %v; should have returned shortly after task finished (~200ms)", elapsed)
	}
	if elapsed < taskDuration {
		t.Errorf("Stop returned in %v, before task completed (%v)", elapsed, taskDuration)
	}
}

// TestAIWorker_Stop_ContextDeadlineExceeded simulates a task that never
// completes. Stop with a 100ms ctx must return ctx.Err() after ~100ms rather
// than blocking forever.
func TestAIWorker_Stop_ContextDeadlineExceeded(t *testing.T) {
	w := newTestAIWorker()

	// Simulate an in-flight task that never finishes. We release it at the
	// end of the test so the WG doesn't leak into the next test.
	w.wg.Add(1)
	release := make(chan struct{})
	go func() {
		<-release
		w.wg.Done()
	}()
	defer close(release)

	stopCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := w.Stop(stopCtx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop returned %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Stop took %v; should have respected the 100ms deadline", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("Stop returned in %v, before deadline elapsed", elapsed)
	}
}

// TestAIWorker_Stop_NoInFlightWork verifies that Stop returns immediately when
// no work is in flight and the loop has already exited.
func TestAIWorker_Stop_NoInFlightWork(t *testing.T) {
	w := newTestAIWorker()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := w.Stop(stopCtx)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Stop returned %v, want nil", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Stop took %v; should have returned immediately", elapsed)
	}
}

// TestAIWorker_Stop_Idempotent verifies that calling Stop multiple times is
// safe (the close-once semantic via sync.Once must not panic).
func TestAIWorker_Stop_Idempotent(t *testing.T) {
	w := newTestAIWorker()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Stop(stopCtx)
		}()
	}
	wg.Wait() // Must not panic.
}

// TestAIWorker_Stop_SignatureMatchesSpec is a compile-time check that the
// signature TD-26 required (`func (w *AIWorker) Stop(ctx context.Context) error`)
// is preserved. If someone changes the signature, this test will not compile.
func TestAIWorker_Stop_SignatureMatchesSpec(t *testing.T) {
	w := newTestAIWorker()
	var stop func(context.Context) error = w.Stop
	if stop == nil {
		t.Fatal("Stop must be assignable to func(context.Context) error")
	}
}
