package api

import (
	"net/http"
	"strings"
	"testing"
)

// newNilAutoAnnotate returns an AutoAnnotateHandler with a nil service.
// Safe only for paths that return before calling any service method.
func newNilAutoAnnotate() *AutoAnnotateHandler {
	return &AutoAnnotateHandler{}
}

// ---------------------------------------------------------------------------
// TriggerAutoAnnotate — validation branches only (service not reached)
// ---------------------------------------------------------------------------

func TestAutoAnnotateHandler_Trigger_BadDatasetID(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate", newNilAutoAnnotate().TriggerAutoAnnotate)
	for _, id := range []string{"abc", "0", "-5"} {
		w := do(r, "POST", "/datasets/"+id+"/auto_annotate", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%s: want 400, got %d", id, w.Code)
		}
	}
}

func TestAutoAnnotateHandler_Trigger_BadJSON(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate", newNilAutoAnnotate().TriggerAutoAnnotate)
	w := doRaw(r, "POST", "/datasets/1/auto_annotate", "not-json", "application/json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad JSON, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Trigger_EmptyDocKeys(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate", newNilAutoAnnotate().TriggerAutoAnnotate)
	w := do(r, "POST", "/datasets/1/auto_annotate", map[string]interface{}{
		"doc_keys":    []string{},
		"provider_id": 1,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty doc_keys, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Trigger_MissingProviderID(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate", newNilAutoAnnotate().TriggerAutoAnnotate)
	w := do(r, "POST", "/datasets/1/auto_annotate", map[string]interface{}{
		"doc_keys":    []string{"doc-1"},
		"provider_id": 0,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for provider_id=0, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// GetAutoAnnotateStatus — no service call at all; fully testable
// ---------------------------------------------------------------------------

func TestAutoAnnotateHandler_GetStatus_BadDatasetID(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/auto_annotate/status", newNilAutoAnnotate().GetAutoAnnotateStatus)
	w := do(r, "GET", "/datasets/xyz/auto_annotate/status", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_GetStatus_OK(t *testing.T) {
	r := singleRoute("GET", "/datasets/:id/auto_annotate/status", newNilAutoAnnotate().GetAutoAnnotateStatus)
	w := do(r, "GET", "/datasets/42/auto_annotate/status", nil)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "42") {
		t.Errorf("response should contain dataset_id 42, got: %s", body)
	}
}

// ---------------------------------------------------------------------------
// CancelAutoAnnotate — validation branches only
// ---------------------------------------------------------------------------

func TestAutoAnnotateHandler_Cancel_BadDatasetID(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/cancel", newNilAutoAnnotate().CancelAutoAnnotate)
	w := do(r, "POST", "/datasets/0/auto_annotate/cancel", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Cancel_EmptyDocKeys(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/cancel", newNilAutoAnnotate().CancelAutoAnnotate)
	w := do(r, "POST", "/datasets/1/auto_annotate/cancel", map[string]interface{}{
		"doc_keys": []string{},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty doc_keys, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Cancel_BadJSON(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/cancel", newNilAutoAnnotate().CancelAutoAnnotate)
	w := doRaw(r, "POST", "/datasets/1/auto_annotate/cancel", "{bad", "application/json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad JSON, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// RangeAutoAnnotate — validation branches only
// ---------------------------------------------------------------------------

func TestAutoAnnotateHandler_Range_BadDatasetID(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/range", newNilAutoAnnotate().RangeAutoAnnotate)
	w := do(r, "POST", "/datasets/bad/auto_annotate/range", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Range_BadJSON(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/range", newNilAutoAnnotate().RangeAutoAnnotate)
	w := doRaw(r, "POST", "/datasets/1/auto_annotate/range", "{{", "application/json")
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad JSON, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Range_NegativeStart(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/range", newNilAutoAnnotate().RangeAutoAnnotate)
	w := do(r, "POST", "/datasets/1/auto_annotate/range", map[string]interface{}{
		"start_idx":   -1,
		"end_idx":     10,
		"provider_id": 1,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for start_idx<0, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Range_EndBeforeStart(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/range", newNilAutoAnnotate().RangeAutoAnnotate)
	w := do(r, "POST", "/datasets/1/auto_annotate/range", map[string]interface{}{
		"start_idx":   5,
		"end_idx":     3,
		"provider_id": 1,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for end_idx<start_idx, got %d", w.Code)
	}
}

func TestAutoAnnotateHandler_Range_MissingProviderID(t *testing.T) {
	r := singleRoute("POST", "/datasets/:id/auto_annotate/range", newNilAutoAnnotate().RangeAutoAnnotate)
	w := do(r, "POST", "/datasets/1/auto_annotate/range", map[string]interface{}{
		"start_idx":   0,
		"end_idx":     9,
		"provider_id": 0,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for provider_id=0, got %d", w.Code)
	}
}

