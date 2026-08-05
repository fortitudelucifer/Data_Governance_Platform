package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"text-annotation-platform/internal/model"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// errNotFound is a sentinel used by mock repos.
var errNotFound = errors.New("not found")

// ---------------------------------------------------------------------------
// mockDatasetRepo — implements iface.DBDatasetRepo without a real DB
// ---------------------------------------------------------------------------

type mockDatasetRepo struct {
	datasets         map[uint]*dbmodel.Dataset
	categories       map[uint]*dbmodel.DatasetCategory
	tags             map[uint]*dbmodel.Tag
	nextID           uint
	createDatasetErr error
}

func newMockDatasetRepo() *mockDatasetRepo {
	return &mockDatasetRepo{
		datasets:   make(map[uint]*dbmodel.Dataset),
		categories: make(map[uint]*dbmodel.DatasetCategory),
		tags:       make(map[uint]*dbmodel.Tag),
		nextID:     1,
	}
}

func (m *mockDatasetRepo) CreateDataset(_ context.Context, ds *dbmodel.Dataset) error {
	if m.createDatasetErr != nil {
		return m.createDatasetErr
	}
	ds.ID = m.nextID
	m.nextID++
	m.datasets[ds.ID] = ds
	return nil
}
func (m *mockDatasetRepo) FindDatasetByID(_ context.Context, id uint) (*dbmodel.Dataset, error) {
	ds, ok := m.datasets[id]
	if !ok {
		return nil, errNotFound
	}
	return ds, nil
}
func (m *mockDatasetRepo) FindDatasetsByIDs(_ context.Context, ids []uint) ([]dbmodel.Dataset, error) {
	var out []dbmodel.Dataset
	for _, id := range ids {
		if ds, ok := m.datasets[id]; ok {
			out = append(out, *ds)
		}
	}
	return out, nil
}
func (m *mockDatasetRepo) ListDatasetListItems(_ context.Context, _ repository.DatasetFilter) ([]repository.DatasetListItem, error) {
	var out []repository.DatasetListItem
	for _, ds := range m.datasets {
		out = append(out, repository.DatasetListItem{ID: ds.ID, Name: ds.Name})
	}
	return out, nil
}
func (m *mockDatasetRepo) ListDatasetsPage(_ context.Context, _ repository.DatasetFilter, _ repository.DatasetListSort, _, _ int) ([]dbmodel.Dataset, int64, error) {
	var out []dbmodel.Dataset
	for _, ds := range m.datasets {
		out = append(out, *ds)
	}
	return out, int64(len(out)), nil
}
func (m *mockDatasetRepo) ListDatasetOptions(_ context.Context) ([]repository.DatasetOption, error) {
	var out []repository.DatasetOption
	for _, ds := range m.datasets {
		out = append(out, repository.DatasetOption{ID: ds.ID, Name: ds.Name})
	}
	return out, nil
}
func (m *mockDatasetRepo) UpdateDataset(_ context.Context, id uint, updates map[string]interface{}) error {
	ds, ok := m.datasets[id]
	if !ok {
		return errNotFound
	}
	if name, ok := updates["name"].(string); ok {
		ds.Name = name
	}
	return nil
}
func (m *mockDatasetRepo) DeleteDataset(_ context.Context, id uint) error {
	delete(m.datasets, id)
	return nil
}
func (m *mockDatasetRepo) UpdateDocCount(_ context.Context, _ uint, _ int) error    { return nil }
func (m *mockDatasetRepo) SetDatasetTags(_ context.Context, _ uint, _ []uint) error { return nil }
func (m *mockDatasetRepo) SetDatasetIndustryTags(_ context.Context, _ uint, _ []uint) error {
	return nil
}
func (m *mockDatasetRepo) CreateCategory(_ context.Context, cat *dbmodel.DatasetCategory) error {
	cat.ID = m.nextID
	m.nextID++
	m.categories[cat.ID] = cat
	return nil
}
func (m *mockDatasetRepo) ListCategories(_ context.Context) ([]repository.CategoryWithCount, error) {
	var out []repository.CategoryWithCount
	for _, c := range m.categories {
		out = append(out, repository.CategoryWithCount{DatasetCategory: *c})
	}
	return out, nil
}
func (m *mockDatasetRepo) UpdateCategory(_ context.Context, _ uint, _ map[string]interface{}) error {
	return nil
}
func (m *mockDatasetRepo) DeleteCategory(_ context.Context, id uint) error {
	delete(m.categories, id)
	return nil
}
func (m *mockDatasetRepo) CreateTag(_ context.Context, tag *dbmodel.Tag) error {
	tag.ID = m.nextID
	m.nextID++
	m.tags[tag.ID] = tag
	return nil
}
func (m *mockDatasetRepo) ListTags(_ context.Context, _ *string) ([]dbmodel.Tag, error) {
	var out []dbmodel.Tag
	for _, t := range m.tags {
		out = append(out, *t)
	}
	return out, nil
}
func (m *mockDatasetRepo) UpdateTag(_ context.Context, _ uint, _ map[string]interface{}) error {
	return nil
}
func (m *mockDatasetRepo) DeleteTag(_ context.Context, id uint) error {
	delete(m.tags, id)
	return nil
}
func (m *mockDatasetRepo) FindTagByName(_ context.Context, name, _ string) (*dbmodel.Tag, error) {
	for _, t := range m.tags {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, errNotFound
}

// noopDocumentDB satisfies repository.DocumentDB with all-no-op methods.
// Only DeleteDocumentsByDataset is exercised in these tests.
type noopDocumentDB struct{}

func (n *noopDocumentDB) EnsureIndexes(_ context.Context) error { return nil }
func (n *noopDocumentDB) InsertDocuments(_ context.Context, _ []paymodel.Document) error {
	return nil
}
func (n *noopDocumentDB) InsertDocument(_ context.Context, _ paymodel.Document) error { return nil }
func (n *noopDocumentDB) FindActiveDocument(_ context.Context, _ *uint, _ string, _ uint) (*paymodel.Document, error) {
	return nil, nil
}
func (n *noopDocumentDB) FindVersionHistory(_ context.Context, _ *uint, _ string, _ uint) ([]paymodel.Document, error) {
	return nil, nil
}
func (n *noopDocumentDB) FindDocumentsByDatasetPaginated(_ context.Context, _ uint, _, _ int, _ uint, _ string) (*repository.PaginatedResult, error) {
	return nil, nil
}
func (n *noopDocumentDB) FindDocumentsByDataset(_ context.Context, _ uint, _ map[string]interface{}, _ uint) ([]paymodel.Document, error) {
	return nil, nil
}
func (n *noopDocumentDB) FindDocKeysByRange(_ context.Context, _ uint, _, _ int64) ([]string, error) {
	return nil, nil
}
func (n *noopDocumentDB) CountActiveDocKeys(_ context.Context, _ uint) (int, error) { return 0, nil }
func (n *noopDocumentDB) CountActiveDocKeysByDatasets(_ context.Context, _ []uint) (map[uint]int, error) {
	return nil, nil
}
func (n *noopDocumentDB) FindExistingDocKeys(_ context.Context, _ uint, _ []string, _ uint) ([]string, error) {
	return nil, nil
}
func (n *noopDocumentDB) DeactivateVersion(_ context.Context, _ *uint, _ string, _ int) error {
	return nil
}
func (n *noopDocumentDB) DeleteDocumentByKey(_ context.Context, _ uint, _ string) (int64, error) {
	return 0, nil
}
func (n *noopDocumentDB) DeleteDocumentsByDataset(_ context.Context, _ uint) error { return nil }
func (n *noopDocumentDB) DeleteDocumentsByKeys(_ context.Context, _ uint, _ []string) error {
	return nil
}
func (n *noopDocumentDB) FindDocumentsSince(_ context.Context, _ uint, _ *time.Time, _ bool, _ uint, _ []string) ([]paymodel.Document, error) {
	return nil, nil
}
func (n *noopDocumentDB) UpdateDocStage(_ context.Context, _ *uint, _ string, _ string) error {
	return nil
}
func (n *noopDocumentDB) UpdateDocumentQAPairsAndStage(_ context.Context, _ *uint, _ string, _ []paymodel.QAPair, _ string) error {
	return nil
}
func (n *noopDocumentDB) UpdateDocumentRefinement(_ context.Context, _ *uint, _ string, _ int, _ string, _ string, _ uint, _ string) error {
	return nil
}
func (n *noopDocumentDB) RollbackDocumentRefinement(_ context.Context, _ *uint, _ string) error {
	return nil
}
func (n *noopDocumentDB) SetDocumentsDeadline(_ context.Context, _ uint, _ []string, _ string) error {
	return nil
}
func (n *noopDocumentDB) AssignDocuments(_ context.Context, _ uint, _ []string, _, _ *uint, _ *string) error {
	return nil
}
func (n *noopDocumentDB) UpdateDocumentRefinementCursor(_ context.Context, _ *uint, _ string, _ string, _ int, _ string, _ string) error {
	return nil
}
func (n *noopDocumentDB) UpdateDocumentQAPairsAndCursor(_ context.Context, _ *uint, _ string, _ string, _ []paymodel.QAPair, _ int, _ string, _ *uint, _ string) error {
	return nil
}
func (n *noopDocumentDB) GetDashboardStats(_ context.Context, _ *uint) (*model.DashboardStats, error) {
	return nil, nil
}
func (n *noopDocumentDB) GetDailyTrend(_ context.Context, _ int, _ *uint) ([]model.DailyTrend, error) {
	return nil, nil
}
func (n *noopDocumentDB) GetAnnotatorStats(_ context.Context, _ *uint) ([]model.AnnotatorStats, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// DatasetService mock-based tests — no real DB connection
// ---------------------------------------------------------------------------

func TestDatasetServiceMock_CreateDataset(t *testing.T) {
	svc := NewDatasetService(newMockDatasetRepo(), nil)
	ds, err := svc.CreateDataset(context.Background(), "test-dataset", 0, 1, nil, nil, "qa", "criminal", nil)
	if err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	if ds.Name != "test-dataset" {
		t.Errorf("unexpected name: %s", ds.Name)
	}
	if ds.ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestDatasetServiceMock_GetByID_Found(t *testing.T) {
	svc := NewDatasetService(newMockDatasetRepo(), nil)
	created, _ := svc.CreateDataset(context.Background(), "dataset-1", 0, 1, nil, nil, "qa", "civil", nil)

	found, err := svc.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if found.Name != "dataset-1" {
		t.Errorf("unexpected name: %s", found.Name)
	}
}

func TestDatasetServiceMock_GetByID_NotFound(t *testing.T) {
	svc := NewDatasetService(newMockDatasetRepo(), nil)
	_, err := svc.GetByID(context.Background(), 999)
	if err == nil {
		t.Error("expected error for non-existent dataset")
	}
}

func TestDatasetServiceMock_CreateTag_DuplicateName(t *testing.T) {
	svc := NewDatasetService(newMockDatasetRepo(), nil)
	if err := svc.CreateTag(context.Background(), &dbmodel.Tag{Name: "urgent", Type: "dataset"}); err != nil {
		t.Fatalf("first CreateTag failed: %v", err)
	}
	if err := svc.CreateTag(context.Background(), &dbmodel.Tag{Name: "urgent", Type: "dataset"}); err == nil {
		t.Error("expected error for duplicate tag name")
	}
}

func TestDatasetServiceMock_DeleteDataset(t *testing.T) {
	svc := NewDatasetService(newMockDatasetRepo(), &noopDocumentDB{})
	created, _ := svc.CreateDataset(context.Background(), "to-delete", 0, 1, nil, nil, "qa", "criminal", nil)

	if err := svc.DeleteDataset(context.Background(), created.ID); err != nil {
		t.Fatalf("DeleteDataset failed: %v", err)
	}
	_, err := svc.GetByID(context.Background(), created.ID)
	if err == nil {
		t.Error("expected dataset to be deleted")
	}
}

func TestDatasetServiceMock_DefaultAnnotationType(t *testing.T) {
	svc := NewDatasetService(newMockDatasetRepo(), nil)
	ds, err := svc.CreateDataset(context.Background(), "defaults", 0, 1, nil, nil, "", "", nil)
	if err != nil {
		t.Fatalf("CreateDataset failed: %v", err)
	}
	if ds.AnnotationType != "qa" {
		t.Errorf("expected default annotation_type=qa, got %s", ds.AnnotationType)
	}
	if ds.CaseType != "criminal" {
		t.Errorf("expected default case_type=criminal, got %s", ds.CaseType)
	}
}
