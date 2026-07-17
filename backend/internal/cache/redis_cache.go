package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache wraps a Redis client with typed JSON helpers and graceful fallback.
// All methods accept ctx and extract request_id for structured logging.
type Cache struct {
	client *redis.Client
}

// New creates a Cache backed by the given Redis client.
func New(client *redis.Client) *Cache {
	return &Cache{client: client}
}

// GetJSON fetches key and unmarshals the value into dst.
// Returns (false, nil) on cache miss (redis.Nil), (false, err) on other errors.
func (c *Cache) GetJSON(ctx context.Context, key string, dst any) (hit bool, err error) {
	val, err := c.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			c.logMiss(ctx, key)
			return false, nil
		}
		c.logDegraded(ctx, key, err)
		return false, nil // degrade to DB on Redis error
	}
	if jsonErr := json.Unmarshal([]byte(val), dst); jsonErr != nil {
		slog.WarnContext(ctx, "[cache] unmarshal error, treating as miss",
			"key", key, "error", jsonErr,
			"request_id", requestID(ctx))
		return false, nil
	}
	c.logHit(ctx, key)
	return true, nil
}

// SetJSON serializes src to JSON and stores it under key with the given TTL.
// Errors are logged and swallowed — cache writes are best-effort.
func (c *Cache) SetJSON(ctx context.Context, key string, src any, ttl time.Duration) {
	b, err := json.Marshal(src)
	if err != nil {
		slog.WarnContext(ctx, "[cache] marshal error, skipping set",
			"key", key, "error", err, "request_id", requestID(ctx))
		return
	}
	if err := c.client.SetEx(ctx, key, b, ttl).Err(); err != nil {
		c.logDegraded(ctx, key, err)
	}
}

// SetJSONPersist serializes src to JSON and stores it without any expiry
// (永不过期). Use only for truly immutable content-addressed data.
func (c *Cache) SetJSONPersist(ctx context.Context, key string, src any) {
	b, err := json.Marshal(src)
	if err != nil {
		slog.WarnContext(ctx, "[cache] marshal error, skipping persist",
			"key", key, "error", err, "request_id", requestID(ctx))
		return
	}
	// go-redis: Set with 0 duration = no expiry
	if err := c.client.Set(ctx, key, b, 0).Err(); err != nil {
		c.logDegraded(ctx, key, err)
	}
}

// Delete removes an exact key. Errors are logged and swallowed.
func (c *Cache) Delete(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		slog.WarnContext(ctx, "[cache] delete error",
			"keys", keys, "error", err, "request_id", requestID(ctx))
	}
}

// ScanDelete removes all keys matching the given pattern (e.g. "datasets:page:*").
// Uses SCAN + pipeline DEL; never calls KEYS.
func (c *Cache) ScanDelete(ctx context.Context, pattern string) {
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			slog.WarnContext(ctx, "[cache] scan error",
				"pattern", pattern, "error", err, "request_id", requestID(ctx))
			return
		}
		if len(keys) > 0 {
			pipe := c.client.Pipeline()
			for _, k := range keys {
				pipe.Del(ctx, k)
			}
			if _, err := pipe.Exec(ctx); err != nil {
				slog.WarnContext(ctx, "[cache] pipeline del error",
					"pattern", pattern, "error", err, "request_id", requestID(ctx))
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
}

// Exists returns true if key exists in Redis.
func (c *Cache) Exists(ctx context.Context, key string) bool {
	n, err := c.client.Exists(ctx, key).Result()
	if err != nil {
		c.logDegraded(ctx, key, err)
		return false
	}
	return n > 0
}

// Ping checks Redis connectivity (used by /health).
func (c *Cache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

// Close closes the underlying Redis client.
func (c *Cache) Close() error {
	return c.client.Close()
}

// requestID extracts the request_id from ctx.
// Gin's c.Set stores values under string keys, accessible via ctx.Value(string).
func requestID(ctx context.Context) string {
	if v := ctx.Value("request_id"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func (c *Cache) logHit(ctx context.Context, key string) {
	slog.DebugContext(ctx, "[cache] hit", "key", key, "request_id", requestID(ctx))
}

func (c *Cache) logMiss(ctx context.Context, key string) {
	slog.DebugContext(ctx, "[cache] miss", "key", key, "request_id", requestID(ctx))
}

func (c *Cache) logDegraded(ctx context.Context, key string, err error) {
	slog.WarnContext(ctx, "[cache] degraded to DB",
		"key", key, "error", err, "request_id", requestID(ctx))
}
