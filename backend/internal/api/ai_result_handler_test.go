package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newNilAIResult() *AIResultHandler { return &AIResultHandler{} }

func testAIResultBadID(t *testing.T, method, handlerPath string, h gin.HandlerFunc) {
	t.Helper()
	for _, id := range []string{"xyz", "0"} {
		target := strings.Replace(handlerPath, ":id", id, 1)
		r := singleRoute(method, handlerPath, h)
		w := do(r, method, target, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s id=%s: want 400, got %d", handlerPath, id, w.Code)
		}
	}
}

func TestAIResultHandler_GetRouting_BadID(t *testing.T) {
	testAIResultBadID(t, "GET", "/tasks/:id/routing", newNilAIResult().GetRouting)
}

func TestAIResultHandler_GetAIRuns_BadID(t *testing.T) {
	testAIResultBadID(t, "GET", "/tasks/:id/ai-runs", newNilAIResult().GetAIRuns)
}

func TestAIResultHandler_GetAIResults_BadID(t *testing.T) {
	testAIResultBadID(t, "GET", "/tasks/:id/ai-results", newNilAIResult().GetAIResults)
}

func TestAIResultHandler_GetTrace_BadID(t *testing.T) {
	testAIResultBadID(t, "GET", "/tasks/:id/trace", newNilAIResult().GetTrace)
}

func TestAIResultHandler_ListCapabilities_NilCapability(t *testing.T) {
	r := singleRoute("GET", "/capabilities", newNilAIResult().ListCapabilities)
	w := do(r, "GET", "/capabilities", nil)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	caps, ok := resp["capabilities"]
	if !ok {
		t.Fatal("response missing 'capabilities' key")
	}
	arr, ok := caps.([]interface{})
	if !ok || len(arr) != 0 {
		t.Errorf("want empty capabilities array, got %v", caps)
	}
}

func TestAIResultHandler_InvokeCapability_BadID(t *testing.T) {
	r := singleRoute("POST", "/tasks/:id/invoke", newNilAIResult().InvokeCapabilityOnTask)
	w := do(r, "POST", "/tasks/bad/invoke", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad task id, got %d", w.Code)
	}
}

func TestAIResultHandler_InvokeCapability_MissingCapability(t *testing.T) {
	r := singleRoute("POST", "/tasks/:id/invoke", newNilAIResult().InvokeCapabilityOnTask)
	w := do(r, "POST", "/tasks/1/invoke", map[string]interface{}{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for missing capability, got %d", w.Code)
	}
}

func TestAIResultHandler_InvokeCapability_NilAdhocService(t *testing.T) {
	r := singleRoute("POST", "/tasks/:id/invoke", newNilAIResult().InvokeCapabilityOnTask)
	w := do(r, "POST", "/tasks/1/invoke?capability=vlm.structured", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when adhoc service is nil, got %d", w.Code)
	}
}
