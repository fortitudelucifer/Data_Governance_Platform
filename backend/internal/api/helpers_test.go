package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// singleRoute mounts one handler on a fresh Gin engine and returns it.
// Shared across all api handler test files.
func singleRoute(method, path string, h gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Handle(method, path, h)
	return r
}

// do sends a JSON-encoded request body (or empty if body is nil).
func do(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// doRaw sends a raw string body. Defined here as the canonical helper —
// older test files (auto_annotate_handler_test.go) previously declared
// a local copy that has been removed in favor of this one.
func doRaw(r interface{ ServeHTTP(http.ResponseWriter, *http.Request) }, method, path, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
