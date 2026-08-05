package service

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrGPUQueueFull is returned when the GPU gate's waiting room is full. Callers
// should surface it as 429 with a retry hint rather than queueing forever.
var ErrGPUQueueFull = errors.New("GPU 推理队列已满，请稍后再试")

// GPUGate bounds concurrent + queued GPU work (执行方案-02 B2.8 «成本闸门»).
//
// detect_track is a GPU-heavy job and the det-server tracker holds global state,
// so only one video may run at a time. A plain mutex gives that serialisation
// but an *unbounded* waiting room: a batch upload of 50 videos parks 50
// goroutines, each holding an HTTP request and a context deadline, and the last
// one waits ~50× the job time before it even starts — by which point its
// deadline has long expired. It also means the 103 box has no way to shed load.
//
// GPUGate keeps the serialisation and adds a bounded waiting room: past the
// limit, callers are rejected immediately with ErrGPUQueueFull instead of
// silently piling up. Backlog is observable via Stats so the workbench can show
// "队列 2/4" and disable the button before the user clicks into a rejection.
type GPUGate struct {
	slots   chan struct{} // capacity = concurrency
	waiting atomic.Int64  // callers currently blocked in Acquire
	maxWait int64         // waiting-room capacity; ≤0 disables the bound
}

// NewGPUGate builds a gate allowing `concurrency` jobs in flight and at most
// `maxWait` callers queued behind them. concurrency ≤ 0 defaults to 1 (the
// det-server tracker is stateful and cannot run two videos at once).
func NewGPUGate(concurrency, maxWait int) *GPUGate {
	if concurrency <= 0 {
		concurrency = 1
	}
	return &GPUGate{slots: make(chan struct{}, concurrency), maxWait: int64(maxWait)}
}

// Acquire takes a slot, blocking while others run. Returns ErrGPUQueueFull
// immediately when the waiting room is full, or ctx.Err() if the caller gives
// up first. Every nil return must be paired with a Release.
func (g *GPUGate) Acquire(ctx context.Context) error {
	// Fast path: a slot is free right now, no need to enter the waiting room.
	select {
	case g.slots <- struct{}{}:
		return nil
	default:
	}

	// Claim a spot in the waiting room *before* blocking: the bound must be
	// enforced against concurrent arrivals, not checked-then-raced.
	if n := g.waiting.Add(1); g.maxWait > 0 && n > g.maxWait {
		g.waiting.Add(-1)
		return ErrGPUQueueFull
	}
	defer g.waiting.Add(-1)

	select {
	case g.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a slot. Safe to call only after a successful Acquire.
func (g *GPUGate) Release() {
	select {
	case <-g.slots:
	default: // unbalanced Release — ignore rather than block a request path
	}
}

// GPUQueueStats is the backlog snapshot shown in the workbench.
type GPUQueueStats struct {
	InFlight    int `json:"inflight"`    // jobs currently running
	Waiting     int `json:"waiting"`     // callers blocked waiting for a slot
	Concurrency int `json:"concurrency"` // max jobs in flight
	MaxWait     int `json:"max_wait"`    // waiting-room capacity (0 = unbounded)
}

// Full reports whether a new Acquire would be rejected outright.
func (s GPUQueueStats) Full() bool { return s.MaxWait > 0 && s.Waiting >= s.MaxWait }

// Stats snapshots the gate. The numbers are sampled independently, so they can
// disagree by one under load — fine for a progress indicator.
func (g *GPUGate) Stats() GPUQueueStats {
	return GPUQueueStats{
		InFlight:    len(g.slots),
		Waiting:     int(g.waiting.Load()),
		Concurrency: cap(g.slots),
		MaxWait:     int(g.maxWait),
	}
}
