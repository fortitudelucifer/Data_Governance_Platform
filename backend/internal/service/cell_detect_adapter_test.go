package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// C2.2 细胞检测 adapter 的语义锁(真 Postgres + httptest 假 sidecar)。锁三件事:
// ① 正常 ROI → 落一条 cells 轨迹(kind/instances/RLE/roi-attr 正确);
// ② AI 路径护栏:sidecar 返回 > MaxCellsPerTrack 时**拒绝、不落库**(不静默截断);
// ③ 重跑同一 ROI → 旧的归档、恰一条 active(不越堆越多)。
// AI 写入走 InsertTrack、绕过 TrackService.validate,所以护栏必须在 adapter 自证。

// cellStubServer 起一个假 sidecar:POST /detect_cells 返回 n 个细胞(每个一份合法
// bbox-local COCO RLE),并遵守 max_cells(与真 stub 一致,让护栏能被触发)。
func cellStubServer(t *testing.T, n int) *httptest.Server {
	t.Helper()
	// 2x2 全 1 掩膜,用我们自己的编码器(已对 pycocotools 锁,C3.3a)出 counts。
	rle, err := EncodeCOCORLE([]byte{1, 1, 1, 1}, 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cellDetectReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		count := n
		if req.MaxCells > 0 && count > req.MaxCells {
			count = req.MaxCells // 与真 sidecar 一样遵守 max_cells 上限
		}
		// 在**请求的 ROI 内**摆网格(每核 2×2,间距 3)——真实模型也只在 ROI 内产核,
		// 而 adapter 现在会拒绝越出 ROI 的实例(#15)。
		rx, ry := 0.0, 0.0
		if len(req.ROI) == 4 {
			rx, ry = req.ROI[0], req.ROI[1]
		}
		cells := make([]cellDetectCell, count)
		for i := range cells {
			cells[i] = cellDetectCell{
				Bbox:   []float64{rx + float64((i%40)*3) + 1, ry + float64((i/40)*3) + 1, 2, 2},
				Size:   rle.Size,
				Counts: rle.Counts,
				Class:  "tumor",
				Score:  0.9,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cellDetectResp{Cells: cells, Count: count, Model: "test-stub"})
	}))
}

func seedWSITaskForCells(t *testing.T) (*repository.DB, uint, uint) {
	t.Helper()
	repo := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}
	ctx := context.Background()
	ds := &dbmodel.Dataset{Name: "cells-fixture", Modality: dbmodel.ModalityWSI}
	if err := repo.DB.Create(ds).Error; err != nil {
		t.Fatalf("dataset: %v", err)
	}
	asset := &dbmodel.Asset{DatasetID: ds.ID, Modality: dbmodel.ModalityWSI, SHA256: strings.Repeat("cd", 32), QCStatus: dbmodel.QCStatusPassed}
	if err := repo.DB.Create(asset).Error; err != nil {
		t.Fatalf("asset: %v", err)
	}
	task := &dbmodel.AnnotationTask{AssetID: asset.ID, DatasetID: ds.ID, State: dbmodel.TaskStateHumanPending}
	if err := repo.CreateAnnotationTask(ctx, task); err != nil {
		t.Fatalf("task: %v", err)
	}
	return repo, task.ID, asset.ID
}

func detectReq(taskID, assetID uint, roi []float64) CapabilityRequest {
	return CapabilityRequest{
		TaskID: taskID, AssetID: assetID, CapabilityType: CapabilityCellDetect,
		Extras: map[string]interface{}{"roi": roi, "label": "AI 细胞"},
	}
}

func TestCellDetectAdapter_WritesCellsTrack(t *testing.T) {
	repo, taskID, assetID := seedWSITaskForCells(t)
	srv := cellStubServer(t, 42)
	defer srv.Close()
	a := NewCellDetectAdapter(CellDetectAdapterConfig{Endpoint: srv.URL, DB: repo, Payload: repo})

	resp, err := a.Invoke(context.Background(), detectReq(taskID, assetID, []float64{1000, 2000, 200, 200}))
	if err != nil || resp.Status != "success" {
		t.Fatalf("invoke: status=%s err=%v", resp.Status, err)
	}
	tracks, _ := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
	if len(tracks) != 1 {
		t.Fatalf("应落 1 条 cells 轨迹, got %d", len(tracks))
	}
	tr := tracks[0]
	if tr.Kind != "cells" || tr.Source != "ai" {
		t.Fatalf("kind/source 错: kind=%q source=%q", tr.Kind, tr.Source)
	}
	if len(tr.Keyframes) != 1 || len(tr.Keyframes[0].Instances) != 42 {
		t.Fatalf("应单关键帧装 42 个细胞, got kfs=%d insts=%d", len(tr.Keyframes), len(tr.Keyframes[0].Instances))
	}
	inst := tr.Keyframes[0].Instances[0]
	if inst.RLE == nil || inst.RLE.Counts == "" || inst.Class != "tumor" {
		t.Fatalf("实例几何/类别丢失: %+v", inst)
	}
	if k, _ := tr.Attrs["roi"].(string); k != "1000_2000_200_200" {
		t.Fatalf("roi attr 应记 ROI 键(供重跑去重), got %q", k)
	}
}

func TestCellDetectAdapter_GuardrailRejectsOversizedROI(t *testing.T) {
	repo, taskID, assetID := seedWSITaskForCells(t)
	// sidecar 想返回远超上限的量;adapter 送 max_cells=上限+1 → sidecar 返回上限+1 →
	// adapter 检出 > 上限 → 拒绝(不静默截断到上限)。
	srv := cellStubServer(t, MaxCellsPerTrack+500)
	defer srv.Close()
	a := NewCellDetectAdapter(CellDetectAdapterConfig{Endpoint: srv.URL, DB: repo, Payload: repo})

	resp, err := a.Invoke(context.Background(), detectReq(taskID, assetID, []float64{0, 0, 9000, 9000}))
	if err == nil || resp.Status == "success" {
		t.Fatal("过大 ROI 必须被拒(超单轨迹细胞数上限)")
	}
	if !strings.Contains(resp.Error, "ROI") {
		t.Fatalf("拒绝文案应指路缩小 ROI, got: %s", resp.Error)
	}
	tracks, _ := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
	if len(tracks) != 0 {
		t.Fatalf("被护栏拒绝时不该落库任何轨迹, got %d", len(tracks))
	}
}

func TestCellDetectAdapter_RerunSameROIDedups(t *testing.T) {
	repo, taskID, assetID := seedWSITaskForCells(t)
	srv := cellStubServer(t, 10)
	defer srv.Close()
	a := NewCellDetectAdapter(CellDetectAdapterConfig{Endpoint: srv.URL, DB: repo, Payload: repo})
	ctx := context.Background()
	roi := []float64{1000, 2000, 100, 100}

	if _, err := a.Invoke(ctx, detectReq(taskID, assetID, roi)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := a.Invoke(ctx, detectReq(taskID, assetID, roi)); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	active, _ := repo.ListActiveTracksByTask(ctx, taskID, "", "")
	if len(active) != 1 {
		t.Fatalf("重跑同一 ROI 后应恰 1 条 active cells 轨迹(旧的归档), got %d", len(active))
	}
	// 不同 ROI 应各自独立、不互相归档。
	if _, err := a.Invoke(ctx, detectReq(taskID, assetID, []float64{5000, 5000, 100, 100})); err != nil {
		t.Fatalf("other roi: %v", err)
	}
	active, _ = repo.ListActiveTracksByTask(ctx, taskID, "", "")
	if len(active) != 2 {
		t.Fatalf("不同 ROI 的 cells 轨迹应共存, got %d active", len(active))
	}
}

// validCell 造一个落在 ROI 内、几何自洽的合法细胞(2×2 全 1 掩膜)。
func validCell(t *testing.T, x, y float64) cellDetectCell {
	t.Helper()
	rle, err := EncodeCOCORLE([]byte{1, 1, 1, 1}, 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	return cellDetectCell{Bbox: []float64{x, y, 2, 2}, Size: rle.Size, Counts: rle.Counts, Class: "tumor", Score: 0.9}
}

func cellStubReturning(t *testing.T, cells []cellDetectCell) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(cellDetectResp{Cells: cells, Count: len(cells), Model: "test-stub"})
	}))
}

// #15:响应里但凡有一个几何非法的实例,就**拒绝整份**(不静默丢那个后当成功)——
// 否则复核员看到的是一份"看着完整"、实则缺格/错位的检测。每个子例都在一份"一好一坏"
// 的响应里放一种坏几何。变异:把 Invoke 里校验失败的 return 改成 continue,本测试必红(会落库)。
func TestCellDetectAdapter_RejectsCorruptCell(t *testing.T) {
	roi := []float64{1000, 2000, 200, 200}
	good := validCell(t, 1000, 2000)
	cases := map[string]cellDetectCell{
		"bbox 越出 ROI":   {Bbox: []float64{5000, 2000, 2, 2}, Size: [2]int{2, 2}, Counts: good.Counts},
		"RLE 尺寸不符 bbox": {Bbox: []float64{1010, 2000, 2, 2}, Size: [2]int{9, 9}, Counts: good.Counts},
		"bbox 宽高非正":     {Bbox: []float64{1010, 2000, 0, 2}, Size: [2]int{2, 2}, Counts: good.Counts},
		"RLE counts 缺失": {Bbox: []float64{1010, 2000, 2, 2}, Size: [2]int{2, 2}, Counts: ""},
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			repo, taskID, assetID := seedWSITaskForCells(t)
			srv := cellStubReturning(t, []cellDetectCell{good, bad}) // 一好一坏
			defer srv.Close()
			a := NewCellDetectAdapter(CellDetectAdapterConfig{Endpoint: srv.URL, DB: repo, Payload: repo})

			resp, err := a.Invoke(context.Background(), detectReq(taskID, assetID, roi))
			if err == nil || resp.Status == "success" {
				t.Fatal("含非法实例的响应必须整份拒绝,不能静默丢弃个别后当成功")
			}
			if !errors.Is(err, ErrCellDetectBadResponse) {
				t.Fatalf("应归类为 BadResponse(→502), got %v", err)
			}
			tracks, _ := repo.ListActiveTracksByTask(context.Background(), taskID, "", "")
			if len(tracks) != 0 {
				t.Fatalf("被拒时不该落库任何轨迹, got %d", len(tracks))
			}
		})
	}
}

// #19:sidecar/上游故障必须归类(502/504)而不是一律 400,否则调用方不会重试。
func TestCellDetectAdapter_UpstreamErrorTyped(t *testing.T) {
	repo, taskID, assetID := seedWSITaskForCells(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	a := NewCellDetectAdapter(CellDetectAdapterConfig{Endpoint: srv.URL, DB: repo, Payload: repo})
	_, err := a.Invoke(context.Background(), detectReq(taskID, assetID, []float64{1000, 2000, 200, 200}))
	if !errors.Is(err, ErrCellDetectUpstream) {
		t.Fatalf("sidecar 500 应归类为 Upstream(→502,不是 400), got %v", err)
	}
}
