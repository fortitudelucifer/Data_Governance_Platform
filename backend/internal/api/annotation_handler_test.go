package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newNilAnnotation() *AnnotationHandler { return &AnnotationHandler{} }

func testAnnotationBadID(t *testing.T, method, handlerPath string, h gin.HandlerFunc) {
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

func TestAnnotationHandler_GetHumanAnnotation_BadID(t *testing.T) {
	testAnnotationBadID(t, "GET", "/tasks/:id/human-annotation", newNilAnnotation().GetHumanAnnotation)
}

func TestAnnotationHandler_PutHumanAnnotation_BadID(t *testing.T) {
	testAnnotationBadID(t, "PUT", "/tasks/:id/human-annotation", newNilAnnotation().PutHumanAnnotation)
}

func TestAnnotationHandler_SubmitTask_BadID(t *testing.T) {
	testAnnotationBadID(t, "POST", "/tasks/:id/submit", newNilAnnotation().SubmitTask)
}

func TestAnnotationHandler_QAPass_BadID(t *testing.T) {
	testAnnotationBadID(t, "POST", "/tasks/:id/qa/pass", newNilAnnotation().QAPass)
}

func TestAnnotationHandler_QAReject_BadID(t *testing.T) {
	testAnnotationBadID(t, "POST", "/tasks/:id/qa/reject", newNilAnnotation().QAReject)
}

func TestAnnotationHandler_GetFinal_BadID(t *testing.T) {
	testAnnotationBadID(t, "GET", "/tasks/:id/final", newNilAnnotation().GetFinal)
}

// ---------------------------------------------------------------------------
// SegmentInteractive — guards before service calls
// ---------------------------------------------------------------------------

func TestAnnotationHandler_SegmentInteractive_BadID(t *testing.T) {
	r := singleRoute("POST", "/tasks/:id/segment", newNilAnnotation().SegmentInteractive)
	w := do(r, "POST", "/tasks/0/segment", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad task id, got %d", w.Code)
	}
}

func TestAnnotationHandler_SegmentInteractive_EmptyPoints(t *testing.T) {
	r := singleRoute("POST", "/tasks/:id/segment", newNilAnnotation().SegmentInteractive)
	w := do(r, "POST", "/tasks/1/segment", map[string]interface{}{"points": []interface{}{}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty points, got %d", w.Code)
	}
}

func TestAnnotationHandler_SegmentInteractive_NilCapability(t *testing.T) {
	r := singleRoute("POST", "/tasks/:id/segment", newNilAnnotation().SegmentInteractive)
	pts := [][]float64{{100, 200, 1}}
	w := do(r, "POST", "/tasks/1/segment", map[string]interface{}{"points": pts})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when capability is nil, got %d", w.Code)
	}
}
