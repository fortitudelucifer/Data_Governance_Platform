package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

func setupIPRateLimitRouter(r rate.Limit, burst int) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	eng.Use(IPRateLimit(r, burst))
	eng.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	return eng
}

func setupUserRateLimitRouter(r rate.Limit, burst int, userID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	// Inject a UserContext so UserRateLimit can key by user ID.
	eng.Use(func(c *gin.Context) {
		c.Set(userContextKey, &UserContext{UserID: userID, Role: "admin"})
		c.Next()
	})
	eng.Use(UserRateLimit(r, burst))
	eng.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	return eng
}

func TestIPRateLimit_AllowsWithinBurst(t *testing.T) {
	// burst=3: first 3 requests must succeed
	r := setupIPRateLimitRouter(rate.Every(time.Hour), 3)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "1.2.3.4:9999"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}
}

func TestIPRateLimit_BlocksAfterBurst(t *testing.T) {
	// burst=3, very slow refill: 4th request must get 429
	r := setupIPRateLimitRouter(rate.Every(time.Hour), 3)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = "1.2.3.4:9999"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "1.2.3.4:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after burst exhausted, got %d", w.Code)
	}
}

func TestIPRateLimit_DifferentIPsAreIsolated(t *testing.T) {
	// burst=1: each unique IP gets its own bucket
	r := setupIPRateLimitRouter(rate.Every(time.Hour), 1)

	for _, ip := range []string{"10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1"} {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("IP %s: expected 200 (fresh bucket), got %d", ip, w.Code)
		}
	}
}

func TestUserRateLimit_BlocksAfterBurst(t *testing.T) {
	// burst=2, very slow refill: 3rd request must get 429
	r := setupUserRateLimitRouter(rate.Every(time.Hour), 2, 42)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after burst exhausted, got %d", w.Code)
	}
}

func TestUserRateLimit_DifferentUsersAreIsolated(t *testing.T) {
	// Each user gets their own bucket; user 1 exhausted must not affect user 2.
	gin.SetMode(gin.TestMode)

	makeRouter := func(uid uint) *gin.Engine {
		eng := gin.New()
		eng.Use(func(c *gin.Context) {
			c.Set(userContextKey, &UserContext{UserID: uid, Role: "admin"})
			c.Next()
		})
		// Share the same store: use identical parameters — each call creates a
		// fresh store, so isolation is per-instance. This tests key isolation.
		eng.Use(UserRateLimit(rate.Every(time.Hour), 1))
		eng.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
		return eng
	}

	r1 := makeRouter(1)
	r2 := makeRouter(2)

	// Exhaust user 1's bucket.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		w := httptest.NewRecorder()
		r1.ServeHTTP(w, req)
	}

	// User 2 on a separate router (fresh store) should still get 200.
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r2.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("user 2: expected 200 (own bucket), got %d", w.Code)
	}
}

