package service

// C3.1b 集成锁:一卷 NIfTI 过 MediaWorker → volume_meta(几何)+ volume_slices
// (逐 z 的 16-bit PNG,前缀一行)。核心断言是 PNG16 像素 → 真值(HU)的仿射
// 往返无损——位深保真(C0.5)不是口号,是可解码验证的字节。跑真 Postgres + 本地
// 对象存储。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"math"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

func TestDeriveVolume_MetaAndSlices(t *testing.T) {
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	store, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()

	// int16 CT volume 2×2×2 with the air+bone HU signature (so modality infers
	// ct): base stored -500 (→ -1524 HU, air/lung), bone spike stored 2000
	// (→ +976 HU) at voxel 4 (i0,j0,k1). scl slope 1 inter -1024 → HU=stored-1024.
	aff := [3][4]float32{{0.7, 0, 0, -5}, {0, 0.7, 0, -6}, {0, 0, 3.0, 7}}
	nii := niftiBuilder{
		dims: [3]int{2, 2, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{0.7, 0.7, 3.0},
		sform: &aff, sclSlope: 1, sclInter: -1024, voxels: int16Voxels(8, -500, 2000),
	}.build()

	// Land the source blob in the store; point the asset at it.
	put, err := store.PutAt(ctx, "src/vol.nii", bytes.NewReader(nii), int64(len(nii)), "application/octet-stream")
	if err != nil {
		t.Fatalf("put source: %v", err)
	}
	ds := &dbmodel.Dataset{Name: "ct-ds", Modality: dbmodel.ModalityVolume, DataSource: dbmodel.DataSourcePublicDeidentified}
	if err := db.DB.Create(ds).Error; err != nil {
		t.Fatalf("dataset: %v", err)
	}
	asset := &dbmodel.Asset{
		DatasetID: ds.ID, Modality: dbmodel.ModalityVolume, StorageURI: put.StorageURI,
		SHA256: strings.Repeat("cd", 32), QCStatus: dbmodel.QCStatusPassed,
		PreprocessStatus: dbmodel.PreprocessPending,
	}
	if err := db.DB.Create(asset).Error; err != nil {
		t.Fatalf("asset: %v", err)
	}

	w := NewMediaWorker(DefaultMediaWorkerConfig(), db, store, MediaTools{})
	if err := w.deriveVolume(ctx, asset); err != nil {
		t.Fatalf("deriveVolume: %v", err)
	}

	// ---- volume_meta ----
	var metaURI string
	db.DB.Raw(`SELECT storage_uri FROM asset_derivatives WHERE asset_id = ? AND kind = ?`,
		asset.ID, dbmodel.DerivativeVolumeMeta).Scan(&metaURI)
	if metaURI == "" {
		t.Fatal("volume_meta derivative row missing")
	}
	mrc, err := store.Get(ctx, metaURI)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	metaBytes := readAll(t, mrc)
	var meta VolumeMeta
	mustJSON(t, metaBytes, &meta)
	if meta.Dims != [3]int{2, 2, 2} {
		t.Errorf("meta dims = %v", meta.Dims)
	}
	if meta.Modality != "ct" {
		t.Errorf("meta modality = %q want ct", meta.Modality)
	}
	if math.Abs(meta.Spacing[2]-3.0) > 1e-5 {
		t.Errorf("meta spacing z = %v want 3.0", meta.Spacing[2])
	}

	// ---- volume_slices: prefix row + PNG16 pixel→HU round-trip ----
	var sliceURI string
	var sliceBytes int64
	db.DB.Raw(`SELECT storage_uri, size_bytes FROM asset_derivatives WHERE asset_id = ? AND kind = ?`,
		asset.ID, dbmodel.DerivativeVolumeSlices).Row().Scan(&sliceURI, &sliceBytes)
	if !strings.HasSuffix(sliceURI, "/") {
		t.Errorf("volume_slices storage_uri must be a prefix (trailing /), got %q", sliceURI)
	}
	if sliceBytes <= 0 {
		t.Errorf("volume_slices size_bytes = %d, want >0", sliceBytes)
	}

	// Decode slice k=1 (holds the spike at pixel (0,0)) and verify the HU map.
	src1, err := store.Get(ctx, sliceURI+"0001.png")
	if err != nil {
		t.Fatalf("get slice 1: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(readAll(t, src1)))
	if err != nil {
		t.Fatalf("decode slice png: %v", err)
	}
	g16, ok := img.(*image.Gray16)
	if !ok {
		t.Fatalf("slice is %T, want *image.Gray16 (16-bit fidelity, C0.5)", img)
	}
	// real = pixel*SliceSclSlope + SliceSclInter. Bone spike stored 2000 → +976 HU.
	realHU := func(x, y int) float64 {
		return float64(g16.Gray16At(x, y).Y)*meta.SliceSclSlope + meta.SliceSclInter
	}
	if got := realHU(0, 0); math.Abs(got-976) > 1e-6 {
		t.Errorf("spike voxel HU = %v want 976 (2000 stored - 1024)", got)
	}
	if got := realHU(1, 1); math.Abs(got-(-1524)) > 1e-6 {
		t.Errorf("base voxel HU = %v want -1524 (-500 stored - 1024)", got)
	}

	// Slice k=0 is all base (no spike): pixel (0,0) must be -1524, proving the
	// slice index maps to the right z-block (not silently reading slice 0 twice).
	src0, _ := store.Get(ctx, sliceURI+"0000.png")
	img0, _ := png.Decode(bytes.NewReader(readAll(t, src0)))
	g0 := img0.(*image.Gray16)
	if got := float64(g0.Gray16At(0, 0).Y)*meta.SliceSclSlope + meta.SliceSclInter; math.Abs(got-(-1524)) > 1e-6 {
		t.Errorf("slice0 (0,0) HU = %v want -1524 (spike must be in slice1 only)", got)
	}
}

// 坏文件必须终止式失败(不是无限重试,更不是静默落一堆空派生)。
func TestDeriveVolume_RejectsGarbage(t *testing.T) {
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	store, _ := NewLocalObjectStore(t.TempDir())
	ctx := context.Background()

	put, _ := store.PutAt(ctx, "src/bad.nii", bytes.NewReader([]byte("not a nifti at all, just text")), 30, "application/octet-stream")
	ds := &dbmodel.Dataset{Name: "bad", Modality: dbmodel.ModalityVolume, DataSource: dbmodel.DataSourcePublicDeidentified}
	db.DB.Create(ds)
	asset := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityVolume, StorageURI: put.StorageURI, SHA256: strings.Repeat("ef", 32), QCStatus: dbmodel.QCStatusPassed}
	db.DB.Create(asset)

	w := NewMediaWorker(DefaultMediaWorkerConfig(), db, store, MediaTools{})
	err := w.deriveVolume(ctx, asset)
	var term *terminalPreprocessError
	if err == nil || !errors.As(err, &term) {
		t.Fatalf("garbage volume must fail terminally, got %v", err)
	}
	var n int64
	db.DB.Raw(`SELECT COUNT(*) FROM asset_derivatives WHERE asset_id = ?`, asset.ID).Scan(&n)
	if n != 0 {
		t.Errorf("failed derive must leave no derivative rows, got %d", n)
	}
}

// readAll drains and CLOSES the reader — leaving the object-store handle open
// blocks t.TempDir() cleanup on Windows (unlinkat "used by another process").
// schemeStore 包一层真 store,但 URIForKey 返回自定 scheme。用来证明 deriveVolume 的
// volume_slices storage_uri 走 store.URIForKey 反推(#1),而非硬编码 local://——后者在
// MinIO 生产下会让切片端点/SAM2 全读不到。PutAt/Get 仍走底层(源读写正常),只有前缀
// URI 被打上 fake://,断言它出现在库里即证明没走硬编码分支。
type schemeStore struct {
	ObjectStore
	scheme string
}

func (s schemeStore) URIForKey(key string) string { return s.scheme + "://" + key }

func TestDeriveVolume_UsesStoreSchemeForPrefix(t *testing.T) {
	db := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	local, err := NewLocalObjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	store := schemeStore{ObjectStore: local, scheme: "fake"}
	ctx := context.Background()

	aff := [3][4]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	nii := niftiBuilder{
		dims: [3]int{2, 2, 2}, datatype: 4, bitpix: 16, pixdim: [3]float32{1, 1, 1},
		sform: &aff, sclSlope: 1, sclInter: -1024, voxels: int16Voxels(8, -500, 2000),
	}.build()
	put, err := store.PutAt(ctx, "src/vol2.nii", bytes.NewReader(nii), int64(len(nii)), "application/octet-stream")
	if err != nil {
		t.Fatalf("put source: %v", err)
	}
	ds := &dbmodel.Dataset{Name: "ct-scheme", Modality: dbmodel.ModalityVolume, DataSource: dbmodel.DataSourcePublicDeidentified}
	if err := db.DB.Create(ds).Error; err != nil {
		t.Fatal(err)
	}
	asset := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityVolume, StorageURI: put.StorageURI,
		SHA256: strings.Repeat("ce", 32), QCStatus: dbmodel.QCStatusPassed, PreprocessStatus: dbmodel.PreprocessPending}
	if err := db.DB.Create(asset).Error; err != nil {
		t.Fatal(err)
	}

	w := NewMediaWorker(DefaultMediaWorkerConfig(), db, store, MediaTools{})
	if err := w.deriveVolume(ctx, asset); err != nil {
		t.Fatalf("deriveVolume: %v", err)
	}

	var sliceURI string
	db.DB.Raw(`SELECT storage_uri FROM asset_derivatives WHERE asset_id = ? AND kind = ?`,
		asset.ID, dbmodel.DerivativeVolumeSlices).Scan(&sliceURI)
	if !strings.HasPrefix(sliceURI, "fake://") {
		t.Fatalf("volume_slices storage_uri = %q,应为 store.URIForKey 的 scheme(fake://),而非硬编码 local://（MinIO 生产会读不到切片,§5f 的 key≠URI 事故类）", sliceURI)
	}
}

func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf.Bytes()
}

func mustJSON(t *testing.T, b []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("json: %v", err)
	}
}
