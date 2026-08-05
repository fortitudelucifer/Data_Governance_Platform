package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// SAM2PropagateAdapter implements CapabilityAdapter for video.sam2_propagate via
// the sam2-video sidecar (SAM2VideoPredictor). One point/box prompt on one frame
// → SAM2 propagates the object across the clip → per-frame polygons → written as
// a single polygon track in mm_tracks (执行方案-02 B2.2 Phase 2).
//
// sam2-video contract (http://<host>:8384):
//
//	POST /propagate {video_b64, frame, points:[[x,y,label]], box?, sample_step, max_frames}
//	→ {frames:[{frame,polygon:[x1,y1,...],score}], width, height, count, ...}
//
// The video is sent display-oriented (rotation baked) via a fresh H.264 transcode
// so cv2 on the server sees the same pixels the annotator clicked.
type SAM2PropagateAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	provider   string
	maxFrames  int
	systemUser uint

	timeout time.Duration
	hc      *http.Client
	reader  AssetReader
	tools   MediaTools
	db   *repository.DB
	payload   *repository.DB

	// SAM2VideoPredictor keeps the whole clip's image features in VRAM, so two
	// concurrent propagations can OOM the 24GB card. Serialise + bound the queue
	// (B2.8 成本闸门).
	gate *GPUGate
}

// QueueStats exposes the propagation backlog.
func (a *SAM2PropagateAdapter) QueueStats() GPUQueueStats { return a.gate.Stats() }

// SAM2PropagateAdapterConfig wires the adapter.
type SAM2PropagateAdapterConfig struct {
	Endpoint     string
	APIKey       string
	ProviderName string
	MaxFrames    int
	MaxQueue     int // waiting-room bound; 0 → default
	SystemUserID uint
	Timeout      time.Duration
	Reader       AssetReader
	Tools        MediaTools
	DB        *repository.DB
	Payload        *repository.DB
}

// NewSAM2PropagateAdapter constructs the adapter.
func NewSAM2PropagateAdapter(cfg SAM2PropagateAdapterConfig) *SAM2PropagateAdapter {
	if cfg.ProviderName == "" {
		cfg.ProviderName = "sam2-video"
	}
	if cfg.MaxFrames <= 0 {
		cfg.MaxFrames = 300
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 600 * time.Second
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = 4
	}
	return &SAM2PropagateAdapter{
		capability: CapabilityVideoSAM2Propagate,
		endpoint:   trimRightSlash(cfg.Endpoint),
		apiKey:     cfg.APIKey,
		provider:   cfg.ProviderName,
		maxFrames:  cfg.MaxFrames,
		systemUser: cfg.SystemUserID,
		gate:       NewGPUGate(1, cfg.MaxQueue), // 1: whole clip lives in VRAM
		timeout:    cfg.Timeout,
		hc:         &http.Client{Timeout: cfg.Timeout},
		reader:     cfg.Reader,
		tools:      cfg.Tools,
		db:      cfg.DB,
		payload:      cfg.Payload,
	}
}

// Capability implements CapabilityAdapter.
func (a *SAM2PropagateAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter can run.
func (a *SAM2PropagateAdapter) Configured() bool {
	return a != nil && a.endpoint != "" && a.reader != nil && a.payload != nil && a.db != nil && a.tools.FFmpeg != ""
}

type sam2PropReq struct {
	VideoB64   string      `json:"video_b64"`
	Frame      int         `json:"frame"`
	Points     [][]float64 `json:"points"`
	Box        []float64   `json:"box,omitempty"`
	SampleStep int         `json:"sample_step"`
	MaxFrames  int         `json:"max_frames"`
}

type sam2PropResp struct {
	Frames []struct {
		Frame   int       `json:"frame"`
		Polygon []float64 `json:"polygon"`
		Score   float64   `json:"score"`
	} `json:"frames"`
	Width  int `json:"width"`
	Height int `json:"height"`
	Count  int `json:"count"`
}

// Invoke: transcode → send video + prompt → build one polygon track from the
// propagated per-frame polygons.
func (a *SAM2PropagateAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
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
		resp.Error = "sam2 propagate adapter not fully configured"
		return resp, errors.New(resp.Error)
	}
	// One clip at a time in VRAM; bounded waiting room past that.
	if err := a.gate.Acquire(ctx); err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	defer a.gate.Release()

	points := extractPoints(req.Extras)
	box := extractBox(req.Extras)
	if len(points) == 0 && len(box) != 4 {
		resp.Error = "no points/box prompt provided"
		return resp, errors.New(resp.Error)
	}
	for i, p := range points { // ensure [x,y,label]
		if len(p) == 2 {
			points[i] = []float64{p[0], p[1], 1}
		}
	}
	frame, _ := extraInt(req.Extras["frame"])
	sampleStep, ok := extraInt(req.Extras["sample_step"])
	if !ok || sampleStep <= 0 {
		sampleStep = 1
	}
	label, _ := req.Extras["label"].(string)
	if label == "" {
		label = "object"
	}
	// auto_adopt: write directly as a human track (created by the annotator);
	// else write as an AI track that goes through the 采纳 flow.
	autoAdopt, _ := req.Extras["auto_adopt"].(bool)
	uid, _ := extraInt(req.Extras["user_id"])
	source := paymodel.TrackSourceAI
	createdBy := a.systemUser
	if autoAdopt {
		source = paymodel.TrackSourceHuman
		if uid > 0 {
			createdBy = uint(uid)
		}
	}
	started := time.Now()

	asset, err := a.db.FindAssetByID(ctx, req.AssetID)
	if err != nil {
		resp.Error = fmt.Sprintf("load asset: %v", err)
		return resp, err
	}
	fps := 30.0
	if asset.FPS != nil && *asset.FPS > 0 {
		fps = *asset.FPS
	}

	// Display-oriented H.264 (rotation baked) so the server's cv2 frames match
	// the annotator's click space + the frame index.
	srcPath, cleanupSrc, err := a.materialize(ctx, req.AssetURI, req.MIME)
	if err != nil {
		resp.Error = fmt.Sprintf("materialize: %v", err)
		return resp, err
	}
	defer cleanupSrc()
	outPath, cleanupOut, err := a.tools.Transcode(ctx, srcPath)
	if err != nil {
		resp.Error = fmt.Sprintf("transcode: %v", err)
		return resp, err
	}
	defer cleanupOut()
	vbytes, err := os.ReadFile(outPath)
	if err != nil {
		resp.Error = fmt.Sprintf("read transcoded: %v", err)
		return resp, err
	}

	// Call /propagate.
	reqBody, _ := json.Marshal(sam2PropReq{
		VideoB64:   base64.StdEncoding.EncodeToString(vbytes),
		Frame:      frame,
		Points:     points,
		Box:        box,
		SampleStep: sampleStep,
		MaxFrames:  a.maxFrames,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/propagate", bytes.NewReader(reqBody))
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
		resp.Error = fmt.Sprintf("propagate call: %v", err)
		return resp, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		resp.Error = fmt.Sprintf("sam2-video %d: %s", res.StatusCode, msg)
		return resp, errors.New(resp.Error)
	}
	var pr sam2PropResp
	if err := json.Unmarshal(raw, &pr); err != nil {
		resp.Error = fmt.Sprintf("decode propagate resp: %v", err)
		return resp, err
	}

	// Build one polygon track from the per-frame polygons.
	kfs := make([]paymodel.Keyframe, 0, len(pr.Frames))
	for _, fr := range pr.Frames {
		if len(fr.Polygon) < 6 {
			continue
		}
		kfs = append(kfs, paymodel.Keyframe{
			Frame: fr.Frame, TsMs: float64(fr.Frame) * 1000.0 / fps,
			Points: fr.Polygon, Outside: false, Occluded: false, Source: paymodel.TrackSourceAI,
		})
	}
	if len(kfs) == 0 {
		resp.Error = "propagation produced no polygons (点是否落在物体上?)"
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
		// Dense per-frame segmentation, stored as an outline (B1 收尾⑤). Distinct
		// from a hand-drawn polygon: every frame gets its own keyframe, vertex
		// counts differ frame to frame, and there is nothing to interpolate.
		Kind:  paymodel.TrackKindMask,
		Color: trackColor(trackNo),
		Attrs: map[string]interface{}{
			"ai_model":  "sam2.1_hiera_base_plus",
			"ai_source": "sam2_propagate",
		},
		Keyframes: kfs,
		Source:    source,
		CreatedBy: createdBy,
		UpdatedBy: createdBy,
	}
	if err := a.payload.InsertTrack(ctx, t); err != nil {
		resp.Error = fmt.Sprintf("insert track: %v", err)
		return resp, err
	}

	resp.Status = "success"
	resp.LatencyMs = time.Since(started).Milliseconds()
	resp.Raw = map[string]interface{}{
		"tracks_written": 1,
		"keyframes":      len(kfs),
		"track_id":       trackNo,
	}
	return resp, nil
}

// materialize streams the asset body to a temp file (mirrors DetTrackAdapter).
func (a *SAM2PropagateAdapter) materialize(ctx context.Context, uri, mime string) (string, func(), error) {
	body, err := a.reader(ctx, uri)
	if err != nil {
		return "", func() {}, err
	}
	defer body.Close()
	f, err := os.CreateTemp("", "sam2prop-src-*"+extForMIME(mime))
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := io.Copy(f, body); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	_ = f.Close()
	return path, cleanup, nil
}
