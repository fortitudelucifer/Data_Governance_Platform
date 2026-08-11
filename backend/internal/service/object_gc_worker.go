package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// ObjectGCWorker drains the durable object-GC outbox (migration 000005): it claims
// due cleanups and runs Delete / DeletePrefix against the object store, retrying
// with backoff until the blob is gone. This is the reliable-compensation backbone
// for the delete paths (包6 #15/#17/#19) — a failed object delete becomes a queued
// retry, not a silent (possibly-PHI) orphan.
type ObjectGCWorker struct {
	cfg    ObjectGCConfig
	db     *repository.DB
	store  ObjectStore
	stopCh chan struct{}
	once   sync.Once
	wg     sync.WaitGroup
}

// ObjectGCConfig tunes the janitor.
type ObjectGCConfig struct {
	Interval   time.Duration // poll cadence
	Batch      int           // rows claimed per tick
	LeaseTTL   time.Duration // how long a claimed row is parked before another janitor may re-take it
	MaxBackoff time.Duration // cap on per-row retry backoff
}

// DefaultObjectGCConfig returns sane defaults.
func DefaultObjectGCConfig() ObjectGCConfig {
	return ObjectGCConfig{Interval: 30 * time.Second, Batch: 50, LeaseTTL: 5 * time.Minute, MaxBackoff: 30 * time.Minute}
}

// NewObjectGCWorker wires the janitor. A nil store disables it (Start is a no-op).
func NewObjectGCWorker(cfg ObjectGCConfig, db *repository.DB, store ObjectStore) *ObjectGCWorker {
	if cfg.Interval <= 0 {
		cfg = DefaultObjectGCConfig()
	}
	if cfg.Batch <= 0 {
		cfg.Batch = DefaultObjectGCConfig().Batch
	}
	return &ObjectGCWorker{cfg: cfg, db: db, store: store, stopCh: make(chan struct{})}
}

// Start launches the drain loop.
func (w *ObjectGCWorker) Start(ctx context.Context) {
	if w.store == nil {
		return
	}
	w.wg.Add(1)
	go w.loop(ctx)
}

// Stop signals shutdown and waits for the loop to drain (or ctx).
func (w *ObjectGCWorker) Stop(ctx context.Context) error {
	w.once.Do(func() { close(w.stopCh) })
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ObjectGCWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce claims + processes one batch synchronously (loop body; also used by tests).
func (w *ObjectGCWorker) RunOnce(ctx context.Context) {
	rows, err := w.db.ClaimDueObjectGC(ctx, time.Now().Add(w.cfg.LeaseTTL), w.cfg.Batch)
	if err != nil {
		slog.Error("object_gc: claim", "error", err)
		return
	}
	for i := range rows {
		w.process(ctx, &rows[i])
	}
}

func (w *ObjectGCWorker) process(ctx context.Context, row *dbmodel.ObjectGC) {
	var derr error
	if row.IsPrefix {
		derr = w.store.DeletePrefix(ctx, row.StorageURI)
	} else {
		derr = w.store.Delete(ctx, row.StorageURI)
	}
	if derr == nil {
		if err := w.db.ResolveObjectGC(ctx, row.ID); err != nil {
			slog.Error("object_gc: resolve after delete", "id", row.ID, "error", err)
		}
		return
	}
	// Linear-ish backoff by attempt count, capped. Keeps a flapping store from
	// being hammered while still retrying to completion.
	backoff := time.Duration(row.Attempts) * w.cfg.Interval
	if backoff < w.cfg.Interval {
		backoff = w.cfg.Interval
	}
	if backoff > w.cfg.MaxBackoff {
		backoff = w.cfg.MaxBackoff
	}
	_ = w.db.RescheduleObjectGC(ctx, row.ID, derr.Error(), time.Now().Add(backoff))
	slog.Warn("object_gc: delete failed, rescheduled", "id", row.ID, "uri", row.StorageURI, "attempts", row.Attempts, "error", derr)
}
