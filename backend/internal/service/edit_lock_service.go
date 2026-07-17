package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"text-annotation-platform/internal/cache"
)

// editLockTTL is the lock lifetime. The frontend refreshes (watchdog) well
// before this elapses; if the tab dies, the lock auto-expires after this.
const editLockTTL = 90 * time.Second

func taskLockKey(taskID uint) string { return fmt.Sprintf("task-lock:%d", taskID) }

// lockStore is the subset of cache.Cache the edit lock needs. An interface so
// the service is unit-testable without a live Redis.
type lockStore interface {
	TryLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, string, error)
	RefreshLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, key, owner string) (bool, error)
}

// EditLockService guards a task against concurrent edits across app instances
// (plan_v2 执行方案-00 T0.4). When no store is wired (Redis absent, e.g. the
// standalone desktop runner) it degrades to "always acquired" — safe for a single
// instance.
type EditLockService struct {
	store lockStore
	ttl   time.Duration
}

// NewEditLockService builds the service from the Redis cache. A nil cache
// (Redis absent) degrades to "always acquired" — safe for a single instance.
// The nil pointer is converted to a nil interface to avoid the typed-nil trap.
func NewEditLockService(c *cache.Cache) *EditLockService {
	var store lockStore
	if c != nil {
		store = c
	}
	return &EditLockService{store: store, ttl: editLockTTL}
}

// LockResult is the wire shape returned to the workspace.
type LockResult struct {
	Acquired bool   `json:"acquired"`        // can the requester edit?
	Owner    string `json:"owner"`           // current holder's user id
	Self     bool   `json:"self"`            // requester is the holder
	TTLSec   int    `json:"ttl_sec"`         // lock lifetime
}

func (s *EditLockService) ttlSec() int { return int(s.ttl.Seconds()) }

// Acquire locks the task for owner. Re-entrant: if owner already holds it, the
// lock is refreshed. If someone else holds it, returns Acquired=false + holder.
func (s *EditLockService) Acquire(ctx context.Context, taskID uint, owner string) (LockResult, error) {
	if s.store == nil {
		return LockResult{Acquired: true, Owner: owner, Self: true, TTLSec: s.ttlSec()}, nil
	}
	key := taskLockKey(taskID)
	ok, cur, err := s.store.TryLock(ctx, key, owner, s.ttl)
	if err != nil {
		return LockResult{}, err
	}
	if ok {
		return LockResult{Acquired: true, Owner: owner, Self: true, TTLSec: s.ttlSec()}, nil
	}
	if cur == owner {
		_, _ = s.store.RefreshLock(ctx, key, owner, s.ttl)
		return LockResult{Acquired: true, Owner: owner, Self: true, TTLSec: s.ttlSec()}, nil
	}
	return LockResult{Acquired: false, Owner: cur, Self: false, TTLSec: s.ttlSec()}, nil
}

// Refresh extends the lock iff owner still holds it (heartbeat).
func (s *EditLockService) Refresh(ctx context.Context, taskID uint, owner string) (bool, error) {
	if s.store == nil {
		return true, nil
	}
	return s.store.RefreshLock(ctx, taskLockKey(taskID), owner, s.ttl)
}

// Release frees the lock iff owner holds it.
func (s *EditLockService) Release(ctx context.Context, taskID uint, owner string) (bool, error) {
	if s.store == nil {
		return true, nil
	}
	return s.store.Unlock(ctx, taskLockKey(taskID), owner)
}

// ownerID renders a user id as the lock owner value.
func ownerID(userID uint) string { return strconv.FormatUint(uint64(userID), 10) }
