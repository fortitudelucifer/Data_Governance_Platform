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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"text-annotation-platform/internal/repository"

	paymodel "text-annotation-platform/internal/model/payload"
)

// DetTrackAdapter implements CapabilityAdapter for video.detect_track via the
// det-server sidecar (YOLO26x + RT-DETR + ByteTrack/BoT-SORT). Unlike the
// OCR/VLM/ASR adapters — which return a typed CapabilityResponse the worker
// persists — the detect-track adapter writes tracks DIRECTLY into mm_tracks
// (source:"ai") per 执行方案-02 B2.5, then returns a summary response.
//
// det-server contract (http://<host>:8382):
//
//	POST /track {image_b64, conf, iou, tracker, classes?, model, persist}
//	→ {tracks:[{track_id,class_id,class_name,confidence,box:[x1,y1,x2,y2]}], image_size:[W,H], ...}
//
// The tracker is STATEFUL server-side: persist=true carries track_id across
// sequential calls. We therefore (a) serialise Invoke through a bounded GPUGate
// so two videos never share tracker state and a batch upload cannot pile up
// unboundedly, and (b) send persist=false on the FIRST frame of each video to
// reset the tracker, then persist=true for the rest.
// The server's track_id is remapped to a fresh per-task track number.
type DetTrackAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	provider   string

	tracker      string
	model        string
	conf         float64
	iou          float64
	sampleStep   int
	maxFrames    int
	minKeyframes int     // drop AI tracks with fewer keyframes (spurious ID-switch fragments)
	minScore     float64 // drop AI tracks whose avg confidence is below this (weak duplicates)
	systemUser   uint

	timeout time.Duration
	hc      *http.Client
	reader  AssetReader
	tools   MediaTools
	db   *repository.DB
	payload   *repository.DB

	// gate serialises (the det-server tracker holds global state) AND bounds the
	// waiting room. A plain mutex serialises too, but leaves the queue unbounded:
	// a batch upload parks every request behind it until their deadlines expire,
	// and the box has no way to shed load (B2.8 成本闸门).
	gate *GPUGate
}

// QueueStats exposes the GPU backlog so the workbench can show it and stop the
// user before they click into a rejection.
func (a *DetTrackAdapter) QueueStats() GPUQueueStats { return a.gate.Stats() }

// DetTrackAdapterConfig wires the detect-track adapter.
type DetTrackAdapterConfig struct {
	Endpoint     string
	APIKey       string
	ProviderName string
	Tracker      string // bytetrack | botsort
	Model        string // yolo | rtdetr
	Conf         float64
	IoU          float64
	SampleStep   int // sample every Nth source frame
	MaxFrames    int     // cap sampled frames per video
	MinKeyframes int     // drop tracks shorter than this (noise); 0 = keep all
	MinScore     float64 // drop tracks below this avg confidence; 0 = keep all
	// MaxQueue bounds how many callers may wait for the (single) GPU slot before
	// new ones are rejected outright. 0 = unbounded (the old mutex behaviour).
	MaxQueue     int
	SystemUserID uint
	Timeout      time.Duration
	Reader       AssetReader
	Tools        MediaTools
	DB        *repository.DB
	Payload        *repository.DB
}

// NewDetTrackAdapter constructs the adapter, applying sensible defaults.
func NewDetTrackAdapter(cfg DetTrackAdapterConfig) *DetTrackAdapter {
	if cfg.ProviderName == "" {
		cfg.ProviderName = "det-server"
	}
	if cfg.Tracker == "" {
		cfg.Tracker = "botsort" // ReID (yolo26n-reid) → fewer ID switches / duplicate boxes
	}
	if cfg.Model == "" {
		cfg.Model = "yolo"
	}
	if cfg.MinKeyframes <= 0 {
		cfg.MinKeyframes = 2 // drop single-frame flickers by default
	}
	if cfg.MinScore <= 0 {
		cfg.MinScore = 0.4 // drop weak/duplicate detections (annotator adds missed ones)
	}
	if cfg.Conf <= 0 {
		cfg.Conf = 0.25
	}
	if cfg.IoU <= 0 {
		cfg.IoU = 0.7
	}
	if cfg.SampleStep <= 0 {
		cfg.SampleStep = 5
	}
	if cfg.MaxFrames <= 0 {
		cfg.MaxFrames = 600
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 600 * time.Second // whole-video track loop can be slow
	}
	if cfg.MaxQueue <= 0 {
		// A job takes minutes; 4 queued means the last one waits ~5× the job time.
		// Beyond that a caller is better served by an immediate "try later".
		cfg.MaxQueue = 4
	}
	return &DetTrackAdapter{
		capability: CapabilityVideoDetectTrack,
		endpoint:   trimRightSlash(cfg.Endpoint),
		apiKey:     cfg.APIKey,
		provider:   cfg.ProviderName,
		tracker:      cfg.Tracker,
		model:        cfg.Model,
		conf:         cfg.Conf,
		iou:          cfg.IoU,
		sampleStep:   cfg.SampleStep,
		maxFrames:    cfg.MaxFrames,
		minKeyframes: cfg.MinKeyframes,
		minScore:     cfg.MinScore,
		systemUser:   cfg.SystemUserID,
		// concurrency 1: the det-server tracker is globally stateful.
		gate:       NewGPUGate(1, cfg.MaxQueue),
		timeout:    cfg.Timeout,
		// keep-alive client so the stateful tracker stays on one connection.
		hc:     &http.Client{Timeout: cfg.Timeout},
		reader: cfg.Reader,
		tools:  cfg.Tools,
		db:  cfg.DB,
		payload:  cfg.Payload,
	}
}

// Capability implements CapabilityAdapter.
func (a *DetTrackAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter can run.
func (a *DetTrackAdapter) Configured() bool {
	return a != nil && a.endpoint != "" && a.reader != nil && a.payload != nil && a.db != nil && a.tools.FFmpeg != ""
}

// trackReq/trackResp mirror the det-server /track contract.
type trackReq struct {
	ImageB64 string `json:"image_b64"`
	Conf     float64 `json:"conf"`
	IoU      float64 `json:"iou"`
	Tracker  string  `json:"tracker"`
	Classes  []int   `json:"classes,omitempty"`
	Model    string  `json:"model"`
	Persist  bool    `json:"persist"`
}

type trackDet struct {
	TrackID    int       `json:"track_id"`
	ClassID    int       `json:"class_id"`
	ClassName  string    `json:"class_name"`
	Confidence float64   `json:"confidence"`
	Box        []float64 `json:"box"` // [x1,y1,x2,y2]
}

type trackResp struct {
	Tracks     []trackDet `json:"tracks"`
	ImageSize  []int      `json:"image_size"`
	InferMs    float64    `json:"inference_ms"`
	Tracker    string     `json:"tracker"`
	Model      string     `json:"model"`
}

// kfEntry is one per-frame observation of a server track before remap.
type kfEntry struct {
	frame int
	tsMs  float64
	bbox  []float64 // [x,y,w,h]
	label string
	score float64
}

// Invoke implements CapabilityAdapter: sample frames → per-frame /track →
// group by server track_id → write mm_tracks(source:"ai").
func (a *DetTrackAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   a.provider,
			ModelID:        a.model + "+" + a.tracker,
			CapabilityType: a.capability,
			EndpointMode:   EndpointModeAdapter,
		},
	}
	if !a.Configured() {
		resp.Error = "detect_track adapter not fully configured"
		return resp, errors.New(resp.Error)
	}
	started := time.Now()

	// Only one video at a time — the det-server tracker holds global state — and
	// only a bounded number may queue behind it. Past that, fail fast so the
	// caller sees "队列已满，稍后再试" instead of a context deadline 10 minutes on.
	if err := a.gate.Acquire(ctx); err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	defer a.gate.Release()

	// 1. Resolve asset for dataset_id + fps (ts_ms = frame*1000/fps; CFR-exact,
	//    VFR-approximate — refine later by reading the frame-index pts_ms).
	asset, err := a.db.FindAssetByID(ctx, req.AssetID)
	if err != nil {
		resp.Error = fmt.Sprintf("load asset: %v", err)
		return resp, err
	}
	fps := 30.0
	if asset.FPS != nil && *asset.FPS > 0 {
		fps = *asset.FPS
	}
	// Dataset ontology → map COCO class_name to the dataset's own label + color.
	onto := a.loadOntology(ctx, asset.DatasetID)

	// Per-call overrides from Extras. TrackService fills these from the dataset's
	// cost gate (B2.8) after layering the caller's model/tracker/sample_step on
	// top; fall back to adapter defaults when invoked without a gate (worker).
	sampleStep, conf := a.sampleStep, a.conf
	maxFrames := a.maxFrames
	tracker, model, minKf, minScore := a.tracker, a.model, a.minKeyframes, a.minScore
	if req.Extras != nil {
		if v, ok := extraInt(req.Extras["sample_step"]); ok && v > 0 {
			sampleStep = v
		}
		if v, ok := extraInt(req.Extras["max_frames"]); ok && v > 0 {
			maxFrames = v
		}
		if v, ok := extraInt(req.Extras["min_keyframes"]); ok && v >= 0 {
			minKf = v
		}
		if v, ok := extraFloat(req.Extras["min_score"]); ok && v >= 0 {
			minScore = v
		}
		if v, ok := req.Extras["tracker"].(string); ok && (v == "bytetrack" || v == "botsort") {
			tracker = v
		}
		if v, ok := req.Extras["model"].(string); ok && (v == "yolo" || v == "rtdetr") {
			model = v
		}
	}
	resp.Provider.ModelID = model + "+" + tracker

	// 2. Stream the video to a temp file for ffmpeg frame extraction.
	srcPath, cleanupSrc, err := a.materialize(ctx, req.AssetURI, req.MIME)
	if err != nil {
		resp.Error = fmt.Sprintf("materialize video: %v", err)
		return resp, err
	}
	defer cleanupSrc()

	// 3. Extract sampled frames (ffmpeg autorotate ON → displayed pixel space,
	//    matching our coordinate convention + the frame index).
	frames, cleanupDir, err := a.extractFrames(ctx, srcPath, sampleStep, maxFrames)
	if err != nil {
		resp.Error = fmt.Sprintf("extract frames: %v", err)
		return resp, err
	}
	defer cleanupDir()
	if len(frames) == 0 {
		resp.Error = "no frames extracted"
		return resp, errors.New(resp.Error)
	}

	// 4. Per-frame /track (persist=false on frame 0 resets the tracker).
	perTrack := map[int][]kfEntry{}
	for i, fr := range frames {
		dets, terr := a.callTrack(ctx, fr.jpg, i > 0, tracker, model, conf)
		if terr != nil {
			resp.Error = fmt.Sprintf("track frame %d: %v", fr.srcFrame, terr)
			return resp, terr
		}
		tsMs := float64(fr.srcFrame) * 1000.0 / fps
		for _, d := range dets {
			if len(d.Box) != 4 {
				continue
			}
			perTrack[d.TrackID] = append(perTrack[d.TrackID], kfEntry{
				frame: fr.srcFrame,
				tsMs:  tsMs,
				bbox:  xyxyToXywh(d.Box),
				label: d.ClassName,
				score: d.Confidence,
			})
		}
	}

	// 5. Replace prior AI tracks: re-running detect_track must not pile up
	//    duplicate source:ai tracks. Archive existing active AI tracks first
	//    (human tracks and adopted tracks are untouched). MaxTrackNumber still
	//    counts archived rows, so new track numbers never collide.
	if prev, perr := a.payload.ListActiveTracksByTask(ctx, req.TaskID, paymodel.TrackSourceAI, ""); perr == nil {
		for _, p := range prev {
			_ = a.payload.SetTrackActive(ctx, p.ID, false, a.systemUser)
		}
	}

	// 6. Remap server track_ids → fresh per-task track numbers; write mm_tracks.
	base, _ := a.payload.MaxTrackNumber(ctx, req.TaskID)
	serverIDs := make([]int, 0, len(perTrack))
	for id := range perTrack {
		serverIDs = append(serverIDs, id)
	}
	sort.Ints(serverIDs) // deterministic track numbering

	written := 0
	for _, sid := range serverIDs {
		entries := perTrack[sid]
		// Drop spurious short fragments (ID-switch flickers) and weak/duplicate
		// low-confidence detections so the annotator isn't handed noise boxes.
		if len(entries) < minKf || avgScore(entries) < minScore {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].frame < entries[j].frame })
		written++
		trackNo := base + written
		kfs := make([]paymodel.Keyframe, 0, len(entries))
		for _, e := range entries {
			kfs = append(kfs, paymodel.Keyframe{
				Frame: e.frame, TsMs: e.tsMs, Bbox: e.bbox,
				Outside: false, Occluded: false, Source: paymodel.TrackSourceAI,
			})
		}
		// Map COCO class → dataset ontology label (canonical name + color) when it
		// matches; otherwise keep the raw class and flag it for manual re-annotation.
		rawClass := majorityLabel(entries)
		label, color, matched := rawClass, trackColor(trackNo), false
		if ol, ok := onto[strings.ToLower(rawClass)]; ok {
			label, matched = ol.name, true
			if ol.color != "" {
				color = ol.color
			}
		}
		t := &paymodel.Track{
			TaskID:    req.TaskID,
			DatasetID: asset.DatasetID,
			AssetID:   req.AssetID,
			TrackID:   trackNo,
			Label:     label,
			Kind:      "bbox",
			Color:     color,
			Attrs: map[string]interface{}{
				"ai_model":     a.model,
				"ai_tracker":   a.tracker,
				"ai_score":     avgScore(entries),
				"ai_class":     rawClass, // original COCO class
				"ai_unmatched": !matched, // true = not in dataset ontology, relabel needed
			},
			Keyframes: kfs,
			Source:    paymodel.TrackSourceAI,
			CreatedBy: a.systemUser,
			UpdatedBy: a.systemUser,
		}
		if err := a.payload.InsertTrack(ctx, t); err != nil {
			resp.Error = fmt.Sprintf("insert track: %v", err)
			return resp, err
		}
	}

	resp.Status = "success"
	resp.LatencyMs = time.Since(started).Milliseconds()
	resp.Raw = map[string]interface{}{
		"tracks_written": written,
		"frames_sampled": len(frames),
		"sample_step":    a.sampleStep,
	}
	return resp, nil
}

// materialize streams the asset body to a temp file ffmpeg can read.
func (a *DetTrackAdapter) materialize(ctx context.Context, uri, mime string) (string, func(), error) {
	body, err := a.reader(ctx, uri)
	if err != nil {
		return "", func() {}, err
	}
	defer body.Close()
	f, err := os.CreateTemp("", "dettrack-src-*"+extForMIME(mime))
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

type frameJPEG struct {
	srcFrame int
	jpg      []byte
}

// extractFrames samples every step-th source frame (capped at maxFrames) to
// JPEGs. Output file order j (0-based) → source frame j*step.
//
// maxFrames is the cost gate's teeth: GPU time is ~linear in the number of
// sampled frames, so this is what bounds what one click can spend.
func (a *DetTrackAdapter) extractFrames(ctx context.Context, srcPath string, step, maxFrames int) ([]frameJPEG, func(), error) {
	if step <= 0 {
		step = 1
	}
	if maxFrames <= 0 {
		maxFrames = a.maxFrames // no dataset config → adapter default
	}
	if maxFrames > videoAIMaxFramesCeiling {
		maxFrames = videoAIMaxFramesCeiling // global guard; a dataset cannot exceed it
	}
	dir, err := os.MkdirTemp("", "dettrack-frames-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	pattern := filepath.Join(dir, "f_%06d.jpg")
	// select every Nth frame; \, escapes the comma inside mod() for ffmpeg's
	// filtergraph parser. -vsync 0 keeps only the selected frames.
	vf := fmt.Sprintf("select=not(mod(n\\,%d))", step)
	args := []string{
		"-v", "error", "-i", srcPath,
		"-vf", vf, "-vsync", "0",
		"-frames:v", strconv.Itoa(maxFrames),
		"-q:v", "3", pattern,
	}
	if _, err := runCapture(ctx, a.tools.FFmpeg, args...); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]frameJPEG, 0, len(names))
	for j, name := range names {
		jpg, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			cleanup()
			return nil, func() {}, rerr
		}
		out = append(out, frameJPEG{srcFrame: j * step, jpg: jpg})
	}
	return out, cleanup, nil
}

// callTrack POSTs one frame to det-server /track.
func (a *DetTrackAdapter) callTrack(ctx context.Context, jpg []byte, persist bool, tracker, model string, conf float64) ([]trackDet, error) {
	reqBody, _ := json.Marshal(trackReq{
		ImageB64: base64.StdEncoding.EncodeToString(jpg),
		Conf:     conf,
		IoU:      a.iou,
		Tracker:  tracker,
		Model:    model,
		Persist:  persist,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/track", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	res, err := a.hc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("det-server %d: %s", res.StatusCode, msg)
	}
	var tr trackResp
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("decode track resp: %w", err)
	}
	return tr.Tracks, nil
}

// --- helpers ---------------------------------------------------------------

// ontoLabel is a dataset ontology label's canonical name + color.
type ontoLabel struct {
	name  string
	color string
}

// loadOntology parses the dataset's label ontology into a lowercased-name lookup
// so COCO class names can map to the dataset's own labels. Returns an empty map
// on any error (adapter then keeps raw COCO class names).
func (a *DetTrackAdapter) loadOntology(ctx context.Context, datasetID uint) map[string]ontoLabel {
	m := map[string]ontoLabel{}
	ds, err := a.db.FindDatasetByID(ctx, datasetID)
	if err != nil || ds == nil || ds.LabelOntology == "" {
		return m
	}
	var parsed struct {
		Labels []struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"labels"`
	}
	if json.Unmarshal([]byte(ds.LabelOntology), &parsed) != nil {
		return m
	}
	for _, l := range parsed.Labels {
		if l.Name != "" {
			m[strings.ToLower(l.Name)] = ontoLabel{name: l.Name, color: l.Color}
		}
	}
	return m
}

// extraInt coerces a CapabilityRequest.Extras value (int / int64 / float64) to int.
func extraInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// extraFloat coerces an Extras value (float64 / int / int64) to float64.
func extraFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func xyxyToXywh(b []float64) []float64 {
	// [x1,y1,x2,y2] → [x,y,w,h]; clamp width/height non-negative.
	w := b[2] - b[0]
	h := b[3] - b[1]
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return []float64{b[0], b[1], w, h}
}

func majorityLabel(entries []kfEntry) string {
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.label]++
	}
	best, bestN := "object", 0
	for l, n := range counts {
		if l != "" && n > bestN {
			best, bestN = l, n
		}
	}
	return best
}

func avgScore(entries []kfEntry) float64 {
	if len(entries) == 0 {
		return 0
	}
	var s float64
	for _, e := range entries {
		s += e.score
	}
	return s / float64(len(entries))
}

// trackColor returns a stable palette color for a track number (mirrors the
// image/video canvas palette so AI tracks look native).
func trackColor(n int) string {
	palette := []string{
		"#e6194B", "#3cb44b", "#4363d8", "#f58231", "#911eb4",
		"#42d4f4", "#f032e6", "#bfef45", "#fabed4", "#469990",
		"#dcbeff", "#9A6324", "#800000", "#aaffc3", "#808000",
	}
	if n < 1 {
		n = 1
	}
	return palette[(n-1)%len(palette)]
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
