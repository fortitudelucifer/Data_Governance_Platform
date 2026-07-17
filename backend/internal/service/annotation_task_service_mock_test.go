package service

import (
	"context"
	"testing"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// mockAnnotationTaskRepo implements iface.DBAnnotationTaskRepo without a DB.
type mockAnnotationTaskRepo struct {
	tasks  map[uint]*dbmodel.AnnotationTask
	nextID uint
}

func newMockAnnotationTaskRepo() *mockAnnotationTaskRepo {
	return &mockAnnotationTaskRepo{
		tasks:  make(map[uint]*dbmodel.AnnotationTask),
		nextID: 1,
	}
}

func (m *mockAnnotationTaskRepo) CreateAnnotationTask(_ context.Context, task *dbmodel.AnnotationTask) error {
	task.ID = m.nextID
	m.nextID++
	cp := *task
	m.tasks[task.ID] = &cp
	return nil
}
func (m *mockAnnotationTaskRepo) FindAnnotationTaskByID(_ context.Context, id uint) (*dbmodel.AnnotationTask, error) {
	t, ok := m.tasks[id]
	if !ok {
		return nil, errNotFound
	}
	cp := *t
	return &cp, nil
}
func (m *mockAnnotationTaskRepo) UpdateAnnotationTask(_ context.Context, id uint, updates map[string]interface{}) error {
	task, ok := m.tasks[id]
	if !ok {
		return errNotFound
	}
	if state, ok := updates["state"].(string); ok {
		task.State = state
	}
	if v, ok := updates["version"].(int); ok {
		task.Version = v
	}
	return nil
}
func (m *mockAnnotationTaskRepo) BatchUpdateAnnotationTasks(_ context.Context, ids []uint, _ map[string]interface{}) (int64, error) {
	var count int64
	for _, id := range ids {
		if _, ok := m.tasks[id]; ok {
			count++
		}
	}
	return count, nil
}
func (m *mockAnnotationTaskRepo) ListAnnotationTasksPage(_ context.Context, _ repository.AnnotationTaskFilter, _, _ int) ([]dbmodel.AnnotationTask, int64, error) {
	var out []dbmodel.AnnotationTask
	for _, t := range m.tasks {
		out = append(out, *t)
	}
	return out, int64(len(out)), nil
}
func (m *mockAnnotationTaskRepo) FindAdjacentTaskIDsByUser(_ context.Context, _, _ uint) (*uint, *uint, error) {
	return nil, nil, nil
}
func (m *mockAnnotationTaskRepo) FindAdjacentAssetIDs(_ context.Context, _, _ uint) (*uint, *uint, error) {
	return nil, nil, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAnnotationTaskServiceMock_CreateForAsset_NilAsset(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	_, err := svc.CreateForAsset(context.Background(), nil, CreateTaskOptions{})
	if err == nil {
		t.Error("expected error for nil asset")
	}
}

func TestAnnotationTaskServiceMock_CreateForAsset_ZeroID(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	_, err := svc.CreateForAsset(context.Background(), &dbmodel.Asset{}, CreateTaskOptions{})
	if err == nil {
		t.Error("expected error for asset with ID=0")
	}
}

func TestAnnotationTaskServiceMock_CreateForAsset_QCPassed(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	asset := &dbmodel.Asset{ID: 1, DatasetID: 10, QCStatus: dbmodel.QCStatusPassed}
	task, err := svc.CreateForAsset(context.Background(), asset, CreateTaskOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.State != dbmodel.TaskStateRouting {
		t.Errorf("expected ROUTING for QC-passed asset, got %s", task.State)
	}
	if task.TraceID == "" {
		t.Error("expected non-empty trace_id")
	}
}

func TestAnnotationTaskServiceMock_CreateForAsset_QCFailed(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	asset := &dbmodel.Asset{ID: 2, QCStatus: dbmodel.QCStatusFailed}
	task, err := svc.CreateForAsset(context.Background(), asset, CreateTaskOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.State != dbmodel.TaskStateQCFailed {
		t.Errorf("expected QC_FAILED, got %s", task.State)
	}
}

func TestAnnotationTaskServiceMock_Get_NotFound(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	_, err := svc.Get(context.Background(), 999)
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

func TestAnnotationTaskServiceMock_Reprocess_NonTerminal(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	asset := &dbmodel.Asset{ID: 5, QCStatus: dbmodel.QCStatusPassed}
	task, _ := svc.CreateForAsset(context.Background(), asset, CreateTaskOptions{})
	// ROUTING is not terminal → Reprocess must fail
	if err := svc.Reprocess(context.Background(), task.ID); err == nil {
		t.Error("expected error for non-terminal task")
	}
}

func TestAnnotationTaskServiceMock_BatchAssign_EmptyIDs(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	n, err := svc.BatchAssign(context.Background(), nil, nil, nil, nil)
	if err != nil || n != 0 {
		t.Errorf("expected (0, nil), got (%d, %v)", n, err)
	}
}

func TestAnnotationTaskServiceMock_BatchAssign_CountsRows(t *testing.T) {
	svc := NewAnnotationTaskService(newMockAnnotationTaskRepo())
	a1 := &dbmodel.Asset{ID: 10, QCStatus: dbmodel.QCStatusPassed}
	a2 := &dbmodel.Asset{ID: 11, QCStatus: dbmodel.QCStatusPassed}
	t1, _ := svc.CreateForAsset(context.Background(), a1, CreateTaskOptions{})
	t2, _ := svc.CreateForAsset(context.Background(), a2, CreateTaskOptions{})
	uid := uint(7)
	dl := time.Now().Add(24 * time.Hour)
	n, err := svc.BatchAssign(context.Background(), []uint{t1.ID, t2.ID}, &uid, nil, &dl)
	if err != nil {
		t.Fatalf("BatchAssign error: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 rows affected, got %d", n)
	}
}
