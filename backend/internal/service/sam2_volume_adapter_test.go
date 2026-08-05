package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// C4.2 SAM2 体数据传播适配器。跑在真 Postgres 上（轨迹要真的落库），sidecar 用
// httptest 假替（这里测的是**适配器的接线**：取片→调用→把 RLE 回写成 voxel_mask
// 轨迹，而不是 SAM2 本身）。真模型的正确性由 103 上的端到端负责，与本测试无关。

func seedVolumeTask(t *testing.T) (*repository.DB, uint, uint) {
	t.Helper()
	repo := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	ctx := context.Background()
	ds := &dbmodel.Dataset{Name: "c42-fixture", Modality: dbmodel.ModalityVolume}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	asset := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityVolume, SHA256: strings.Repeat("ab", 32), QCStatus: dbmodel.QCStatusPassed}
	if err := repo.DB.Create(asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	task := &dbmodel.AnnotationTask{AssetID: asset.ID, DatasetID: ds.ID, State: dbmodel.TaskStateHumanPending}
	if err := repo.CreateAnnotationTask(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return repo, asset.ID, task.ID
}

// cannedSidecar returns a sidecar that echoes a fixed 2×2 block on three slices
// centred on prompt_z. Using a real COCO RLE (via our own encoder, which is
// pycocotools-locked) means the adapter's decode path is exercised for real.
func cannedSidecar(t *testing.T, promptZ int) *httptest.Server {
	t.Helper()
	// a 2×2 solid block, bbox-local
	sub := []byte{1, 1, 1, 1}
	rle, err := EncodeCOCORLE(sub, 2, 2, true)
	if err != nil {
		t.Fatalf("encode canned rle: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/propagate_volume" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req sam2VolReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := sam2VolResp{Model: "stub"}
		for _, z := range []int{promptZ - 1, promptZ, promptZ + 1} {
			out.Slices = append(out.Slices, sam2VolSlice{
				Z: z, Size: [2]int{2, 2}, Bbox: []int{5, 6, 2, 2}, Counts: rle.Counts, Score: 0.9,
			})
		}
		out.Count = len(out.Slices)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
}



func baseExtras(promptZ int) map[string]interface{} {
	return map[string]interface{}{
		"points":          [][]float64{{10, 12, 1}},
		"prompt_z":        promptZ,
		"slice_prefix":    "derived/x/volume_slices/v1/",
		"nz":              64,
		"label":           "肝",
		"spacing":         []float64{0.7, 0.7, 3.0},
		"window_width":    float64(400),
		"window_center":   float64(40),
		"slice_scl_slope": float64(1),
		"slice_scl_inter": float64(-1024),
	}
}

func newVolAdapter(t *testing.T, repo *repository.DB, endpoint string) *SAM2VolumeAdapter {
	t.Helper()
	return NewSAM2VolumeAdapter(SAM2VolumeAdapterConfig{
		Endpoint: endpoint, MaxSlices: 8, MaxQueue: 2, SystemUserID: 0,
		Reader:  func(_ context.Context, _ string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("fake-png-bytes")), nil
		},
		DB:      repo, Payload: repo,
	})
}

func TestSAM2Volume_WritesVoxelMaskTrack(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	srv := cannedSidecar(t, 20)
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)

	resp, err := a.Invoke(context.Background(), CapabilityRequest{
		TaskID: taskID, AssetID: assetID, CapabilityType: CapabilitySAM2Volume,
		Extras: baseExtras(20),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Status != "success" {
		t.Fatalf("status=%s err=%s", resp.Status, resp.Error)
	}

	tracks, err := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 {
		t.Fatalf("want 1 track, got %d", len(tracks))
	}
	tr := tracks[0]
	if tr.Kind != paymodel.TrackKindVoxelMask {
		t.Errorf("kind=%q, want voxel_mask（必须与手画/导出同格式，否则分层就断了）", tr.Kind)
	}
	if tr.Source != paymodel.TrackSourceAI {
		t.Errorf("source=%q, want ai", tr.Source)
	}
	if tr.Label != "肝" {
		t.Errorf("label=%q, want 肝", tr.Label)
	}
	if len(tr.Keyframes) != 3 {
		t.Fatalf("want 3 keyframes (z=19,20,21), got %d", len(tr.Keyframes))
	}
	// 关键帧按 z 升序，且落在 prompt_z 周围。
	if tr.Keyframes[0].Frame != 19 || tr.Keyframes[2].Frame != 21 {
		t.Errorf("keyframe z = %d..%d, want 19..21", tr.Keyframes[0].Frame, tr.Keyframes[2].Frame)
	}
	// RLE 必须真能解码成 2×2 全 1（回写没有损坏几何）。
	kf := tr.Keyframes[1]
	if kf.RLE == nil {
		t.Fatal("keyframe 无 RLE")
	}
	sub, h, wd, err := DecodeCOCORLE(kf.RLE)
	if err != nil {
		t.Fatalf("decode kf rle: %v", err)
	}
	if h != 2 || wd != 2 {
		t.Errorf("rle size = %dx%d, want 2x2", h, wd)
	}
	on := 0
	for _, v := range sub {
		if v != 0 {
			on++
		}
	}
	if on != 4 {
		t.Errorf("解出 %d 个前景体素，want 4", on)
	}
	// bbox 原样带过来（RLE 是 bbox-local，丢了 bbox 掩膜会落到原点）。
	if len(kf.Bbox) != 4 || kf.Bbox[0] != 5 || kf.Bbox[1] != 6 {
		t.Errorf("bbox=%v, want [5 6 2 2]", kf.Bbox)
	}
}

func TestSAM2Volume_RejectsNoPoints(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	srv := cannedSidecar(t, 20)
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)
	ex := baseExtras(20)
	delete(ex, "points")
	_, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: taskID, AssetID: assetID, Extras: ex})
	if err == nil {
		t.Fatal("无点提示必须报错")
	}
}

func TestSAM2Volume_SidecarErrorSurfaces(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cuda oom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)
	resp, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: taskID, AssetID: assetID, Extras: baseExtras(20)})
	if err == nil {
		t.Fatal("sidecar 500 必须冒泡")
	}
	if !strings.Contains(resp.Error, "cuda oom") {
		t.Errorf("错误里应带 sidecar 原文，得到: %s", resp.Error)
	}
	// 失败时不该留下半条轨迹。
	tracks, _ := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
	if len(tracks) != 0 {
		t.Errorf("失败后不该有轨迹，得到 %d", len(tracks))
	}
}

func TestSAM2Volume_RejectsSliceOutsideVolume(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	sub := []byte{1}
	rle, _ := EncodeCOCORLE(sub, 1, 1, true)
	// **混入合法切片**：只回一个越界 z 的话，"跳过越界" 的错误实现会落到
	// "没有产出掩膜" 这条别的错误上，测试就分不清是不是真的拒绝了越界。
	// 回 z=19,20,999：真拒绝会因 999 报错；若实现改成 break 跳过，就会保留
	// 19/20 成功入库——那才暴露出"静默落错层"。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := sam2VolResp{Slices: []sam2VolSlice{
			{Z: 19, Size: [2]int{1, 1}, Bbox: []int{0, 0, 1, 1}, Counts: rle.Counts},
			{Z: 20, Size: [2]int{1, 1}, Bbox: []int{0, 0, 1, 1}, Counts: rle.Counts},
			{Z: 999, Size: [2]int{1, 1}, Bbox: []int{0, 0, 1, 1}, Counts: rle.Counts},
		}}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)
	_, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: taskID, AssetID: assetID, Extras: baseExtras(20)})
	if err == nil {
		t.Fatal("sidecar 返回越界 z 必须报错，不能静默落到错误层")
	}
	// 拒绝必须是**整条拒绝**：不能把合法的 19/20 落库、只丢掉越界的那片。
	tracks, _ := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
	if len(tracks) != 0 {
		t.Errorf("越界应整条拒绝，却落了 %d 条轨迹（合法切片被静默保留了？）", len(tracks))
	}
}

// #14 越界 prompt_z:修复前 z1-z0+1 变负 → make([]string, 0, 负数) panic(请求 500,
// worker 无 recover 时更会带走进程)。必须先夹住报错。
func TestSAM2Volume_RejectsOutOfRangePromptZ(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	srv := cannedSidecar(t, 20)
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)
	ex := baseExtras(20)
	ex["prompt_z"] = -100 // nz=64,负 prompt_z
	resp, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: taskID, AssetID: assetID, Extras: ex})
	if err == nil {
		t.Fatal("越界 prompt_z 必须报错,而不是 make 负容量 panic")
	}
	if !strings.Contains(resp.Error, "prompt_z") {
		t.Errorf("错误应指明 prompt_z 越界, got %s", resp.Error)
	}
}

// #13 SAM2 直接 InsertTrack、绕过 validate:一片 RLE 尺寸与 bbox 不符时必须**整份拒绝**,
// 不能只落合法那片(复核员会以为传播完整,却缺格/落错)。变异:把逐片 return 改成 continue,
// 本测试红(会落库)。
func TestSAM2Volume_RejectsInconsistentSlice(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	rle, _ := EncodeCOCORLE([]byte{1, 1, 1, 1}, 2, 2, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := sam2VolResp{Slices: []sam2VolSlice{
			{Z: 20, Size: [2]int{2, 2}, Bbox: []int{5, 6, 2, 2}, Counts: rle.Counts},
			{Z: 21, Size: [2]int{2, 2}, Bbox: []int{5, 6, 9, 9}, Counts: rle.Counts}, // size≠bbox
		}}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)
	_, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: taskID, AssetID: assetID, Extras: baseExtras(20)})
	if err == nil {
		t.Fatal("含尺寸与 bbox 不符的片必须整份拒绝")
	}
	tracks, _ := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
	if len(tracks) != 0 {
		t.Errorf("整份拒绝时不该落库, got %d", len(tracks))
	}
}

func TestSAM2Volume_TrackNumberFollowsExisting(t *testing.T) {
	repo, assetID, taskID := seedVolumeTask(t)
	asset, _ := repo.FindAssetByID(context.Background(), assetID)
	// 先放一条已有轨迹（号 5），AI 段应拿 6，而不是撞 1。
	pre := &paymodel.Track{TaskID: taskID, AssetID: assetID, DatasetID: asset.DatasetID, TrackID: 5, Label: "existing",
		Kind: paymodel.TrackKindVoxelMask, Source: paymodel.TrackSourceHuman,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{0, 0, 1, 1}}}}
	if err := repo.InsertTrack(context.Background(), pre); err != nil {
		t.Fatal(err)
	}
	srv := cannedSidecar(t, 20)
	defer srv.Close()
	a := newVolAdapter(t, repo, srv.URL)
	resp, err := a.Invoke(context.Background(), CapabilityRequest{TaskID: taskID, AssetID: assetID, Extras: baseExtras(20)})
	if err != nil {
		t.Fatal(err)
	}
	no, _ := resp.Raw.(map[string]interface{})["track_no"].(int)
	if no != 6 {
		t.Errorf("AI 段 track_no = %d, want 6（应接在已有轨迹之后，不撞号）", no)
	}
}
