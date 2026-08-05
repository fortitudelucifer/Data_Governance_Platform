package api

import (
	"context"
	"errors"
	"encoding/json"
	"net/http"
	"testing"

	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/service"
)

// newLLMHandlerWithEmptyRegistry builds an LLMHandler backed by a real LLMService
// that has no DB connection and no plugins registered.
// This lets us test paths beyond input validation:
//   - ListTaskTypes → returns empty array (no panic, no DB)
//   - GenerateQACandidates with valid body → "QA generation plugin not found" → 500
func newLLMHandlerWithEmptyRegistry() *LLMHandler {
	taskReg := plugin.NewPluginRegistry[plugin.TaskPlugin]()
	// capSvc is nil; it is never reached because the qa_generation plugin is not
	// registered — taskRegistry.Get("qa_generation") fails before capSvc.Invoke.
	svc := service.NewLLMService(nil, taskReg, nil, service.LLMServiceConfig{})
	return NewLLMHandler(svc)
}

// ---------------------------------------------------------------------------
// GenerateQACandidates — validation branches (service not reached)
// ---------------------------------------------------------------------------

func TestLLMHandler_GenerateQACandidates_BadDatasetID(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/qa/llm_suggest", newLLMHandlerWithEmptyRegistry().GenerateQACandidates)
	for _, id := range []string{"abc", "0"} {
		w := do(r, "POST", "/datasets/"+id+"/qa/llm_suggest", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%s: want 400, got %d", id, w.Code)
		}
	}
}

func TestLLMHandler_GenerateQACandidates_BadJSON(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/qa/llm_suggest", newLLMHandlerWithEmptyRegistry().GenerateQACandidates)
	w := doRaw(r, "POST", "/datasets/1/qa/llm_suggest", "not-json", "application/json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad JSON, got %d", w.Code)
	}
}

func TestLLMHandler_GenerateQACandidates_MissingTextSpan(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/qa/llm_suggest", newLLMHandlerWithEmptyRegistry().GenerateQACandidates)
	w := do(r, "POST", "/datasets/1/qa/llm_suggest", map[string]interface{}{
		"doc_key":   "doc-1",
		"text_span": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty text_span, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GenerateQACandidates — valid body reaches service; plugin not registered → 500
// ---------------------------------------------------------------------------

func TestLLMHandler_GenerateQACandidates_PluginNotFound(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/qa/llm_suggest", newLLMHandlerWithEmptyRegistry().GenerateQACandidates)
	w := do(r, "POST", "/datasets/1/qa/llm_suggest", map[string]interface{}{
		"doc_key":   "doc-1",
		"text_span": "some legal text here",
	})
	// No qa_generation plugin registered → service returns error → 500
	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500 when plugin not found, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["error"]; !ok {
		t.Error("response should contain 'error' key")
	}
}

// ---------------------------------------------------------------------------
// GenerateQACandidates — LLMDegradedError path → 503 with degraded:true
// To reach this path we need: a registered qa_generation plugin AND a
// CapabilityService adapter that returns *LLMDegradedError.
// ---------------------------------------------------------------------------

// degradedCapAdapter is a test CapabilityAdapter that always returns LLMDegradedError.
type degradedCapAdapter struct{}

func (a *degradedCapAdapter) Capability() string { return service.CapabilityTextChat }
func (a *degradedCapAdapter) Invoke(_ context.Context, _ service.CapabilityRequest) (service.CapabilityResponse, error) {
	return service.CapabilityResponse{Status: "failed"},
		&service.LLMDegradedError{Cause: errors.New("upstream down"), Retries: 3}
}

// minimalQAPlugin is a TaskPlugin stub that returns a fixed prompt and empty result.
type minimalQAPlugin struct{}

func (p *minimalQAPlugin) TypeID() string                                       { return "qa_generation" }
func (p *minimalQAPlugin) Name() string                                         { return "QA Generation" }
func (p *minimalQAPlugin) BuildPrompt(text string, _ map[string]interface{}) (string, error) {
	return "prompt:" + text, nil
}
func (p *minimalQAPlugin) ParseResult(_ string) (interface{}, error)            { return []interface{}{}, nil }
func (p *minimalQAPlugin) ValidateResult(_ interface{}) error                   { return nil }

func TestLLMHandler_GenerateQACandidates_DegradedReturns503(t *testing.T) {
	taskReg := plugin.NewPluginRegistry[plugin.TaskPlugin]()
	if err := taskReg.Register("qa_generation", plugin.TaskPlugin(&minimalQAPlugin{})); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	capSvc := service.NewCapabilityService(nil)
	capSvc.Register(&degradedCapAdapter{})

	svc := service.NewLLMService(nil, taskReg, capSvc, service.LLMServiceConfig{})
	h := NewLLMHandler(svc)
	r := singleRoute("POST", "/datasets/:id/qa/llm_suggest", h.GenerateQACandidates)

	w := do(r, "POST", "/datasets/1/qa/llm_suggest", map[string]interface{}{
		"text_span": "some legal text",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 for LLMDegradedError, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if degraded, _ := resp["degraded"].(bool); !degraded {
		t.Errorf("want degraded:true in response, got %v", resp)
	}
}

// ---------------------------------------------------------------------------
// ListTaskTypes — fully testable: no DB, returns empty array for empty registry
// ---------------------------------------------------------------------------

func TestLLMHandler_ListTaskTypes_EmptyRegistry(t *testing.T) {
	r := singleRoute("GET", "/llm/task_types", newLLMHandlerWithEmptyRegistry().ListTaskTypes)
	w := do(r, "GET", "/llm/task_types", nil)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	// Expect a JSON array (possibly empty)
	var result []interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Errorf("response is not a JSON array: %s", w.Body.String())
	}
}

func TestLLMHandler_ListTaskTypes_WithRegisteredPlugin(t *testing.T) {
	taskReg := plugin.NewPluginRegistry[plugin.TaskPlugin]()
	if err := taskReg.Register("qa_generation", plugin.TaskPlugin(&minimalQAPlugin{})); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	svc := service.NewLLMService(nil, taskReg, nil, service.LLMServiceConfig{})
	r := singleRoute("GET", "/llm/task_types", NewLLMHandler(svc).ListTaskTypes)

	w := do(r, "GET", "/llm/task_types", nil)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	var result []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("want 1 task type, got %d", len(result))
	}
	if result[0]["type_id"] != "qa_generation" {
		t.Errorf("unexpected type_id: %v", result[0]["type_id"])
	}
}
