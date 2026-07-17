package api

import (
	"context"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// newNilTask returns a TaskHandler with nil services.
// Safe only for early-return validation paths.
func newNilTask() *TaskHandler { return &TaskHandler{} }

// ---------------------------------------------------------------------------
// CreateTask
// ---------------------------------------------------------------------------

func TestTaskHandler_CreateTask_BadID(t *testing.T) {
	r := singleRoute("POST", "/assets/:id/tasks", newNilTask().CreateTask)
	for _, id := range []string{"abc", "0", "-1"} {
		w := do(r, "POST", "/assets/"+id+"/tasks", nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%s: want 400, got %d", id, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// GetTask
// ---------------------------------------------------------------------------

func TestTaskHandler_GetTask_BadID(t *testing.T) {
	r := singleRoute("GET", "/tasks/:id", newNilTask().GetTask)
	w := do(r, "GET", "/tasks/bad", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// AssignTask
// ---------------------------------------------------------------------------

func TestTaskHandler_AssignTask_BadID(t *testing.T) {
	r := singleRoute("PUT", "/tasks/:id/assign", newNilTask().AssignTask)
	w := do(r, "PUT", "/tasks/0/assign", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestTaskHandler_AssignTask_BadJSON(t *testing.T) {
	r := singleRoute("PUT", "/tasks/:id/assign", newNilTask().AssignTask)
	req := httptest.NewRequest("PUT", "/tasks/1/assign", bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestTaskHandler_AssignTask_AllNil(t *testing.T) {
	r := singleRoute("PUT", "/tasks/:id/assign", newNilTask().AssignTask)
	w := do(r, "PUT", "/tasks/1/assign", map[string]interface{}{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for all-nil body, got %d", w.Code)
	}
}

func TestTaskHandler_AssignTask_BadDeadline(t *testing.T) {
	r := singleRoute("PUT", "/tasks/:id/assign", newNilTask().AssignTask)
	uid := uint(1)
	w := do(r, "PUT", "/tasks/1/assign", map[string]interface{}{
		"assignee_id": uid, "deadline_at": "not-a-time",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad deadline, got %d", w.Code)
	}
}

func TestTaskHandler_AssignTask_ValidDeadlineShortCircuitsOnBadID(t *testing.T) {
	r := singleRoute("PUT", "/tasks/:id/assign", newNilTask().AssignTask)
	uid := uint(5)
	w := do(r, "PUT", "/tasks/0/assign", map[string]interface{}{
		"assignee_id": uid, "deadline_at": "2026-07-01T00:00:00Z",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("id=0 should short-circuit before service call, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// BatchAssignTasks
// ---------------------------------------------------------------------------

func TestTaskHandler_BatchAssign_BadJSON(t *testing.T) {
	r := singleRoute("POST", "/tasks/batch-assign", newNilTask().BatchAssignTasks)
	req := httptest.NewRequest("POST", "/tasks/batch-assign", bytes.NewBufferString("{bad"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestTaskHandler_BatchAssign_EmptyTaskIDs(t *testing.T) {
	r := singleRoute("POST", "/tasks/batch-assign", newNilTask().BatchAssignTasks)
	uid := uint(3)
	w := do(r, "POST", "/tasks/batch-assign", map[string]interface{}{
		"task_ids": []uint{}, "assignee_id": uid,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty task_ids, got %d", w.Code)
	}
}

func TestTaskHandler_BatchAssign_AllNil(t *testing.T) {
	r := singleRoute("POST", "/tasks/batch-assign", newNilTask().BatchAssignTasks)
	w := do(r, "POST", "/tasks/batch-assign", map[string]interface{}{"task_ids": []uint{1, 2}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for all-nil assign fields, got %d", w.Code)
	}
}

func TestTaskHandler_BatchAssign_BadDeadline(t *testing.T) {
	r := singleRoute("POST", "/tasks/batch-assign", newNilTask().BatchAssignTasks)
	uid := uint(1)
	w := do(r, "POST", "/tasks/batch-assign", map[string]interface{}{
		"task_ids": []uint{1}, "assignee_id": uid, "deadline_at": "not-a-date",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400 for bad deadline, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Reprocess, GetAdjacentTasks — bad ID branch
// ---------------------------------------------------------------------------

func testTaskBadID(t *testing.T, method, handlerPath string, h func(c *gin.Context)) {
	t.Helper()
	for _, id := range []string{"xyz", "0"} {
		// inline since helper not used; keep call site simple
		target := strings.Replace(handlerPath, ":id", id, 1)
		r := singleRoute(method, handlerPath, h)
		w := do(r, method, target, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s id=%s: want 400, got %d", handlerPath, id, w.Code)
		}
	}
}

func TestTaskHandler_Reprocess_BadID(t *testing.T) {
	testTaskBadID(t, "POST", "/tasks/:id/reprocess", newNilTask().Reprocess)
}

func TestTaskHandler_GetAdjacentTasks_BadID(t *testing.T) {
	testTaskBadID(t, "GET", "/tasks/:id/adjacent", newNilTask().GetAdjacentTasks)
}

// ---------------------------------------------------------------------------
// Pure helpers: parseDeadline, taskIDParam (live in task_handler.go)
// ---------------------------------------------------------------------------

func TestParseDeadline_Nil(t *testing.T) {
	got, err := parseDeadline(nil)
	if err != nil || got != nil {
		t.Errorf("nil input: want (nil, nil), got (%v, %v)", got, err)
	}
}

func TestParseDeadline_EmptyString(t *testing.T) {
	s := ""
	got, err := parseDeadline(&s)
	if err != nil {
		t.Fatalf("empty string: unexpected error: %v", err)
	}
	if got == nil || !got.IsZero() {
		t.Errorf("empty string: want zero time, got %v", got)
	}
}

func TestParseDeadline_ValidRFC3339(t *testing.T) {
	s := "2026-06-01T00:00:00Z"
	got, err := parseDeadline(&s)
	if err != nil {
		t.Fatalf("valid RFC3339: unexpected error: %v", err)
	}
	if got == nil || got.Year() != 2026 {
		t.Errorf("valid RFC3339: unexpected result %v", got)
	}
}

func TestParseDeadline_Invalid(t *testing.T) {
	s := "not-a-date"
	_, err := parseDeadline(&s)
	if err == nil {
		t.Error("invalid string: expected error, got nil")
	}
}

func TestTaskIDParam_ZeroReturnsError(t *testing.T) {
	r := singleRoute("GET", "/tasks/:id", newNilTask().GetTask)
	w := do(r, "GET", "/tasks/0", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("id=0: want 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Happy-path tests using mockTaskRepo (TD-03.a supplement after TD-08.c)
// ---------------------------------------------------------------------------

// taskMockRepo is an inline implementation of iface.DBAnnotationTaskRepo
// for api-layer happy-path tests. No real DB needed.
type taskMockRepo struct {
	tasks  map[uint]*dbmodel.AnnotationTask
	nextID uint
}

func newTaskMockRepo() *taskMockRepo {
	return &taskMockRepo{tasks: make(map[uint]*dbmodel.AnnotationTask), nextID: 1}
}
func (m *taskMockRepo) CreateAnnotationTask(_ context.Context, t *dbmodel.AnnotationTask) error {
	t.ID = m.nextID
	m.nextID++
	cp := *t
	m.tasks[t.ID] = &cp
	return nil
}
func (m *taskMockRepo) FindAnnotationTaskByID(_ context.Context, id uint) (*dbmodel.AnnotationTask, error) {
	t, ok := m.tasks[id]
	if !ok {
		return nil, errNotFoundAPI
	}
	cp := *t
	return &cp, nil
}
func (m *taskMockRepo) UpdateAnnotationTask(_ context.Context, id uint, _ map[string]interface{}) error {
	if _, ok := m.tasks[id]; !ok {
		return errNotFoundAPI
	}
	return nil
}
func (m *taskMockRepo) BatchUpdateAnnotationTasks(_ context.Context, ids []uint, _ map[string]interface{}) (int64, error) {
	var n int64
	for _, id := range ids {
		if _, ok := m.tasks[id]; ok {
			n++
		}
	}
	return n, nil
}
func (m *taskMockRepo) ListAnnotationTasksPage(_ context.Context, _ repository.AnnotationTaskFilter, _, _ int) ([]dbmodel.AnnotationTask, int64, error) {
	var out []dbmodel.AnnotationTask
	for _, t := range m.tasks {
		out = append(out, *t)
	}
	return out, int64(len(out)), nil
}
func (m *taskMockRepo) FindAdjacentTaskIDsByUser(_ context.Context, _, _ uint) (*uint, *uint, error) { return nil, nil, nil }
func (m *taskMockRepo) FindAdjacentAssetIDs(_ context.Context, _, _ uint) (*uint, *uint, error)      { return nil, nil, nil }

// errNotFoundAPI is local to the api test package.
var errNotFoundAPI = errors.New("not found")

// newTaskHandlerWithMock returns a TaskHandler backed by an in-memory task repo.
// asset service is nil — tests must avoid paths that call asset.GetAsset.
func newTaskHandlerWithMock(repo *taskMockRepo) *TaskHandler {
	taskSvc := service.NewAnnotationTaskService(repo)
	return NewTaskHandler(nil, taskSvc)
}

func TestTaskHandler_GetTask_HappyPath(t *testing.T) {
	repo := newTaskMockRepo()
	_ = repo.CreateAnnotationTask(context.Background(), &dbmodel.AnnotationTask{AssetID: 1, State: dbmodel.TaskStateCreated})
	h := newTaskHandlerWithMock(repo)
	r := singleRoute("GET", "/tasks/:id", h.GetTask)

	w := do(r, "GET", "/tasks/1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %s", w.Body.String())
	}
}

func TestTaskHandler_GetTask_NotFound(t *testing.T) {
	h := newTaskHandlerWithMock(newTaskMockRepo())
	r := singleRoute("GET", "/tasks/:id", h.GetTask)
	w := do(r, "GET", "/tasks/99", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404 for unknown task, got %d", w.Code)
	}
}

func TestTaskHandler_ListTasks_ReturnsJSON(t *testing.T) {
	repo := newTaskMockRepo()
	_ = repo.CreateAnnotationTask(context.Background(), &dbmodel.AnnotationTask{AssetID: 1, State: dbmodel.TaskStateCreated})
	_ = repo.CreateAnnotationTask(context.Background(), &dbmodel.AnnotationTask{AssetID: 2, State: dbmodel.TaskStateRouting})
	h := newTaskHandlerWithMock(repo)
	r := singleRoute("GET", "/tasks", h.ListTasks)

	w := do(r, "GET", "/tasks", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %s", w.Body.String())
	}
	if _, ok := resp["items"]; !ok {
		t.Error("expected 'items' key in response")
	}
	if _, ok := resp["total"]; !ok {
		t.Error("expected 'total' key in response")
	}
}
