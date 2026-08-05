package api

// Slice 端点(C3.1)的处理器锁:viewer 逐片取数的路径。volume_slices 的
// storage_uri 是前缀(C0.2),端点拼 {z:04d}.png 后取出。断言:命中片正确回、
// 越界 z 与缺派生物 404、坏 z 400。跑真 Postgres + 本地对象存储。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"
	"text-annotation-platform/internal/testutil"

	"github.com/gin-gonic/gin"
)

func TestAssetHandler_Slice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	store, err := service.NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()

	ds := &dbmodel.Dataset{Name: "vol", Modality: dbmodel.ModalityVolume, DataSource: dbmodel.DataSourcePublicDeidentified}
	if err := db.DB.Create(ds).Error; err != nil {
		t.Fatalf("dataset: %v", err)
	}
	sha := strings.Repeat("ab", 32)
	asset := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityVolume, SHA256: sha, QCStatus: dbmodel.QCStatusPassed}
	if err := db.DB.Create(asset).Error; err != nil {
		t.Fatalf("asset: %v", err)
	}

	// Seed one slice (0000.png) + the prefix derivative row, mimicking deriveVolume.
	prefix := fmt.Sprintf("derived/%s/%s/v1/", sha, dbmodel.DerivativeVolumeSlices)
	wantBody := []byte("\x89PNG\r\n\x1a\nSLICE-ZERO")
	if _, err := store.PutAt(ctx, prefix+"0000.png", strings.NewReader(string(wantBody)), int64(len(wantBody)), "image/png"); err != nil {
		t.Fatalf("put slice: %v", err)
	}
	if err := db.UpsertDerivative(ctx, &dbmodel.AssetDerivative{
		AssetID: asset.ID, Kind: dbmodel.DerivativeVolumeSlices, Version: 1,
		ParamsHash: "vs-png16-v1", StorageURI: "local://" + prefix, Status: "ready", SizeBytes: int64(len(wantBody)),
	}); err != nil {
		t.Fatalf("upsert deriv: %v", err)
	}

	svc := service.NewAssetService(db, store, nil)
	h := NewAssetHandler(svc, nil)
	r := gin.New()
	r.GET("/assets/:id/slice/:z", h.Slice)

	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	// hit slice 0 → 200 + exact bytes (local store has no presign → streams).
	if w := get(fmt.Sprintf("/assets/%d/slice/0", asset.ID)); w.Code != http.StatusOK {
		t.Errorf("slice 0 → %d want 200", w.Code)
	} else if w.Body.String() != string(wantBody) {
		t.Errorf("slice 0 body mismatch")
	} else if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("slice content-type = %q want image/png", ct)
	}

	// out-of-range z (no object) → 404, not 500, not a wrong slice.
	if w := get(fmt.Sprintf("/assets/%d/slice/7", asset.ID)); w.Code != http.StatusNotFound {
		t.Errorf("missing slice 7 → %d want 404", w.Code)
	}
	// non-numeric z → 400.
	if w := get(fmt.Sprintf("/assets/%d/slice/abc", asset.ID)); w.Code != http.StatusBadRequest {
		t.Errorf("bad z → %d want 400", w.Code)
	}
	// asset with no volume_slices derivative → 404.
	other := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityVolume, SHA256: strings.Repeat("cd", 32), QCStatus: dbmodel.QCStatusPassed}
	db.DB.Create(other)
	if w := get(fmt.Sprintf("/assets/%d/slice/0", other.ID)); w.Code != http.StatusNotFound {
		t.Errorf("no-derivative asset slice → %d want 404", w.Code)
	}
}
