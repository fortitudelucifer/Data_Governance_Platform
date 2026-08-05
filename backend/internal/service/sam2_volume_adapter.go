package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// maxSAM2RespBytes 有界读取 sam2-volume 响应(#13):坏/巨响应不该撑爆内存。
const maxSAM2RespBytes = 32 << 20

// SAM2VolumeAdapter implements CapabilityAdapter for seg.sam2_volume via a
// sam2-volume sidecar (C4.2). One point prompt on one axial slice → SAM2 treats
// the volume as a sequence of 2D slices along z and propagates the object →
// per-slice bbox-local RLE → written as a **voxel_mask track**, byte-for-byte
// the same shape C3.3 hand-drawing and C3.4 export already use.
//
// # Why the result reuses voxel_mask exactly
//
// The whole point of doing C4.2 before C4.1 (user's call, 2026-07-21) is to keep
// the annotation stack layered: AI pre-labelling produces the *same* geometry a
// human draws, so every downstream stage — the segment panel, brush editing,
// undo, save, NIfTI export, Slicer verification — works unchanged. If the AI
// output needed its own storage/edit/export path, we'd have two of everything
// and they would drift.
//
// # Sidecar contract (http://<host>:PORT)
//
//	POST /propagate_volume
//	  { slices_b64: ["<png16 base64>", ...],   // absolute z = z0 + index
//	    z0, prompt_z,                           // absolute z indices
//	    points: [[u, v, label], ...],           // voxel (i,j) in the axial plane
//	    spacing: [sx, sy, sz],
//	    slice_scl_slope, slice_scl_inter,       // png pixel → real value (HU)
//	    window: { width, center },              // display window for 8-bit render
//	    max_slices }
//	→ { slices: [{ z, size:[h,w], bbox:[x,y,w,h], counts:"<coco rle>", score }],
//	    count, model }
//
// The RLE `counts` is COCO compressed RLE, bbox-local — the exact encoding
// locked against pycocotools in C3.3a. A Python sidecar using
// pycocotools.mask.encode produces bytes our DecodeCOCORLE reads, so the
// cross-language contract is already pinned; the sidecar cannot silently emit
// an RLE dialect we can't read.
//
// # Slices are fetched, not received
//
// Like the video adapter transcodes so the server sees the annotator's pixels,
// this adapter reads the **volume_slices PNG16 derivative** for a bounded z-range
// around the prompt (±max_slices) and base64s them. Bounding the range keeps the
// payload small — sending all 215 slices would be ~11 MB per click.
type SAM2VolumeAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	provider   string
	maxSlices  int
	systemUser uint

	timeout time.Duration
	hc      *http.Client
	reader  AssetReader
	db      *repository.DB
	payload *repository.DB

	// SAM2 keeps the whole sequence's image features in VRAM; two concurrent
	// propagations can OOM the card. Serialise + bound the waiting room (B2.8).
	gate *GPUGate
}

// SAM2VolumeAdapterConfig wires the adapter.
type SAM2VolumeAdapterConfig struct {
	Endpoint     string
	APIKey       string
	ProviderName string
	MaxSlices    int
	MaxQueue     int
	SystemUserID uint
	Timeout      time.Duration
	Reader       AssetReader
	DB           *repository.DB
	Payload      *repository.DB
}

// NewSAM2VolumeAdapter builds the adapter.
func NewSAM2VolumeAdapter(cfg SAM2VolumeAdapterConfig) *SAM2VolumeAdapter {
	if cfg.MaxSlices <= 0 {
		cfg.MaxSlices = 32
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 4
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "sam2-volume"
	}
	return &SAM2VolumeAdapter{
		capability: CapabilitySAM2Volume,
		endpoint:   cfg.Endpoint,
		apiKey:     cfg.APIKey,
		provider:   cfg.ProviderName,
		maxSlices:  cfg.MaxSlices,
		systemUser: cfg.SystemUserID,
		timeout:    cfg.Timeout,
		hc:         &http.Client{Timeout: cfg.Timeout},
		reader:     cfg.Reader,
		db:         cfg.DB,
		payload:    cfg.Payload,
		gate:       NewGPUGate(1, cfg.MaxQueue),
	}
}

func (a *SAM2VolumeAdapter) Capability() string       { return a.capability }
func (a *SAM2VolumeAdapter) QueueStats() GPUQueueStats { return a.gate.Stats() }

func (a *SAM2VolumeAdapter) Configured() bool {
	return a.endpoint != "" && a.reader != nil && a.db != nil && a.payload != nil
}

type sam2VolReq struct {
	SlicesB64     []string    `json:"slices_b64"`
	Z0            int         `json:"z0"`
	PromptZ       int         `json:"prompt_z"`
	Points        [][]float64 `json:"points"`
	Spacing       []float64   `json:"spacing"`
	SliceSclSlope float64     `json:"slice_scl_slope"`
	SliceSclInter float64     `json:"slice_scl_inter"`
	Window        sam2VolWin  `json:"window"`
	MaxSlices     int         `json:"max_slices"`
}

type sam2VolWin struct {
	Width  float64 `json:"width"`
	Center float64 `json:"center"`
}

type sam2VolSlice struct {
	Z      int     `json:"z"`
	Size   [2]int  `json:"size"`
	Bbox   []int   `json:"bbox"`
	Counts string  `json:"counts"`
	Score  float64 `json:"score"`
}

type sam2VolResp struct {
	Slices []sam2VolSlice `json:"slices"`
	Count  int            `json:"count"`
	Model  string         `json:"model"`
}

// Invoke runs one point-prompt propagation and writes a voxel_mask track.
func (a *SAM2VolumeAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   a.provider,
			ModelID:        "sam2.1_hiera_base_plus",
			CapabilityType: a.capability,
			EndpointMode:   EndpointModeAdapter,
		},
	}
	if !a.Configured() {
		resp.Error = "sam2 volume adapter not fully configured"
		return resp, errors.New(resp.Error)
	}
	if err := a.gate.Acquire(ctx); err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	defer a.gate.Release()

	points := extractPoints(req.Extras)
	if len(points) == 0 {
		resp.Error = "no point prompt provided"
		return resp, errors.New(resp.Error)
	}
	for i, p := range points { // normalise to [u,v,label]
		if len(p) == 2 {
			points[i] = []float64{p[0], p[1], 1}
		}
	}
	// #14 prompt 点必须有限,否则送进 sidecar / 几何计算是垃圾。
	for _, p := range points {
		for _, v := range p {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				resp.Error = "prompt 点包含非有限坐标"
				return resp, errors.New(resp.Error)
			}
		}
	}
	promptZ, _ := extraInt(req.Extras["prompt_z"])
	slicePrefix, _ := req.Extras["slice_prefix"].(string)
	if slicePrefix == "" {
		resp.Error = "slice_prefix missing (volume_slices derivative not ready?)"
		return resp, errors.New(resp.Error)
	}
	nz, _ := extraInt(req.Extras["nz"])
	if nz <= 0 {
		resp.Error = "nz missing"
		return resp, errors.New(resp.Error)
	}
	// #14 越界 prompt_z 会让下面 z1-z0+1 变负 → make([]string, 0, 负数) panic(整个请求 500,
	// 更糟是 worker 无 recover 时会带走进程)。进 GPU gate 前先夹住。
	if promptZ < 0 || promptZ >= nz {
		resp.Error = fmt.Sprintf("prompt_z=%d 越界 [0,%d)", promptZ, nz)
		return resp, errors.New(resp.Error)
	}
	label, _ := req.Extras["label"].(string)
	if label == "" {
		label = "AI 分割"
	}
	spacing := extraFloatSlice(req.Extras["spacing"])
	win := sam2VolWin{}
	if w, ok := req.Extras["window_width"].(float64); ok {
		win.Width = w
	}
	if c, ok := req.Extras["window_center"].(float64); ok {
		win.Center = c
	}
	sclSlope, _ := req.Extras["slice_scl_slope"].(float64)
	sclInter, _ := req.Extras["slice_scl_inter"].(float64)

	// Bounded z-range around the prompt: [z0, z1] clamped to the volume.
	z0 := promptZ - a.maxSlices
	if z0 < 0 {
		z0 = 0
	}
	z1 := promptZ + a.maxSlices
	if z1 >= nz {
		z1 = nz - 1
	}

	slices := make([]string, 0, z1-z0+1)
	for z := z0; z <= z1; z++ {
		uri := slicePrefix + fmt.Sprintf("%04d.png", z)
		rc, err := a.reader(ctx, uri)
		if err != nil {
			resp.Error = fmt.Sprintf("read slice z=%d: %v", z, err)
			return resp, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			resp.Error = fmt.Sprintf("read slice bytes z=%d: %v", z, err)
			return resp, err
		}
		slices = append(slices, base64.StdEncoding.EncodeToString(b))
	}

	started := time.Now()
	reqBody, _ := json.Marshal(sam2VolReq{
		SlicesB64: slices, Z0: z0, PromptZ: promptZ, Points: points,
		Spacing: spacing, SliceSclSlope: sclSlope, SliceSclInter: sclInter,
		Window: win, MaxSlices: a.maxSlices,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/propagate_volume", bytes.NewReader(reqBody))
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
		resp.Error = fmt.Sprintf("propagate_volume call: %v", err)
		return resp, err
	}
	defer res.Body.Close()
	// #13 有界读取:坏/巨响应不该无界撑爆内存。
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxSAM2RespBytes+1))
	if err != nil {
		resp.Error = fmt.Sprintf("read propagate_volume resp: %v", err)
		return resp, err
	}
	if int64(len(raw)) > maxSAM2RespBytes {
		resp.Error = fmt.Sprintf("propagate_volume 响应超过 %d MB 上限", maxSAM2RespBytes/1024/1024)
		return resp, errors.New(resp.Error)
	}
	if res.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		resp.Error = fmt.Sprintf("sam2-volume %d: %s", res.StatusCode, msg)
		return resp, errors.New(resp.Error)
	}
	var pr sam2VolResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		resp.Error = fmt.Sprintf("decode propagate_volume resp: %v", err)
		return resp, err
	}

	asset, err := a.db.FindAssetByID(ctx, req.AssetID)
	if err != nil {
		resp.Error = fmt.Sprintf("load asset: %v", err)
		return resp, err
	}

	// #13 逐片**严格**校验 + 累计护栏:SAM2 直接 InsertTrack、绕过 TrackService.validate,
	// 所以单帧 64KB / 整轨 4MB / 越界 z / 重复 z / RLE 尺寸-bbox 一致 / RLE 可解码,都必须在
	// 这里自证。fail-closed——任一片非法就**拒绝整份响应**,不静默跳过个别坏片后当成功
	// (那会让复核员以为这次传播完整可信,却缺格/落错层)。
	kfs := make([]paymodel.Keyframe, 0, len(pr.Slices))
	totalGeom := 0
	seenZ := make(map[int]bool, len(pr.Slices))
	for _, sl := range pr.Slices {
		if sl.Counts == "" || len(sl.Bbox) < 4 || sl.Size[0] <= 0 || sl.Size[1] <= 0 {
			resp.Error = fmt.Sprintf("z=%d 的片缺 RLE/bbox（整份拒绝）", sl.Z)
			return resp, errors.New(resp.Error)
		}
		if sl.Z < 0 || sl.Z >= nz {
			// 越界 z 会把掩膜放到错的层且下游无从察觉。拒绝而非夹取。
			resp.Error = fmt.Sprintf("sidecar returned z=%d outside [0,%d)", sl.Z, nz)
			return resp, errors.New(resp.Error)
		}
		if seenZ[sl.Z] {
			resp.Error = fmt.Sprintf("sidecar 对 z=%d 返回多片(重复层),拒绝", sl.Z)
			return resp, errors.New(resp.Error)
		}
		seenZ[sl.Z] = true
		// RLE 是 bbox-local:Size=[h,w] 应等于 bbox 的高宽(bbox=[x,y,w,h]),且必须能解码。
		if sl.Size[0] != sl.Bbox[3] || sl.Size[1] != sl.Bbox[2] {
			resp.Error = fmt.Sprintf("z=%d 的 RLE 尺寸 %v 与 bbox %v 不符,拒绝", sl.Z, sl.Size, sl.Bbox)
			return resp, errors.New(resp.Error)
		}
		if n := len(sl.Counts); n > MaxKeyframeRLEBytes {
			resp.Error = fmt.Sprintf("z=%d 的单帧掩膜 %d 字节超过上限 %d", sl.Z, n, MaxKeyframeRLEBytes)
			return resp, errors.New(resp.Error)
		}
		rle := &paymodel.MaskRLE{Size: sl.Size, Counts: sl.Counts}
		if _, _, _, derr := DecodeCOCORLE(rle); derr != nil {
			resp.Error = fmt.Sprintf("z=%d 的 RLE 解不出: %v,拒绝", sl.Z, derr)
			return resp, errors.New(resp.Error)
		}
		totalGeom += len(sl.Counts) + 4*8
		kfs = append(kfs, paymodel.Keyframe{
			Frame:  sl.Z,
			Bbox:   []float64{float64(sl.Bbox[0]), float64(sl.Bbox[1]), float64(sl.Bbox[2]), float64(sl.Bbox[3])},
			RLE:    rle,
			Source: paymodel.TrackSourceAI,
		})
	}
	if len(kfs) == 0 {
		resp.Error = "传播没有产出掩膜（点是否落在物体上？）"
		return resp, errors.New(resp.Error)
	}
	if totalGeom > MaxTrackGeomBytes {
		resp.Error = fmt.Sprintf("传播结果几何总量 %.1f MB 超过整轨上限 %d MB", float64(totalGeom)/1024/1024, MaxTrackGeomBytes/1024/1024)
		return resp, errors.New(resp.Error)
	}
	sort.Slice(kfs, func(i, j int) bool { return kfs[i].Frame < kfs[j].Frame })

	base, _ := a.payload.MaxTrackNumber(ctx, req.TaskID)
	trackNo := base + 1
	t := &paymodel.Track{
		TaskID:    req.TaskID,
		DatasetID: asset.DatasetID,
		AssetID:   req.AssetID,
		TrackID:   trackNo,
		Label:     label,
		Kind:      paymodel.TrackKindVoxelMask,
		Color:     trackColor(trackNo),
		Source:    paymodel.TrackSourceAI,
		Attrs: map[string]interface{}{
			"ai_model":  "sam2.1_hiera_base_plus",
			"ai_source": "sam2_volume",
		},
		Keyframes: kfs,
	}
	if err := a.payload.InsertTrack(ctx, t); err != nil {
		resp.Error = fmt.Sprintf("insert track: %v", err)
		return resp, err
	}

	resp.Status = "success"
	resp.Raw = map[string]interface{}{
		"track_id":   t.ID,
		"track_no":   trackNo,
		"keyframes":  len(kfs),
		"elapsed_ms": time.Since(started).Milliseconds(),
	}
	return resp, nil
}

// extraFloatSlice pulls a []float64 out of an Extras value that may arrive as
// []float64 or []interface{} (JSON round-trips lose the concrete type).
func extraFloatSlice(v interface{}) []float64 {
	switch s := v.(type) {
	case []float64:
		return s
	case []interface{}:
		out := make([]float64, 0, len(s))
		for _, x := range s {
			if f, ok := x.(float64); ok {
				out = append(out, f)
			}
		}
		return out
	}
	return nil
}
