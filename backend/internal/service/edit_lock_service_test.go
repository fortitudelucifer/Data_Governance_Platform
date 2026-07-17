package service

import (
	"context"
	"testing"
	"time"
)

// fakeLockStore is an in-memory lockStore for unit-testing the edit-lock logic
// without a live Redis (ttl is ignored; expiry isn't exercised here).
type fakeLockStore struct {
	locks map[string]string // key -> owner
}

func newFakeLockStore() *fakeLockStore { return &fakeLockStore{locks: map[string]string{}} }

func (f *fakeLockStore) TryLock(_ context.Context, key, owner string, _ time.Duration) (bool, string, error) {
	if cur, ok := f.locks[key]; ok {
		return false, cur, nil
	}
	f.locks[key] = owner
	return true, owner, nil
}

func (f *fakeLockStore) RefreshLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	if f.locks[key] == owner {
		return true, nil
	}
	return false, nil
}

func (f *fakeLockStore) Unlock(_ context.Context, key, owner string) (bool, error) {
	if f.locks[key] == owner {
		delete(f.locks, key)
		return true, nil
	}
	return false, nil
}

func newLockSvc(store lockStore) *EditLockService {
	return &EditLockService{store: store, ttl: editLockTTL}
}

func TestEditLock_AcquireContendRelease(t *testing.T) {
	ctx := context.Background()
	svc := newLockSvc(newFakeLockStore())

	// u1 acquires.
	r, err := svc.Acquire(ctx, 7, "u1")
	if err != nil {
		t.Fatalf("acquire u1: %v", err)
	}
	if !r.Acquired || !r.Self || r.Owner != "u1" {
		t.Fatalf("u1 first acquire = %+v", r)
	}

	// u2 is blocked, sees u1 as holder.
	r, _ = svc.Acquire(ctx, 7, "u2")
	if r.Acquired || r.Self || r.Owner != "u1" {
		t.Fatalf("u2 contend = %+v, want blocked with owner u1", r)
	}

	// u1 re-acquires (re-entrant).
	r, _ = svc.Acquire(ctx, 7, "u1")
	if !r.Acquired || !r.Self {
		t.Fatalf("u1 re-acquire = %+v", r)
	}

	// Refresh: owner ok, other fails.
	if ok, _ := svc.Refresh(ctx, 7, "u1"); !ok {
		t.Fatalf("u1 refresh should succeed")
	}
	if ok, _ := svc.Refresh(ctx, 7, "u2"); ok {
		t.Fatalf("u2 refresh should fail")
	}

	// Release by non-owner is a no-op; by owner frees it.
	if ok, _ := svc.Release(ctx, 7, "u2"); ok {
		t.Fatalf("u2 release should be no-op")
	}
	if ok, _ := svc.Release(ctx, 7, "u1"); !ok {
		t.Fatalf("u1 release should succeed")
	}

	// Now u2 can acquire.
	r, _ = svc.Acquire(ctx, 7, "u2")
	if !r.Acquired || r.Owner != "u2" {
		t.Fatalf("u2 acquire after release = %+v", r)
	}
}

// Degrade path: no store (Redis absent) → always acquired (single instance).
func TestEditLock_DegradesWithoutStore(t *testing.T) {
	ctx := context.Background()
	svc := NewEditLockService(nil)
	r, err := svc.Acquire(ctx, 1, "u1")
	if err != nil || !r.Acquired || !r.Self {
		t.Fatalf("degrade acquire = %+v err=%v", r, err)
	}
	if ok, _ := svc.Refresh(ctx, 1, "u1"); !ok {
		t.Fatalf("degrade refresh should succeed")
	}
	if ok, _ := svc.Release(ctx, 1, "u1"); !ok {
		t.Fatalf("degrade release should succeed")
	}
}
