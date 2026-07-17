package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"text-annotation-platform/internal/logger"

	"github.com/gin-gonic/gin"
)

func setupRequestIDRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	r.GET("/ping", func(c *gin.Context) {
		rid := c.GetString(RequestIDKey)
		c.JSON(http.StatusOK, gin.H{"request_id": rid})
	})
	return r
}

func TestRequestIDMiddleware_InjectsHeader(t *testing.T) {
	r := setupRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	rid := w.Header().Get("X-Request-Id")
	if rid == "" {
		t.Error("expected X-Request-Id response header to be set")
	}
}

func TestRequestIDMiddleware_HeaderIs16HexChars(t *testing.T) {
	r := setupRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	rid := w.Header().Get("X-Request-Id")
	if len(rid) != 16 {
		t.Errorf("expected 16-char hex request id, got %q (len %d)", rid, len(rid))
	}
	for _, ch := range rid {
		if !('0' <= ch && ch <= '9') && !('a' <= ch && ch <= 'f') {
			t.Errorf("expected lowercase hex chars in request id, got %q", rid)
			break
		}
	}
}

func TestRequestIDMiddleware_UniquePerRequest(t *testing.T) {
	r := setupRequestIDRouter()
	rid1 := func() string {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Header().Get("X-Request-Id")
	}()
	rid2 := func() string {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Header().Get("X-Request-Id")
	}()
	if rid1 == rid2 {
		t.Errorf("expected unique request IDs per request, both were %q", rid1)
	}
}

func TestRequestIDMiddleware_PassthroughClientSuppliedID(t *testing.T) {
	r := setupRequestIDRouter()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-Id", "client-supplied-id")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	rid := w.Header().Get("X-Request-Id")
	if rid != "client-supplied-id" {
		t.Errorf("expected client-supplied ID to be echoed, got %q", rid)
	}
}

func TestRequestIDMiddleware_InjectsIntoContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestIDMiddleware())
	var ctxRID string
	r.GET("/ctx", func(c *gin.Context) {
		ctxRID = logger.RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ctx", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if ctxRID == "" {
		t.Error("expected request_id to be stored in request context")
	}
	headerRID := w.Header().Get("X-Request-Id")
	if ctxRID != headerRID {
		t.Errorf("context request_id %q does not match response header %q", ctxRID, headerRID)
	}
}
