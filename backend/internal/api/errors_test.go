package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestError_ShapeAndStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		Error(c, http.StatusBadRequest, "bad input")
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %s", w.Body.String())
	}
	if code, _ := body["code"].(float64); int(code) != http.StatusBadRequest {
		t.Errorf("code: want 400, got %v", body["code"])
	}
	if msg, _ := body["message"].(string); msg != "bad input" {
		t.Errorf("message: want %q, got %v", "bad input", body["message"])
	}
	// Backward-compat: the legacy "error" key must still carry the message.
	if e, _ := body["error"].(string); e != "bad input" {
		t.Errorf("error alias: want %q, got %v", "bad input", body["error"])
	}
}

func TestErrorWithExtras_PreservesStandardFieldsAndMergesExtras(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		ErrorWithExtras(c, http.StatusServiceUnavailable, "LLM down", gin.H{"degraded": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: want 503, got %d", w.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if int(body["code"].(float64)) != 503 {
		t.Errorf("code: want 503, got %v", body["code"])
	}
	if body["message"] != "LLM down" {
		t.Errorf("message: want %q, got %v", "LLM down", body["message"])
	}
	if body["error"] != "LLM down" {
		t.Errorf("error alias missing: got %v", body["error"])
	}
	if body["degraded"] != true {
		t.Errorf("extras lost: degraded=%v", body["degraded"])
	}
}

func TestOK_Shape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		OK(c, "saved")
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", w.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if int(body["code"].(float64)) != 200 {
		t.Errorf("code: want 200, got %v", body["code"])
	}
	if body["message"] != "saved" {
		t.Errorf("message: want %q, got %v", "saved", body["message"])
	}
}

func TestError_AbortsTheChain(t *testing.T) {
	// Error must abort: any handler after Error in the chain should not run.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	var afterCalled bool
	r.GET("/x",
		func(c *gin.Context) { Error(c, http.StatusForbidden, "nope") },
		func(c *gin.Context) { afterCalled = true },
	)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: want 403, got %d", w.Code)
	}
	if afterCalled {
		t.Error("Error did not abort: subsequent handler ran")
	}
}
