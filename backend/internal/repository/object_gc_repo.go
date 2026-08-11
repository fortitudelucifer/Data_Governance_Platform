package repository

import (
	"context"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// object_gc_repo.go — durable object-store GC outbox (migration 000005).
//
// The point of the outbox is atomicity: EnqueueObjectGCTx runs inside the caller's
// transaction, so "relational rows deleted" and "these blobs are now garbage" commit
// together. A crash between the two can't leave an orphaned blob with no record.

// EnqueueObjectGCTx queues object cleanups **within the caller's transaction**.
// Pass the *gorm.DB of the surrounding tx so the enqueue is atomic with the
// relational deletes. no-op on empty input.
func (r *DB) EnqueueObjectGCTx(ctx context.Context, tx *gorm.DB, items []dbmodel.ObjectGC) error {
	if len(items) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Create(&items).Error
}

// EnqueueObjectGC queues cleanups **outside** any transaction — for error-path
// compensation where an object was already written but a later DB insert failed,
// leaving an orphan blob (#15). Best-effort: if this enqueue itself fails (DB
// truly down) the orphan persists; but the common case is an insert that hit a
// constraint / transient while the DB is up, so the row lands and the janitor
// reclaims the blob.
func (r *DB) EnqueueObjectGC(ctx context.Context, items []dbmodel.ObjectGC) error {
	if len(items) == 0 {
		return nil
	}
	return r.DB.WithContext(ctx).Create(&items).Error
}

// ClaimDueObjectGC atomically claims up to limit due rows (next_attempt_at <= now),
// parking their next_attempt_at forward by the lease so a second janitor won't
// double-process, and bumping attempts. FOR UPDATE SKIP LOCKED keeps concurrent
// janitors non-blocking.
func (r *DB) ClaimDueObjectGC(ctx context.Context, leaseUntil time.Time, limit int) ([]dbmodel.ObjectGC, error) {
	if limit <= 0 {
		return nil, nil
	}
	now := time.Now()
	var rows []dbmodel.ObjectGC
	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Model(&dbmodel.ObjectGC{}).
			Select("id").
			Where("next_attempt_at <= ?", now).
			Order("next_attempt_at asc").
			Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Model(&dbmodel.ObjectGC{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"next_attempt_at": leaseUntil,
				"attempts":        gorm.Expr("attempts + 1"),
			}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Order("id asc").Find(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ResolveObjectGC removes a queue row once its object is confirmed gone.
func (r *DB) ResolveObjectGC(ctx context.Context, id uint) error {
	return r.DB.WithContext(ctx).Delete(&dbmodel.ObjectGC{}, id).Error
}

// RescheduleObjectGC records the error and parks the row for a later retry
// (backoff). The claim already advanced next_attempt_at as a lease; this sets the
// real backoff.
func (r *DB) RescheduleObjectGC(ctx context.Context, id uint, errMsg string, nextAt time.Time) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.ObjectGC{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"next_attempt_at": nextAt,
			"last_error":      errMsg,
		}).Error
}

// CountObjectGC returns the queue depth (observability / tests).
func (r *DB) CountObjectGC(ctx context.Context) (int64, error) {
	var n int64
	err := r.DB.WithContext(ctx).Model(&dbmodel.ObjectGC{}).Count(&n).Error
	return n, err
}
