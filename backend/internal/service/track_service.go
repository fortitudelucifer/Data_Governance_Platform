package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// TrackLimits caps track/keyframe/point counts to keep payload rows bounded
// and the frontend responsive. Defaults per 执行方案-02 §上限校验; configurable later.
type TrackLimits struct {
	MaxTracksPerTask     int
	MaxKeyframesPerTrack int
	MaxPointsPerShape    int
}

// DefaultTrackLimits returns the plan defaults.
func DefaultTrackLimits() TrackLimits {
	return TrackLimits{MaxTracksPerTask: 200, MaxKeyframesPerTrack: 2000, MaxPointsPerShape: 1000}
}

// Sentinel errors mapped to HTTP status by the handler.
var (
	ErrTrackConflict = errors.New("track version conflict")      // → 409
	ErrTrackNotFound = errors.New("track not found or inactive") // → 404
	ErrTaskNotFound  = errors.New("task not found")              // → 404
	// ErrTaskNotEditable:任务不在人工可编辑态(仅 HUMAN_PENDING/HUMAN_IN_PROGRESS/
	// QA_REJECTED 接受写入)。冻结态(FINALIZED/EXPORTED)与送审态(QA_PENDING)都拒
	// (#2/#15)。→ 409
	ErrTaskNotEditable = errors.New("task is not in an editable state (only HUMAN_PENDING/HUMAN_IN_PROGRESS/QA_REJECTED accept edits)")
)

// TrackService owns the video track lifecycle: per-track upsert under an
// optimistic lock, adopt (archive AI + create human), and reads. Tracks persist
// in mm_tracks (never in HumanAnnotation).
type TrackService struct {
	db          *repository.DB
	payload     *repository.DB
	limits      TrackLimits
	cap         *CapabilityService // optional: video.detect_track manual trigger
	assetReader AssetReader        // optional: read volume_meta blob for PropagateVolume (C4.2)
}

// NewTrackService wires the dependencies with default limits.
func NewTrackService(db *repository.DB, payload *repository.DB) *TrackService {
	return &TrackService{db: db, payload: payload, limits: DefaultTrackLimits()}
}

// WithCapability injects the capability service so DetectTrack can invoke the
// det-server adapter. Returns the receiver for chaining.
func (s *TrackService) WithCapability(cap *CapabilityService) *TrackService {
	s.cap = cap
	return s
}

// WithAssetReader injects a blob opener so PropagateVolume can read the
// volume_meta derivative (spacing/window/scl) when building the AI request.
func (s *TrackService) WithAssetReader(r AssetReader) *TrackService {
	s.assetReader = r
	return s
}

// AdoptBatchFilter selects which active AI tracks to adopt. Filters AND together;
// at least one selection mechanism (All / IDs / Label / MinScore) must be set.
type AdoptBatchFilter struct {
	IDs      []string // specific AI track object ids
	All      bool     // all active AI tracks
	Label    string   // only this label
	MinScore float64  // only ai_score >= this (from track attr ai_score)
}

// AdoptBatch adopts multiple active AI tracks in one call (执行方案-02 B2.7):
// 全部 / 按标签 / 按置信度阈值。Each adoption archives the AI track and creates a
// human track (via Adopt). Returns the created human tracks.
func (s *TrackService) AdoptBatch(ctx context.Context, taskID, userID uint, f AdoptBatchFilter) ([]paymodel.Track, error) {
	if !f.All && len(f.IDs) == 0 && f.Label == "" && f.MinScore <= 0 {
		return nil, fmt.Errorf("no adoption selection (need all / ids / label / min_score)")
	}
	ai, err := s.payload.ListActiveTracksByTask(ctx, taskID, paymodel.TrackSourceAI, "")
	if err != nil {
		return nil, err
	}
	idSet := map[string]bool{}
	for _, id := range f.IDs {
		idSet[id] = true
	}
	adopted := make([]paymodel.Track, 0, len(ai))
	for _, t := range ai {
		if len(f.IDs) > 0 && !idSet[t.ID] {
			continue
		}
		if f.Label != "" && t.Label != f.Label {
			continue
		}
		if f.MinScore > 0 {
			score, _ := t.Attrs["ai_score"].(float64)
			if score < f.MinScore {
				continue
			}
		}
		h, aerr := s.Adopt(ctx, taskID, t.ID, userID)
		if aerr != nil {
			return adopted, aerr
		}
		adopted = append(adopted, *h)
	}
	return adopted, nil
}

// DetectTrackOpts lets the workspace pick the detector/tracker/sampling per run.
type DetectTrackOpts struct {
	Model      string // yolo | rtdetr
	Tracker    string // bytetrack | botsort
	SampleStep int    // sample every Nth frame (0 = adapter default)
}

// PropagateOpts is a SAM2 propagate request: a prompt (point/box) on one frame.
type PropagateOpts struct {
	Frame      int
	Points     [][]float64 // [[x,y,label], ...]
	Box        []float64   // [x1,y1,x2,y2]
	SampleStep int
	Label      string
	AutoAdopt  bool // true → write directly as a human track (skip 采纳)
	UserID     uint // annotator id for created_by when auto-adopting
}

// Propagate runs video.sam2_propagate for a task: one prompt on one frame → SAM2
// propagates the object across the clip → one polygon track written to mm_tracks.
// Returns the number of keyframes in the created track.
func (s *TrackService) Propagate(ctx context.Context, taskID uint, opts PropagateOpts) (int, error) {
	if s.cap == nil || !s.cap.Has(CapabilityVideoSAM2Propagate) {
		return 0, fmt.Errorf("sam2 propagate capability not configured")
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return 0, ErrTaskNotFound
	}
	asset, err := s.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		return 0, fmt.Errorf("load asset: %w", err)
	}
	if asset.Modality != dbmodel.ModalityVideo {
		return 0, fmt.Errorf("task %d is not a video task", taskID)
	}
	extras := map[string]interface{}{
		"frame":       opts.Frame,
		"points":      opts.Points,
		"sample_step": opts.SampleStep,
		"label":       opts.Label,
		"auto_adopt":  opts.AutoAdopt,
		"user_id":     int(opts.UserID),
	}
	if len(opts.Box) == 4 {
		extras["box"] = opts.Box
	}
	resp, err := s.cap.Invoke(ctx, CapabilityRequest{
		TaskID: taskID, AssetID: asset.ID, CapabilityType: CapabilityVideoSAM2Propagate,
		AssetURI: asset.StorageURI, MIME: asset.MIME, Width: asset.Width, Height: asset.Height,
		Extras: extras,
	})
	if err != nil {
		return 0, err
	}
	if resp.Status != "success" {
		return 0, fmt.Errorf("propagate: %s", resp.Error)
	}
	kf := 0
	if raw, ok := resp.Raw.(map[string]interface{}); ok {
		if v, ok := raw["keyframes"].(int); ok {
			kf = v
		}
	}
	return kf, nil
}

// VolumePropagateOpts is the prompt for C4.2 SAM2 volume propagation.
type VolumePropagateOpts struct {
	PromptZ int         // absolute z of the slice where the point was clicked
	Points  [][]float64 // [[u,v,label], ...] in axial voxel coords
	Label   string
	UserID  uint
}

// VolumePropagateResult reports what the propagation produced so the frontend can
// load the new segment without a full track refetch.
type VolumePropagateResult struct {
	TrackID   string `json:"track_id"`
	TrackNo   int    `json:"track_no"`
	Keyframes int    `json:"keyframes"`
}

// PropagateVolume runs seg.sam2_volume: one point on one axial slice → SAM2 treats
// the volume as a z-sequence and propagates → one **voxel_mask** track (C4.2).
//
// The result is the exact geometry a human draws (C3.3) and export reads (C3.4),
// so the segment panel / brush / undo / save / NIfTI export all work unchanged.
// That layering is the whole reason C4.2 comes before C4.1.
func (s *TrackService) PropagateVolume(ctx context.Context, taskID uint, opts VolumePropagateOpts) (*VolumePropagateResult, error) {
	if s.cap == nil || !s.cap.Has(CapabilitySAM2Volume) {
		return nil, fmt.Errorf("sam2 volume capability not configured")
	}
	if len(opts.Points) == 0 {
		return nil, fmt.Errorf("need a point prompt")
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, ErrTaskNotFound
	}
	asset, err := s.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		return nil, fmt.Errorf("load asset: %w", err)
	}
	if asset.Modality != dbmodel.ModalityVolume {
		return nil, fmt.Errorf("task %d is not a volume task", taskID)
	}

	// volume_meta carries spacing/window/scl and dims (nz); volume_slices gives
	// the slice prefix the adapter reads. Both must be derived already — a
	// volume task without them means derivation never ran, and we say so rather
	// than sending the model a request with no geometry.
	meta, err := s.loadVolumeMeta(ctx, asset.ID)
	if err != nil {
		return nil, err
	}
	slicesDeriv, err := s.db.GetDerivative(ctx, asset.ID, dbmodel.DerivativeVolumeSlices)
	if err != nil {
		return nil, fmt.Errorf("体切片派生未就绪：%w", err)
	}
	slicePrefix := slicesDeriv.StorageURI
	if slicePrefix != "" && slicePrefix[len(slicePrefix)-1] != '/' {
		slicePrefix += "/"
	}

	win := VolumeWindow{}
	if len(meta.Windows) > 0 {
		win = VolumeWindow{Width: meta.Windows[0].Width, Center: meta.Windows[0].Center}
	}

	label := opts.Label
	if label == "" {
		label = "AI 分割"
	}
	resp, err := s.cap.Invoke(ctx, CapabilityRequest{
		TaskID: taskID, AssetID: asset.ID, CapabilityType: CapabilitySAM2Volume,
		AssetURI: asset.StorageURI, MIME: asset.MIME,
		Extras: map[string]interface{}{
			"points":          opts.Points,
			"prompt_z":        opts.PromptZ,
			"label":           label,
			"user_id":         int(opts.UserID),
			"slice_prefix":    slicePrefix,
			"nz":              meta.Dims[2],
			"spacing":         meta.Spacing[:],
			"window_width":    win.Width,
			"window_center":   win.Center,
			"slice_scl_slope": meta.SliceSclSlope,
			"slice_scl_inter": meta.SliceSclInter,
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("propagate: %s", resp.Error)
	}
	out := &VolumePropagateResult{}
	if raw, ok := resp.Raw.(map[string]interface{}); ok {
		out.TrackID, _ = raw["track_id"].(string)
		if v, ok := raw["track_no"].(int); ok {
			out.TrackNo = v
		}
		if v, ok := raw["keyframes"].(int); ok {
			out.Keyframes = v
		}
	}
	return out, nil
}

// CellDetectOpts is one ROI cell-detection request (病理 C2.2).
type CellDetectOpts struct {
	ROI    []float64 // [x,y,w,h] in level-0 px — the region to detect cells in
	MPP    float64   // µm/px (0 = unknown)
	Label  string
	UserID uint
}

// CellDetectResult reports what the detection produced so the frontend can load
// the new cells track without a full refetch.
type CellDetectResult struct {
	TrackID string `json:"track_id"`
	TrackNo int    `json:"track_no"`
	Cells   int    `json:"cells"`
}

// DetectCells runs seg.cells_detect on one WSI ROI: the detector returns every
// cell in the region → **one cells track** (single keyframe frame=0 whose
// instances[] holds all cells, C-Q4: one ROI = one track). The adapter writes it
// in the exact shape a human corrects (C2.3) and export reads (C2.4), so the
// sampling-review / edit / export stages all work on one format (C4.2 分层原则).
func (s *TrackService) DetectCells(ctx context.Context, taskID uint, opts CellDetectOpts) (*CellDetectResult, error) {
	if s.cap == nil || !s.cap.Has(CapabilityCellDetect) {
		return nil, fmt.Errorf("cell detect capability not configured")
	}
	if len(opts.ROI) != 4 || opts.ROI[2] <= 0 || opts.ROI[3] <= 0 {
		return nil, fmt.Errorf("需要 ROI [x,y,w,h](level-0 像素，w/h>0)")
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, ErrTaskNotFound
	}
	asset, err := s.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		return nil, fmt.Errorf("load asset: %w", err)
	}
	if asset.Modality != dbmodel.ModalityWSI {
		return nil, fmt.Errorf("task %d 不是病理(WSI)任务", taskID)
	}
	label := opts.Label
	if label == "" {
		label = "AI 细胞"
	}
	resp, err := s.cap.Invoke(ctx, CapabilityRequest{
		TaskID: taskID, AssetID: asset.ID, CapabilityType: CapabilityCellDetect,
		AssetURI: asset.StorageURI, MIME: asset.MIME,
		Extras: map[string]interface{}{
			"roi":     opts.ROI,
			"mpp":     opts.MPP,
			"label":   label,
			"user_id": int(opts.UserID),
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("detect cells: %s", resp.Error)
	}
	out := &CellDetectResult{}
	if raw, ok := resp.Raw.(map[string]interface{}); ok {
		out.TrackID, _ = raw["track_id"].(string)
		if v, ok := raw["track_no"].(int); ok {
			out.TrackNo = v
		}
		if v, ok := raw["cells"].(int); ok {
			out.Cells = v
		}
	}
	return out, nil
}

// VolumeWindow is a display window (width/center) for the sidecar's 8-bit render.
type VolumeWindow struct {
	Width  float64
	Center float64
}

// loadVolumeMeta reads + parses the volume_meta derivative blob.
func (s *TrackService) loadVolumeMeta(ctx context.Context, assetID uint) (*VolumeMeta, error) {
	if s.assetReader == nil {
		return nil, fmt.Errorf("asset reader 未注入，无法读取 volume_meta")
	}
	d, err := s.db.GetDerivative(ctx, assetID, dbmodel.DerivativeVolumeMeta)
	if err != nil {
		return nil, fmt.Errorf("体几何派生（volume_meta）未就绪：%w", err)
	}
	rc, err := s.assetReader(ctx, d.StorageURI)
	if err != nil {
		return nil, fmt.Errorf("读取 volume_meta：%w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var m VolumeMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("解析 volume_meta：%w", err)
	}
	if m.Dims[2] <= 0 {
		return nil, fmt.Errorf("volume_meta 的 nz 非法：%v", m.Dims)
	}
	return &m, nil
}

// DetectTrack manually triggers video.detect_track for a task: it synchronously
// invokes the det-server adapter, which writes mm_tracks(source:"ai"). Returns
// the number of AI tracks written. This is the cost-safe "manual" trigger mode
// (执行方案-02 B2.8); production may later move it onto the AI worker queue.
func (s *TrackService) DetectTrack(ctx context.Context, taskID uint, opts DetectTrackOpts) (int, error) {
	if s.cap == nil || !s.cap.Has(CapabilityVideoDetectTrack) {
		return 0, fmt.Errorf("detect_track capability not configured")
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return 0, ErrTaskNotFound
	}
	asset, err := s.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		return 0, fmt.Errorf("load asset: %w", err)
	}
	if asset.Modality != dbmodel.ModalityVideo {
		return 0, fmt.Errorf("task %d is not a video task (modality=%s)", taskID, asset.Modality)
	}

	// Dataset-level cost gate (B2.8): the dataset owner sets the ceiling; the
	// caller may only pick a model/tracker and sample more sparsely within it.
	cfg := s.videoAIConfig(ctx, asset.DatasetID).ApplyRequestOverrides(opts)
	if cfg.Trigger == VideoAITriggerOff {
		return 0, ErrVideoAIDisabled
	}
	extras := map[string]interface{}{
		"model":         cfg.Model,
		"tracker":       cfg.Tracker,
		"sample_step":   cfg.SampleStep,
		"max_frames":    cfg.MaxFrames,
		"min_score":     cfg.MinScore,
		"min_keyframes": cfg.MinKeyframes,
	}
	resp, err := s.cap.Invoke(ctx, CapabilityRequest{
		TaskID:         taskID,
		AssetID:        asset.ID,
		CapabilityType: CapabilityVideoDetectTrack,
		AssetURI:       asset.StorageURI,
		MIME:           asset.MIME,
		Width:          asset.Width,
		Height:         asset.Height,
		Extras:         extras,
	})
	if err != nil {
		return 0, err
	}
	if resp.Status != "success" {
		return 0, fmt.Errorf("detect_track: %s", resp.Error)
	}
	written := 0
	if raw, ok := resp.Raw.(map[string]interface{}); ok {
		if v, ok := raw["tracks_written"].(int); ok {
			written = v
		}
	}
	return written, nil
}

// TrackUpsertRequest is a single-track upsert payload (per-track granularity so
// high-frequency autosave only writes the edited track).
type TrackUpsertRequest struct {
	ID        string                 `json:"id"`       // empty = create
	TrackID   int                    `json:"track_id"` // for create; ≤0 = server assigns max+1
	Label     string                 `json:"label"`
	Kind      string                 `json:"kind"`
	Color     string                 `json:"color"`
	Attrs     map[string]interface{} `json:"attrs"`
	Keyframes []paymodel.Keyframe    `json:"keyframes"`
	Version   int                    `json:"version"` // required for update (optimistic lock)
}

// List returns active tracks for a task (optional source/label filters).
func (s *TrackService) List(ctx context.Context, taskID uint, source, label string) ([]paymodel.Track, error) {
	return s.payload.ListActiveTracksByTask(ctx, taskID, source, label)
}

func (s *TrackService) validate(req *TrackUpsertRequest) error {
	if len(req.Keyframes) == 0 {
		return errors.New("track 至少需要一个关键帧")
	}
	if len(req.Keyframes) > s.limits.MaxKeyframesPerTrack {
		return fmt.Errorf("关键帧数 %d 超过上限 %d", len(req.Keyframes), s.limits.MaxKeyframesPerTrack)
	}
	totalGeom := 0
	totalCells := 0
	for i := range req.Keyframes {
		if len(req.Keyframes[i].Points) > s.limits.MaxPointsPerShape*2 { // flat [x,y,...] → *2
			return fmt.Errorf("单形状点数超过上限 %d", s.limits.MaxPointsPerShape)
		}
		// kind ↔ 几何一致性(#13):导出/渲染按 kind 分派几何。若 kind 与实际几何字段不符
		// (如 kind=polygon 却只带 bbox),页面按"有没有几何"照样画出来,导出却按 kind 静默
		// 跳过——"页面有、交付无"的静默错。在写入处就拒绝,别让它编得出来。只查非 outside
		// 的关键帧(outside 帧本就无几何);同时带两种几何时以 kind 主几何在场为准,不误伤。
		if kf := &req.Keyframes[i]; !kf.Outside {
			switch req.Kind {
			case paymodel.TrackKindBBox:
				if len(kf.Points) > 0 && len(kf.Bbox) != 4 {
					return errors.New("kind=bbox 但关键帧几何是 points 而非 bbox：几何与 kind 不符(否则导出会静默丢这条)")
				}
			case paymodel.TrackKindPolygon, paymodel.TrackKindMask:
				if len(kf.Bbox) == 4 && len(kf.Points) < 6 {
					return fmt.Errorf("kind=%s 但关键帧几何是 bbox 而非多边形：几何与 kind 不符(否则导出会静默丢这条)", req.Kind)
				}
			case paymodel.TrackKindVoxelMask:
				if verr := validateVoxelMaskKeyframe(kf); verr != nil {
					return verr
				}
			}
		}
		// cells(病理 C2):每个细胞的 bbox-local RLE 藏在 instances[] 里,顶层的
		// RLE/Bbox 护栏看不到——显式把每实例的 RLE 字节计入 totalGeom(让 4MB 后盾
		// 也覆盖 cells),并累计细胞数(4MB 字节护栏兜不住整片 6.7 万细胞≈2.8MB,
		// 得靠单独的细胞数上限 MaxCellsPerTrack 从结构上强制一个 ROI 一条轨迹)。
		for j := range req.Keyframes[i].Instances {
			inst := &req.Keyframes[i].Instances[j]
			totalCells++
			if inst.RLE != nil {
				totalGeom += len(inst.RLE.Counts)
			}
			totalGeom += len(inst.Bbox) * 8
		}
		// 稠密掩膜的护栏(00《稠密几何存储契约》规则 1)。voxel_mask 是"稀疏内联"
		// 侧的成员,但它会**滑向稠密**:切片一多、单片 RLE 一大,整条 track 就
		// 逼近载荷行的量级纪律。撞线时**明确拒绝并告诉标注员改走 voxel_label
		// 外置工作流**——不静默转换(静默转换 = 他以为存下的是这个,实际是另一个),
		// 也不能让他以为是自己画错了重画一遍。
		if r := req.Keyframes[i].RLE; r != nil {
			n := len(r.Counts)
			if n > MaxKeyframeRLEBytes {
				return fmt.Errorf("第 %d 个关键帧的掩膜 %.0f KB 超过单帧上限 %d KB：该目标过大，"+
					"请改用 voxel_label 外置工作流（整卷标签体），不要继续在逐切片 RLE 上画",
					req.Keyframes[i].Frame, float64(n)/1024, MaxKeyframeRLEBytes/1024)
			}
			totalGeom += n
		}
		totalGeom += len(req.Keyframes[i].Points)*8 + len(req.Keyframes[i].Bbox)*8
	}
	if totalCells > MaxCellsPerTrack {
		return fmt.Errorf("这条轨迹有 %d 个细胞实例，超过单轨迹上限 %d：细胞检测的任务粒度是 ROI，"+
			"一个 ROI 一条轨迹装该 ROI 的全部细胞；请按 ROI 拆分，别把整片细胞塞进一条轨迹", totalCells, MaxCellsPerTrack)
	}
	if totalGeom > MaxTrackGeomBytes {
		return fmt.Errorf("整条 track 的几何总量 %.1f MB 超过上限 %d MB：请改用 voxel_label "+
			"外置工作流，或把标注拆成多条 track", float64(totalGeom)/1024/1024, MaxTrackGeomBytes/1024/1024)
	}
	// Persist keyframes sorted by ts_ms (interpolation contract requires it).
	sort.SliceStable(req.Keyframes, func(i, j int) bool { return req.Keyframes[i].TsMs < req.Keyframes[j].TsMs })
	return nil
}

// validateVoxelMaskKeyframe(#10):voxel_mask 的非 outside 关键帧必须有合法 bbox-local
// 几何。否则导出会跳过/截断坐标/裁掉体外部分、甚至产出全零 segment,而 validate 却放行。
// RLE 是 bbox-local(Size=[h,w] 应等于 bbox 的高宽)。
func validateVoxelMaskKeyframe(kf *paymodel.Keyframe) error {
	if kf.RLE == nil || len(kf.Bbox) != 4 {
		return fmt.Errorf("voxel_mask 第 %d 帧缺 RLE 或 bbox", kf.Frame)
	}
	for _, v := range kf.Bbox {
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return fmt.Errorf("voxel_mask 第 %d 帧 bbox 非整数/非有限: %v", kf.Frame, kf.Bbox)
		}
	}
	bw, bh := int(kf.Bbox[2]), int(kf.Bbox[3])
	if bw <= 0 || bh <= 0 {
		return fmt.Errorf("voxel_mask 第 %d 帧 bbox 宽/高非正", kf.Frame)
	}
	if kf.RLE.Size[0] != bh || kf.RLE.Size[1] != bw {
		return fmt.Errorf("voxel_mask 第 %d 帧 RLE 尺寸 %v 与 bbox %d×%d 不符(RLE 是 bbox-local)", kf.Frame, kf.RLE.Size, bw, bh)
	}
	mask, _, _, derr := DecodeCOCORLE(kf.RLE)
	if derr != nil {
		return fmt.Errorf("voxel_mask 第 %d 帧 RLE 解不出: %w", kf.Frame, derr)
	}
	for _, p := range mask {
		if p != 0 {
			return nil // 至少一个前景体素
		}
	}
	return fmt.Errorf("voxel_mask 第 %d 帧是全零 segment(空掩膜),拒绝", kf.Frame)
}

// Upsert creates a new track or updates an existing one under an optimistic
// lock. Create enforces the per-task track cap; update returns ErrTrackConflict
// on a stale version.
func (s *TrackService) Upsert(ctx context.Context, taskID, userID uint, req TrackUpsertRequest) (*paymodel.Track, error) {
	if err := s.validate(&req); err != nil {
		return nil, err
	}
	// #15 后端强制:写入**已冻结的交付物**(FINALIZED/EXPORTED)是错的——终稿后再改 live
	// track 会让 QA 通过时冻结的快照与 live 漂移。前端有编辑锁/任务态门禁,后端这里再拦一道
	// (所有模态共用此写入路径)。并发编辑的锁归属仍主要靠前端锁,后端 TODO。
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	if !taskAcceptsWrites(task.State) {
		return nil, ErrTaskNotEditable
	}

	if req.ID == "" {
		cnt, err := s.payload.CountActiveTracksByTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if cnt >= int64(s.limits.MaxTracksPerTask) {
			return nil, fmt.Errorf("track 数达到上限 %d，无法新建", s.limits.MaxTracksPerTask)
		}
		// Assign a fresh logical track_id when none was requested, or when the
		// requested one is already live. Exports key on track_id, so a duplicate
		// would silently merge two objects into one MOT/COCO/YOLO track — and two
		// annotators racing on "next id" is exactly how that happens. Reassigning
		// rather than rejecting can never lose the annotator's work; the response
		// carries the id actually used.
		trackID := req.TrackID
		if trackID > 0 {
			taken, err := s.payload.ActiveTrackNumberTaken(ctx, taskID, trackID)
			if err != nil {
				return nil, err
			}
			if taken {
				trackID = 0
			}
		}
		if trackID <= 0 {
			mx, err := s.payload.MaxTrackNumber(ctx, taskID)
			if err != nil {
				return nil, err
			}
			trackID = mx + 1
		}
		t := &paymodel.Track{
			TaskID: taskID, DatasetID: task.DatasetID, AssetID: task.AssetID,
			TrackID: trackID, Label: req.Label, Kind: req.Kind, Color: req.Color,
			Attrs: req.Attrs, Keyframes: req.Keyframes,
			Source: paymodel.TrackSourceHuman, CreatedBy: userID, UpdatedBy: userID,
		}
		if err := s.payload.InsertTrack(ctx, t); err != nil {
			return nil, err
		}
		return t, nil
	}

	set := map[string]any{
		"label": req.Label, "kind": req.Kind, "color": req.Color,
		"attrs": req.Attrs, "keyframes": req.Keyframes,
	}
	applied, err := s.payload.UpdateTrackByVersion(ctx, taskID, req.ID, req.Version, set, userID)
	if err != nil {
		return nil, err
	}
	if !applied {
		existing, _ := s.payload.FindTrackByID(ctx, req.ID)
		// 不属于本任务 / 不存在 / 已归档 → NotFound(#1:跨任务 id 不该暴露成 409 冲突,
		// 那等于确认"这个 id 存在、只是版本不对";它压根不在本任务里)。
		if existing == nil || !existing.IsActive || existing.TaskID != taskID {
			return nil, ErrTrackNotFound
		}
		return nil, ErrTrackConflict
	}
	return s.payload.FindTrackByID(ctx, req.ID)
}

// taskAcceptsWrites reports whether a human track write is allowed for a task in
// this state. **只放行人工可编辑态**——与前端 EDITABLE_STATES 对齐。
//
// 批C 的 #15 只挡了 FINALIZED/EXPORTED,但那太松:QA_PENDING(送审中)也被放行,
// 于是标注员能在 reviewer 审过 v1 之后把 track 改成 v2,再 Pass → 冻结的是**未经
// 审核的 v2**(SetTrackReview 刻意不动 version,Upsert 的 set 也从不清 review_status,
// 所以裁决与内容对不上)(#2)。收紧成白名单:只有 HUMAN_PENDING / HUMAN_IN_PROGRESS
// / QA_REJECTED 三个态接受人工写入;QA_PENDING / FINALIZED / EXPORTED / AI 态一律拒。
// AI 适配器直接走 InsertTrack,不经这里,不受影响。
func taskAcceptsWrites(state string) bool {
	switch state {
	case dbmodel.TaskStateHumanPending, dbmodel.TaskStateHumanInProgress, dbmodel.TaskStateQARejected:
		return true
	}
	return false
}

// Delete archives a track (is_active=false) — outside/删除 semantics live in the
// UI; deletion here is a soft archive so history/audit survives.
//
// expectedVersion > 0 turns the archive into an optimistic-locked delete (#18):
// a stale client can't blind-delete a track another annotator just updated —
// version mismatch returns ErrTrackConflict. expectedVersion ≤ 0 keeps the
// unconditional archive (callers that don't track a version).
func (s *TrackService) Delete(ctx context.Context, taskID uint, trackObjectID string, expectedVersion int, userID uint) error {
	// #15 冻结的交付物不接受删除(与 Upsert 同一道后端门禁)。
	if task, err := s.db.FindAnnotationTaskByID(ctx, taskID); err != nil {
		return err
	} else if task == nil {
		return ErrTaskNotFound
	} else if !taskAcceptsWrites(task.State) {
		return ErrTaskNotEditable
	}
	t, err := s.payload.FindTrackByID(ctx, trackObjectID)
	if err != nil {
		return err
	}
	if t == nil || t.TaskID != taskID || !t.IsActive {
		return ErrTrackNotFound
	}
	if expectedVersion > 0 {
		applied, err := s.payload.SetTrackActiveByVersion(ctx, t.ID, false, expectedVersion, userID)
		if err != nil {
			return err
		}
		if !applied {
			return ErrTrackConflict // 别人刚改过这条 track,别盲删
		}
		return nil
	}
	return s.payload.SetTrackActive(ctx, t.ID, false, userID)
}

// ErrVideoAIDisabled is returned when a dataset has turned detect_track off.
var ErrVideoAIDisabled = errors.New("该数据集已关闭 AI 预标注（数据集设置 → 触发模式）")

// videoAIConfig loads the dataset's cost gate. A missing dataset or a
// hand-corrupted row degrades to the global defaults rather than blocking work.
func (s *TrackService) videoAIConfig(ctx context.Context, datasetID uint) VideoAIConfig {
	ds, err := s.db.FindDatasetByID(ctx, datasetID)
	if err != nil || ds == nil {
		return DefaultVideoAIConfig()
	}
	return VideoAIConfigFromDataset(ds.AIConfig)
}

// ErrBadReviewStatus rejects a verdict outside {"", passed, rejected}.
var ErrBadReviewStatus = errors.New("review status 必须是 passed / rejected / 空（撤销）")

// ErrNotEnoughRounds is returned when a task has never been re-submitted, so
// there is nothing to compare against.
var ErrNotEnoughRounds = errors.New("该任务只提交过一轮，暂无返工可对比")

// RoundMeta describes one submission round without its track payload.
type RoundMeta struct {
	Round       int    `json:"round"`
	SubmittedBy uint   `json:"submitted_by"`
	SubmittedAt string `json:"submitted_at"`
	TrackCount  int    `json:"track_count"`
}

// Rounds lists a task's submission rounds, newest first.
func (s *TrackService) Rounds(ctx context.Context, taskID uint) ([]RoundMeta, error) {
	rds, err := s.payload.ListTrackRoundMeta(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]RoundMeta, 0, len(rds))
	for _, r := range rds {
		out = append(out, RoundMeta{Round: r.Round, SubmittedBy: r.SubmittedBy,
			SubmittedAt: r.SubmittedAt.Format(time.RFC3339), TrackCount: r.TrackCount})
	}
	return out, nil
}

// Diff compares two submission rounds so a reviewer re-checking a rework only
// looks at what actually moved (执行方案-02 B3.1). from/to ≤ 0 default to the
// two most recent rounds.
func (s *TrackService) Diff(ctx context.Context, taskID uint, from, to int) (*TrackDiff, error) {
	if to <= 0 || from <= 0 {
		latest, err := s.payload.MaxTrackRound(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if latest < 2 {
			return nil, ErrNotEnoughRounds
		}
		from, to = latest-1, latest
	}
	if from >= to {
		return nil, fmt.Errorf("from(%d) 必须早于 to(%d)", from, to)
	}
	prev, err := s.payload.FindTrackRound(ctx, taskID, from)
	if err != nil {
		return nil, err
	}
	cur, err := s.payload.FindTrackRound(ctx, taskID, to)
	if err != nil {
		return nil, err
	}
	d := DiffTrackRounds(prev.Tracks, cur.Tracks, from, to)
	return &d, nil
}

// validReviewStatus guards the only values that may reach mm_tracks. A stray
// value would slip past the "any track still rejected?" gate on QA pass.
func validReviewStatus(status string) bool {
	switch status {
	case "", paymodel.TrackReviewPassed, paymodel.TrackReviewRejected:
		return true
	}
	return false
}

// Review records a reviewer's verdict on one track, giving the reviewer a
// per-object checklist instead of one all-or-nothing decision on the whole task
// (执行方案-02 B3.1). QAService.Pass refuses while any track is still rejected.
func (s *TrackService) Review(ctx context.Context, taskID uint, trackObjectID string, reviewerID uint, status, note string) error {
	if !validReviewStatus(status) {
		return ErrBadReviewStatus
	}
	t, err := s.payload.FindTrackByID(ctx, trackObjectID)
	if err != nil {
		return err
	}
	if t == nil || t.TaskID != taskID || !t.IsActive {
		return ErrTrackNotFound
	}
	return s.payload.SetTrackReview(ctx, t.ID, status, note, reviewerID)
}

// Adopt implements the 采纳约定: archive the AI track (is_active=false) and
// create a new human track carrying adopted_from — the model's raw output is
// preserved so adoption-rate / correction-distance stay computable.
func (s *TrackService) Adopt(ctx context.Context, taskID uint, aiTrackObjectID string, userID uint) (*paymodel.Track, error) {
	// #17 采纳也是人工写入,必须过任务态门禁——否则能在 FINALIZED 任务上采纳,
	// 凭空往冻结的交付物里塞一条 human track(Upsert/Delete 早有此门禁,Adopt 漏了)。
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrTaskNotFound
	}
	if !taskAcceptsWrites(task.State) {
		return nil, ErrTaskNotEditable
	}
	ai, err := s.payload.FindTrackByID(ctx, aiTrackObjectID)
	if err != nil {
		return nil, err
	}
	if ai == nil || !ai.IsActive || ai.TaskID != taskID {
		return nil, ErrTrackNotFound
	}
	adoptedFrom := ai.ID
	human := &paymodel.Track{
		TaskID: ai.TaskID, DatasetID: ai.DatasetID, AssetID: ai.AssetID,
		TrackID: ai.TrackID, Label: ai.Label, Kind: ai.Kind, Color: ai.Color,
		Attrs: ai.Attrs, Keyframes: ai.Keyframes,
		Source: paymodel.TrackSourceHuman, AdoptedFrom: &adoptedFrom,
		CreatedBy: userID, UpdatedBy: userID,
	}
	// #17 归档 AI + 插入 human **一个事务**,不再靠 best-effort 回滚(回滚本身会失败,
	// 留下「AI 已归档、human 没插进去」——这条 track 凭空消失,不报错)。
	if err := s.payload.ArchiveTrackAndInsert(ctx, ai.ID, human, userID); err != nil {
		return nil, err
	}
	return human, nil
}

// ListSnapshotTracks returns a task's FINALIZED track snapshots shaped as Tracks.
//
// **导出的唯一真源**（CLAUDE.md《数据模型要点》）：`annotation_tracks` 是原地
// 覆盖的、会漂，快照是 QA 通过时冻结的那一份。任何导出器都该走这里，而不是
// List()——两者形状一样，很容易顺手用错，所以这个方法单独存在并写明用途。
func (s *TrackService) ListSnapshotTracks(ctx context.Context, datasetID, taskID uint) ([]paymodel.Track, error) {
	out := []paymodel.Track{}
	_, err := s.payload.StreamTrackSnapshotsByDataset(ctx, datasetID, []uint{taskID},
		func(snap *paymodel.TrackSnapshot) error {
			out = append(out, paymodel.Track{
				ID: snap.ID, TaskID: snap.TaskID, DatasetID: snap.DatasetID, AssetID: snap.AssetID,
				TrackID: snap.TrackID, Label: snap.Label, Kind: snap.Kind, Color: snap.Color,
				Attrs: snap.Attrs, Keyframes: snap.Keyframes,
			})
			return nil
		})
	return out, err
}
