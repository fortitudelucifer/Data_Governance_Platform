package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"time"

	"gorm.io/gorm"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// Cell-detect 失败分类 —— 让 handler 把 sidecar/DB 故障映射成对的 HTTP 码
// (502/504/500)而不是一律 400,调用方才知道该重试还是该改输入(C2.2 review #19)。
var (
	ErrCellDetectUpstream    = errors.New("cell detect upstream failure")       // sidecar 不可达/非200/响应坏或过大 → 502
	ErrCellDetectTimeout     = errors.New("cell detect timeout")                // 调 sidecar 超时 → 504
	ErrCellDetectBadResponse = errors.New("cell detect returned invalid cells") // 结构合法但几何非法(模型输出坏)→ 502
	ErrCellDetectStorage     = errors.New("cell detect storage failure")        // 落库失败(事务/插入)→ 500
)

// maxCellRespBytes 有界读取 sidecar 响应:io.ReadAll 无界 = 一个坏 sidecar 就能撑爆内存
// (#17)。上限给足合法量(上万细胞的 JSON)再留富余。
const maxCellRespBytes = 32 << 20

// CellDetectAdapter implements CapabilityAdapter for seg.cells_detect via a
// cell-detection sidecar (病理 C2.2). One ROI on a WSI → the detector returns all
// cells in that region → written as a **cells track**: one keyframe (frame=0)
// whose Instances[] holds the whole ROI's cells (C-Q4: one ROI = one track = one
// keyframe, never one-cell-one-track). Each instance = bbox + bbox-local COCO RLE
// + class + score — byte-for-byte the shape a human corrects (C2.3) and export
// reads (C2.4), so the sampling-review / edit / export stages work on one format.
//
// # Sidecar contract (http://<host>:PORT)
//
//	POST /detect_cells
//	  { roi: [x, y, w, h],   // level-0 px region to detect in
//	    mpp,                  // µm/px (size filtering; 0 = unknown)
//	    max_cells }           // hard cap the sidecar must respect
//	→ { cells: [{ bbox:[x,y,w,h],   // level-0 px (absolute, the RLE's anchor)
//	              size:[h,w], counts:"<coco rle>",  // bbox-local COCO compressed RLE
//	              class, score }],
//	    count, model }
//
// The RLE `counts` is bbox-local COCO compressed RLE — the exact encoding locked
// against pycocotools in C3.3a. A Python sidecar using pycocotools.mask.encode
// emits bytes our DecodeCOCORLE reads, so the cross-language RLE contract is
// already pinned (same guarantee voxel_mask relies on).
//
// # Pixels: sent for the real model, ignored by the deterministic stub
//
// The real detector needs the ROI's pixels; extracting + stitching the WSI tiles
// for an arbitrary ROI is a deploy-time addition (marked below). The local CPU
// stub is deterministic on the ROI bbox alone (no pixels), so the whole chain —
// trigger → gate → adapter → 落库 → 抽样复核 → 导出 — is end-to-end testable
// without the GPU box (same approach as C4.2's sam2-volume-stub).
type CellDetectAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	provider   string
	systemUser uint

	timeout time.Duration
	hc      *http.Client
	db      *repository.DB
	payload *repository.DB

	// Cell detection on a large ROI is heavy; serialise + bound the queue (B2.8).
	gate *GPUGate
}

// CellDetectAdapterConfig wires the adapter.
type CellDetectAdapterConfig struct {
	Endpoint     string
	APIKey       string
	ProviderName string
	MaxQueue     int
	SystemUserID uint
	Timeout      time.Duration
	DB           *repository.DB
	Payload      *repository.DB
}

// NewCellDetectAdapter builds the adapter.
func NewCellDetectAdapter(cfg CellDetectAdapterConfig) *CellDetectAdapter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 4
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "cell-detect"
	}
	return &CellDetectAdapter{
		capability: CapabilityCellDetect,
		endpoint:   cfg.Endpoint,
		apiKey:     cfg.APIKey,
		provider:   cfg.ProviderName,
		systemUser: cfg.SystemUserID,
		timeout:    cfg.Timeout,
		hc:         &http.Client{Timeout: cfg.Timeout},
		db:         cfg.DB,
		payload:    cfg.Payload,
		gate:       NewGPUGate(1, cfg.MaxQueue),
	}
}

func (a *CellDetectAdapter) Capability() string        { return a.capability }
func (a *CellDetectAdapter) QueueStats() GPUQueueStats { return a.gate.Stats() }

func (a *CellDetectAdapter) Configured() bool {
	return a.endpoint != "" && a.db != nil && a.payload != nil
}

type cellDetectReq struct {
	ROI      []float64 `json:"roi"`
	MPP      float64   `json:"mpp"`
	MaxCells int       `json:"max_cells"`
}

type cellDetectCell struct {
	Bbox   []float64 `json:"bbox"`
	Size   [2]int    `json:"size"`
	Counts string    `json:"counts"`
	Class  string    `json:"class"`
	Score  float64   `json:"score"`
}

type cellDetectResp struct {
	Cells []cellDetectCell `json:"cells"`
	Count int              `json:"count"`
	Model string           `json:"model"`
}

// Invoke runs cell detection on one ROI and writes a cells track.
func (a *CellDetectAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   a.provider,
			ModelID:        "cell-detect",
			CapabilityType: a.capability,
			EndpointMode:   EndpointModeAdapter,
		},
	}
	if !a.Configured() {
		resp.Error = "cell detect adapter not fully configured"
		return resp, errors.New(resp.Error)
	}
	if err := a.gate.Acquire(ctx); err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	defer a.gate.Release()

	roi := extraFloatSlice(req.Extras["roi"])
	if len(roi) != 4 || roi[2] <= 0 || roi[3] <= 0 {
		resp.Error = "roi [x,y,w,h] required (level-0 像素,w/h>0)"
		return resp, errors.New(resp.Error)
	}
	mpp, _ := req.Extras["mpp"].(float64)
	label, _ := req.Extras["label"].(string)
	if label == "" {
		label = "AI 细胞"
	}

	// TODO(C2.2 real deploy): 真模型需要 ROI 像素 —— 从 WSI 按 roi 抽瓦片拼图后
	// 随请求带 image_b64。stub 不看像素,只按 roi bbox 确定性产核,不影响契约。

	started := time.Now()
	// max_cells = 上限 + 1:让 sidecar 对过大 ROI 恰好多返回 1 个,下面 len>上限 时
	// **拒绝**(告诉用户缩小 ROI),而不是让 sidecar 悄悄截断到上限、静默丢掉多出来的
	// 细胞(#6 静默丢弃)。正常 ROI(≤上限)全量返回、正常落库。
	reqBody, _ := json.Marshal(cellDetectReq{ROI: roi, MPP: mpp, MaxCells: MaxCellsPerTrack + 1})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/detect_cells", bytes.NewReader(reqBody))
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	res, err := a.hc.Do(httpReq)
	if err != nil {
		resp.Error = fmt.Sprintf("detect_cells call: %v", err)
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return resp, fmt.Errorf("%w: %v", ErrCellDetectTimeout, err)
		}
		return resp, fmt.Errorf("%w: %v", ErrCellDetectUpstream, err)
	}
	defer res.Body.Close()
	// 有界读取(#17):多读 1 字节以判定是否超限;超了当上游故障拒,而不是 OOM。
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxCellRespBytes+1))
	if err != nil {
		resp.Error = fmt.Sprintf("read detect_cells resp: %v", err)
		return resp, fmt.Errorf("%w: %v", ErrCellDetectUpstream, err)
	}
	if int64(len(raw)) > maxCellRespBytes {
		resp.Error = fmt.Sprintf("detect_cells 响应超过 %d MB 上限", maxCellRespBytes/1024/1024)
		return resp, fmt.Errorf("%w: response too large", ErrCellDetectUpstream)
	}
	if res.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		resp.Error = fmt.Sprintf("cell-detect %d: %s", res.StatusCode, msg)
		return resp, fmt.Errorf("%w: status %d", ErrCellDetectUpstream, res.StatusCode)
	}
	var pr cellDetectResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		resp.Error = fmt.Sprintf("decode detect_cells resp: %v", err)
		return resp, fmt.Errorf("%w: %v", ErrCellDetectUpstream, err)
	}

	// count 护栏:adapter 直接 InsertTrack,**绕过 TrackService.validate**,MaxCellsPerTrack
	// 必须在这里也拦一道——否则过大的 ROI 会把整片细胞塞进一条轨迹、锁死数天工作量。
	// client-actionable(缩小 ROI)→ 默认 400。
	if len(pr.Cells) > MaxCellsPerTrack {
		resp.Error = fmt.Sprintf("该 ROI 检出 %d 个细胞，超过单轨迹上限 %d：ROI 太大，请缩小 ROI 再检测（任务粒度是 ROI）", len(pr.Cells), MaxCellsPerTrack)
		return resp, errors.New(resp.Error)
	}

	// 逐实例**严格**校验(#15):AI 写入绕过 validate,坏实例只能在这里挡。fail-closed——
	// 任一实例几何非法(bbox 非有限/非正/越出 ROI、RLE 尺寸不符 bbox 或解不出)就**拒绝
	// 整份响应**,绝不静默丢弃个别坏实例后当成功(那会让复核员以为这份检测完整可信)。
	// 同时补上 4MB 几何总量护栏(count 少但单个 RLE 巨大也能堆出几十 MB 一条轨迹,#17)。
	insts := make([]paymodel.CellInstance, 0, len(pr.Cells))
	totalGeom := 0
	for i, c := range pr.Cells {
		if verr := validateDetectedCell(c, roi); verr != nil {
			resp.Error = fmt.Sprintf("第 %d 个细胞不合法：%v（整份检测已拒绝，请重试）", i, verr)
			return resp, fmt.Errorf("%w: cell %d: %v", ErrCellDetectBadResponse, i, verr)
		}
		totalGeom += len(c.Counts) + 4*8 // RLE 字节 + bbox(4×float64),与 validate 口径一致
		insts = append(insts, paymodel.CellInstance{
			Bbox:  []float64{c.Bbox[0], c.Bbox[1], c.Bbox[2], c.Bbox[3]},
			RLE:   &paymodel.MaskRLE{Size: c.Size, Counts: c.Counts},
			Class: c.Class,
			Score: c.Score,
		})
	}
	if len(insts) == 0 {
		resp.Error = "该 ROI 没有检出细胞"
		return resp, errors.New(resp.Error)
	}
	if totalGeom > MaxTrackGeomBytes {
		resp.Error = fmt.Sprintf("该 ROI 的细胞几何总量 %.1f MB 超过单轨迹上限 %d MB：请缩小 ROI 再检测",
			float64(totalGeom)/1024/1024, MaxTrackGeomBytes/1024/1024)
		return resp, errors.New(resp.Error)
	}

	asset, err := a.db.FindAssetByID(ctx, req.AssetID)
	if err != nil {
		resp.Error = fmt.Sprintf("load asset: %v", err)
		return resp, fmt.Errorf("%w: %v", ErrCellDetectStorage, err)
	}

	// 同 ROI 替换 + 落库放进**一个事务**(#18):归档旧、取号、插新要么全成、要么全不动。
	// 以前非事务且吞掉 list/deactivate/MaxTrackNumber 的错——归档成功但插入失败时旧结果
	// 永久消失、任务凭空少一份检测。
	roiKey := roiAttrKey(roi)
	modelName := pr.Model // 记 sidecar 实际报告的模型名/版本(而非硬编码"cell-detect"),供溯源(#14)
	if modelName == "" {
		modelName = "cell-detect"
	}
	var newTrack *paymodel.Track
	txErr := a.payload.DB.Transaction(func(tx *gorm.DB) error {
		txRepo := a.payload.WithTx(tx)
		actives, lerr := txRepo.ListActiveTracksByTask(ctx, req.TaskID, paymodel.TrackSourceAI, "")
		if lerr != nil {
			return fmt.Errorf("list active: %w", lerr)
		}
		for _, ot := range actives {
			if ot.Kind != paymodel.TrackKindCells {
				continue
			}
			if k, _ := ot.Attrs["roi"].(string); k == roiKey {
				if derr := txRepo.SetTrackActive(ctx, ot.ID, false, a.systemUser); derr != nil {
					return fmt.Errorf("archive old roi track: %w", derr)
				}
			}
		}
		base, berr := txRepo.MaxTrackNumber(ctx, req.TaskID)
		if berr != nil {
			return fmt.Errorf("max track number: %w", berr)
		}
		trackNo := base + 1
		newTrack = &paymodel.Track{
			TaskID:    req.TaskID,
			DatasetID: asset.DatasetID,
			AssetID:   req.AssetID,
			TrackID:   trackNo,
			Label:     label,
			Kind:      paymodel.TrackKindCells,
			Color:     trackColor(trackNo),
			Source:    paymodel.TrackSourceAI,
			Attrs: map[string]interface{}{
				"ai_model":  modelName,
				"ai_source": "cell_detect",
				"roi":       roiKey, // 用于重跑同 ROI 去重
			},
			Keyframes: []paymodel.Keyframe{{Frame: 0, TsMs: 0, Instances: insts, Source: paymodel.TrackSourceAI}},
		}
		return txRepo.InsertTrack(ctx, newTrack)
	})
	if txErr != nil {
		resp.Error = fmt.Sprintf("persist cells: %v", txErr)
		return resp, fmt.Errorf("%w: %v", ErrCellDetectStorage, txErr)
	}

	resp.Status = "success"
	resp.Raw = map[string]interface{}{
		"track_id":   newTrack.ID,
		"track_no":   newTrack.TrackID,
		"cells":      len(insts),
		"elapsed_ms": time.Since(started).Milliseconds(),
	}
	return resp, nil
}

// validateDetectedCell 严格校验 sidecar 返回的一个细胞(#15)。任何一项不过就让调用方
// 拒绝整份响应——坏几何静默落库后,复核员看到的是一份"看着完整"的检测,却在错的位置
// 或解不出掩膜。
func validateDetectedCell(c cellDetectCell, roi []float64) error {
	if len(c.Bbox) < 4 {
		return fmt.Errorf("bbox 需要 4 个数,有 %d", len(c.Bbox))
	}
	for i := 0; i < 4; i++ {
		if math.IsNaN(c.Bbox[i]) || math.IsInf(c.Bbox[i], 0) {
			return fmt.Errorf("bbox[%d] 非有限值", i)
		}
	}
	x, y, w, h := c.Bbox[0], c.Bbox[1], c.Bbox[2], c.Bbox[3]
	if w <= 0 || h <= 0 {
		return fmt.Errorf("bbox 宽/高非正 (%g×%g)", w, h)
	}
	// bbox 必须落在被检测的 ROI 内(留 1px 容差)。冒到 ROI 外 = 模型坐标错、或响应串了
	// 别的 ROI —— 这份检测不可信。
	const tol = 1.0
	rx, ry, rw, rh := roi[0], roi[1], roi[2], roi[3]
	if x < rx-tol || y < ry-tol || x+w > rx+rw+tol || y+h > ry+rh+tol {
		return fmt.Errorf("bbox [%g,%g,%g,%g] 越出 ROI [%g,%g,%g,%g]", x, y, w, h, rx, ry, rw, rh)
	}
	// RLE 是 bbox-local:尺寸(Size=[h,w])应≈bbox 的像素高宽,且必须能解码。
	if c.Size[0] <= 0 || c.Size[1] <= 0 || c.Counts == "" {
		return errors.New("RLE 尺寸/counts 缺失")
	}
	if d := c.Size[0] - int(math.Round(h)); d < -1 || d > 1 {
		return fmt.Errorf("RLE 高 %d 与 bbox 高 %g 不符(RLE 是 bbox-local)", c.Size[0], h)
	}
	if d := c.Size[1] - int(math.Round(w)); d < -1 || d > 1 {
		return fmt.Errorf("RLE 宽 %d 与 bbox 宽 %g 不符(RLE 是 bbox-local)", c.Size[1], w)
	}
	if _, _, _, derr := DecodeCOCORLE(&paymodel.MaskRLE{Size: c.Size, Counts: c.Counts}); derr != nil {
		return fmt.Errorf("RLE 解码失败: %w", derr)
	}
	return nil
}

// roiAttrKey turns an ROI bbox into a stable string key for re-run dedup.
func roiAttrKey(roi []float64) string {
	return fmt.Sprintf("%.0f_%.0f_%.0f_%.0f", roi[0], roi[1], roi[2], roi[3])
}
