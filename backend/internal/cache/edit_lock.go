package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Distributed edit-lock primitives (plan_v2 执行方案-00 T0.4). Implemented with
// SETNX + owner-checked Lua release/refresh on the existing go-redis client —
// equivalent to redsync for a single-Redis deployment without a new dependency.

// refreshScript extends the TTL only if the caller still owns the lock.
const refreshScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
  return 0
end`

// releaseScript deletes the lock only if the caller still owns it (prevents
// releasing a lock that has already expired and been re-acquired by someone else).
const releaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end`

// TryLock attempts to acquire key for owner with ttl. On success returns
// (true, owner). If already held, returns (false, currentOwner). currentOwner
// is best-effort (may be "" if the lock expired during the race).
func (c *Cache) TryLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, string, error) {
	ok, err := c.client.SetNX(ctx, key, owner, ttl).Result()
	if err != nil {
		return false, "", err
	}
	if ok {
		return true, owner, nil
	}
	cur, gerr := c.client.Get(ctx, key).Result()
	if errors.Is(gerr, redis.Nil) {
		// Lock expired between SETNX and GET — try once more.
		if ok2, err2 := c.client.SetNX(ctx, key, owner, ttl).Result(); err2 == nil && ok2 {
			return true, owner, nil
		}
		cur, _ = c.client.Get(ctx, key).Result()
		return false, cur, nil
	}
	if gerr != nil {
		return false, "", gerr
	}
	return false, cur, nil
}

// RefreshLock extends ttl iff owner still holds key. Returns true if refreshed.
func (c *Cache) RefreshLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	res, err := c.client.Eval(ctx, refreshScript, []string{key}, owner, ttl.Milliseconds()).Result()
	if err != nil {
		return false, err
	}
	n, _ := res.(int64)
	return n == 1, nil
}

// Unlock releases key iff owner still holds it. Returns true if released.
func (c *Cache) Unlock(ctx context.Context, key, owner string) (bool, error) {
	res, err := c.client.Eval(ctx, releaseScript, []string{key}, owner).Result()
	if err != nil {
		return false, err
	}
	n, _ := res.(int64)
	return n == 1, nil
}
