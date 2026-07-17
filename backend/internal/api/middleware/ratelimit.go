package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"

	redis_rate "github.com/go-redis/redis_rate/v10"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// globalRedisLimiter is set once at startup by InitRedisLimiter.
// nil means no Redis — all requests fall through to the in-memory limiter.
var (
	globalRedisLimiter *redis_rate.Limiter
	globalRedisOnce    sync.Once
)

// InitRedisLimiter wires the Redis client into the rate-limit middleware.
// Must be called before the first request; safe to call with nil (no-op).
func InitRedisLimiter(c *goredis.Client) {
	if c == nil {
		return
	}
	globalRedisOnce.Do(func() {
		globalRedisLimiter = redis_rate.NewLimiter(c)
	})
}

// fallbackStore is a simple unbounded sync.Map used only when Redis is
// unavailable. No LRU eviction — this path is a short-lived failsafe, not
// a long-running replacement. OOM risk under sustained Redis outage is
// accepted as the tradeoff for simplicity.
type fallbackStore struct {
	mu    sync.Mutex
	items map[string]*rate.Limiter
	newFn func() *rate.Limiter
}

func newFallbackStore(r rate.Limit, burst int) *fallbackStore {
	return &fallbackStore{
		items: make(map[string]*rate.Limiter),
		newFn: func() *rate.Limiter { return rate.NewLimiter(r, burst) },
	}
}

func (s *fallbackStore) allow(key string) bool {
	s.mu.Lock()
	lim, ok := s.items[key]
	if !ok {
		lim = s.newFn()
		s.items[key] = lim
	}
	s.mu.Unlock()
	return lim.Allow()
}

// toRedisLimit converts golang.org/x/time/rate parameters to redis_rate.Limit.
// rate.Limit is tokens-per-second (float64); we round to the nearest integer
// and use a per-second window so the semantics stay equivalent.
func toRedisLimit(r rate.Limit, burst int) redis_rate.Limit {
	rps := int(r + 0.5) // round to nearest int; minimum 1
	if rps < 1 {
		rps = 1
	}
	return redis_rate.Limit{
		Rate:   rps,
		Burst:  burst,
		Period: time.Second,
	}
}

// tryRedis attempts a Redis-backed Allow check.
// Returns (allowed, ok): ok=false means Redis was unreachable → caller falls back.
func tryRedis(key string, lim redis_rate.Limit) (allowed, ok bool) {
	if globalRedisLimiter == nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := globalRedisLimiter.Allow(ctx, key, lim)
	if err != nil {
		return false, false // Redis error → fall back to in-memory
	}
	return res.Allowed > 0, true
}

// IPRateLimit limits requests by client IP address.
// Uses Redis sliding-window when available; falls back to in-memory token bucket.
func IPRateLimit(r rate.Limit, burst int) gin.HandlerFunc {
	rLim := toRedisLimit(r, burst)
	fb := newFallbackStore(r, burst)
	return func(c *gin.Context) {
		key := "ratelimit:ip:" + c.ClientIP()
		allowed, ok := tryRedis(key, rLim)
		if !ok {
			allowed = fb.allow(key)
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后重试"})
			return
		}
		c.Next()
	}
}

// UserRateLimit limits requests by authenticated user ID.
// Falls back to client IP when no UserContext is present.
// Uses Redis sliding-window when available; falls back to in-memory token bucket.
func UserRateLimit(r rate.Limit, burst int) gin.HandlerFunc {
	rLim := toRedisLimit(r, burst)
	fb := newFallbackStore(r, burst)
	return func(c *gin.Context) {
		var key string
		if uc := GetUserContext(c); uc != nil {
			key = "ratelimit:user:" + uintToStr(uc.UserID)
		} else {
			key = "ratelimit:ip:" + c.ClientIP()
		}
		allowed, ok := tryRedis(key, rLim)
		if !ok {
			allowed = fb.allow(key)
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "请求过于频繁，请稍后重试"})
			return
		}
		c.Next()
	}
}

// uintToStr converts a uint to its decimal string representation.
func uintToStr(n uint) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
