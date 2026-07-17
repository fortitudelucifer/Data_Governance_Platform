package service

import (
	"context"
	"errors"
	"fmt"
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
)

// TrackService owns the video track lifecycle: per-track upsert under an
// optimistic lock, adopt (archive AI + create human), and reads. Tracks persist
// in mm_tracks (never in HumanAnnotation).
type TrackService struct {
	db      *repository.DB
	payload *repository.DB
	limits  TrackLimits
	cap     *CapabilityService // optional: video.detect_track manual trigger
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
	Keyframes []paymodel.Keyframe  `json:"keyframes"`
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
	for i := range req.Keyframes {
		if len(req.Keyframes[i].Points) > s.limits.MaxPointsPerShape*2 { // flat [x,y,...] → *2
			return fmt.Errorf("单形状点数超过上限 %d", s.limits.MaxPointsPerShape)
		}
	}
	// Persist keyframes sorted by ts_ms (interpolation contract requires it).
	sort.SliceStable(req.Keyframes, func(i, j int) bool { return req.Keyframes[i].TsMs < req.Keyframes[j].TsMs })
	return nil
}

// Upsert creates a new track or updates an existing one under an optimistic
// lock. Create enforces the per-task track cap; update returns ErrTrackConflict
// on a stale version.
func (s *TrackService) Upsert(ctx context.Context, taskID, userID uint, req TrackUpsertRequest) (*paymodel.Track, error) {
	if err := s.validate(&req); err != nil {
		return nil, err
	}

	if req.ID == "" {
		task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, ErrTaskNotFound
		}
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
	applied, err := s.payload.UpdateTrackByVersion(ctx, req.ID, req.Version, set, userID)
	if err != nil {
		return nil, err
	}
	if !applied {
		existing, _ := s.payload.FindTrackByID(ctx, req.ID)
		if existing == nil || !existing.IsActive {
			return nil, ErrTrackNotFound
		}
		return nil, ErrTrackConflict
	}
	return s.payload.FindTrackByID(ctx, req.ID)
}

// Delete archives a track (is_active=false) — outside/删除 semantics live in the
// UI; deletion here is a soft archive so history/audit survives.
func (s *TrackService) Delete(ctx context.Context, taskID uint, trackObjectID string, userID uint) error {
	t, err := s.payload.FindTrackByID(ctx, trackObjectID)
	if err != nil {
		return err
	}
	if t == nil || t.TaskID != taskID || !t.IsActive {
		return ErrTrackNotFound
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
	ai, err := s.payload.FindTrackByID(ctx, aiTrackObjectID)
	if err != nil {
		return nil, err
	}
	if ai == nil || !ai.IsActive || ai.TaskID != taskID {
		return nil, ErrTrackNotFound
	}
	if err := s.payload.SetTrackActive(ctx, ai.ID, false, userID); err != nil {
		return nil, err
	}
	adoptedFrom := ai.ID
	human := &paymodel.Track{
		TaskID: ai.TaskID, DatasetID: ai.DatasetID, AssetID: ai.AssetID,
		TrackID: ai.TrackID, Label: ai.Label, Kind: ai.Kind, Color: ai.Color,
		Attrs: ai.Attrs, Keyframes: ai.Keyframes,
		Source: paymodel.TrackSourceHuman, AdoptedFrom: &adoptedFrom,
		CreatedBy: userID, UpdatedBy: userID,
	}
	if err := s.payload.InsertTrack(ctx, human); err != nil {
		_ = s.payload.SetTrackActive(ctx, ai.ID, true, userID) // best-effort rollback
		return nil, err
	}
	return human, nil
}
