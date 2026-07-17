package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"text-annotation-platform/internal/api"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// emptyDeps returns a Deps that contains just enough non-nil fields for
// RegisterRoutes to succeed at compile time. Most handlers are non-nil
// zero-struct pointers; the routes are registered but calling them would
// nil-deref into internal services — that's fine because we only assert on
// the registered route table, not on handler execution.
func emptyDeps() Deps {
	return Deps{
		AuthService:            &service.AuthService{},
		AllowedOrigins:         []string{"http://localhost:5173"},
		AuthHandler:            &api.AuthHandler{},
		CategoryHandler:        &api.DatasetCategoryHandler{},
		TagHandler:             &api.TagHandler{},
		DatasetHandler:         &api.DatasetHandler{},
		DocumentHandler:        &api.DocumentHandler{},
		LLMHandler:             &api.LLMHandler{},
		ExportHandler:          &api.ExportHandler{},
		SamplingHandler:        &api.SamplingHandler{},
		AuditHandler:           &api.AuditHandler{},
		SystemPromptHandler:    &api.SystemPromptHandler{},
		AutoAnnotateHandler:    &api.AutoAnnotateHandler{},
		RefinementHandler:      &api.RefinementHandler{},
		DashboardHandler:       &api.DashboardHandler{},
		ExtractionHandler:      &api.ExtractionHandler{},
		DatasetFunctionHandler: &api.DatasetFunctionHandler{},
		LLMRefinementHandler:   &api.LLMRefinementHandler{},
	}
}

// fullDeps adds all the multi-modal + admin handlers on top of emptyDeps.
func fullDeps() Deps {
	d := emptyDeps()
	d.UserHandler = &api.UserHandler{}
	d.CapabilityConfigHandler = &api.CapabilityConfigHandler{}
	d.AssetHandler = &api.AssetHandler{}
	d.TaskHandler = &api.TaskHandler{}
	d.AnnotationHandler = &api.AnnotationHandler{}
	d.AIResultHandler = &api.AIResultHandler{}
	d.ImageExportHandler = &api.ImageExportHandler{}
	d.TextCandidateHandler = api.NewTextCandidateHandler(nil)
	return d
}

func routeSet(r *gin.Engine) map[string]struct{} {
	out := make(map[string]struct{})
	for _, ri := range r.Routes() {
		out[ri.Method+" "+ri.Path] = struct{}{}
	}
	return out
}

func TestRegisterRoutes_FullDepsCoversMultiModal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r, fullDeps())

	routes := routeSet(r)
	expectations := []string{
		// Health
		"GET /health",
		// V1
		"POST /auth/login",
		"GET /dataset_categories",
		"GET /tags",
		"GET /datasets",
		"GET /llm/task_types",
		"GET /audit_logs",
		"GET /dashboard/stats",
		// Multi-modal (require AssetHandler/TaskHandler/etc. set)
		"GET /tasks",
		"GET /tasks/:id",
		"GET /tasks/:id/routing",
		"GET /tasks/:id/human-annotation",
		"GET /datasets/:id/assets",
		"GET /datasets/:id/export.coco.json",
		"GET /capabilities",
		// Admin
		"POST /users",
		"GET /capabilities/providers",
		"GET /datasets/:id/auto_annotate/prompts",
		"GET /datasets/:id/auto_annotate/judge_prompts",
		"POST /datasets/:id/auto_annotate/compare",
		"POST /datasets/:id/auto_annotate/judge",
		"DELETE /documents/:key/auto_annotate/candidates/:run_id",
		"GET /documents/:key/auto_annotate/judges",
		"POST /documents/:key/qa_pairs/adopt_judge",
		// Multi-modal-conditional dashboard route
		"GET /dashboard/image-annotators",
	}
	for _, p := range expectations {
		if _, ok := routes[p]; !ok {
			t.Errorf("expected route not registered: %q", p)
		}
	}
}

func TestRegisterRoutes_RunnerModeSkipsMultiModal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r, emptyDeps())

	routes := routeSet(r)

	// V1 routes still present.
	for _, p := range []string{
		"GET /health",
		"POST /auth/login",
		"GET /dataset_categories",
		"GET /dashboard/stats",
	} {
		if _, ok := routes[p]; !ok {
			t.Errorf("V1 route missing in runner mode: %q", p)
		}
	}

	// Multi-modal routes must NOT be present when those handlers are nil.
	for _, p := range []string{
		"GET /tasks",
		"GET /tasks/:id",
		"GET /tasks/:id/routing",
		"GET /datasets/:id/assets",
		"GET /datasets/:id/export.coco.json",
		"GET /capabilities",
		"POST /users",
		"GET /capabilities/providers",
		"GET /datasets/:id/auto_annotate/prompts",
		"GET /datasets/:id/auto_annotate/judge_prompts",
		"POST /datasets/:id/auto_annotate/compare",
		"POST /datasets/:id/auto_annotate/judge",
		"DELETE /documents/:key/auto_annotate/candidates/:run_id",
		"GET /documents/:key/auto_annotate/judges",
		"POST /documents/:key/qa_pairs/adopt_judge",
		"GET /dashboard/image-annotators",
	} {
		if _, ok := routes[p]; ok {
			t.Errorf("multi-modal route should not be registered in runner mode: %q", p)
		}
	}
}

func TestRegisterRoutes_HealthEndpointReturns200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterRoutes(r, emptyDeps())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /health returned %d, want 200", w.Code)
	}
}
