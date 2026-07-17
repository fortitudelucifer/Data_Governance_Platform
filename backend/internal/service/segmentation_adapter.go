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
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// SegmentationHTTPAdapter implements CapabilityAdapter for seg.instance via a
// thin HTTP wrapper around YOLOv8-seg (seg-server). It produces polygon
// contours for COCO-80 objects, used to pre-label car / pedestrian / etc.
// outlines that humans then refine in the polygon canvas.
//
// Expected request:
//
//	POST {endpoint}/segment
//	{ "image_base64": "...", "mime": "image/jpeg", "task_id": 1,
//	  "trace_id": "...", "capability": "seg.instance", "width": W, "height": H }
//
// Expected response:
//
//	{ "polygons": [ {"class_name":"car","class_id":2,
//	                 "points":[[x,y],...], "bbox":[x,y,w,h], "score":0.93} ],
//	  "model": "yolov8m-seg", "version": "ultralytics-8.3.39" }
type SegmentationHTTPAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	model      string
	provider   string
	timeout    time.Duration
	hc         *http.Client
	reader     AssetReader
}

// SegAdapterConfig captures the wiring options for the segmentation adapter.
type SegAdapterConfig struct {
	Capability   string
	Endpoint     string
	APIKey       string
	Model        string
	ProviderName string
	Timeout      time.Duration
	Reader       AssetReader
}

// NewSegmentationHTTPAdapter constructs the adapter.
func NewSegmentationHTTPAdapter(cfg SegAdapterConfig) *SegmentationHTTPAdapter {
	if cfg.Capability == "" {
		cfg.Capability = CapabilitySegInstance
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "yolov8-seg"
	}
	if cfg.Model == "" {
		cfg.Model = "yolov8m-seg"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	return &SegmentationHTTPAdapter{
		capability: cfg.Capability,
		endpoint:   strings.TrimRight(cfg.Endpoint, "/"),
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		provider:   cfg.ProviderName,
		timeout:    cfg.Timeout,
		hc:         &http.Client{Timeout: cfg.Timeout},
		reader:     cfg.Reader,
	}
}

// Capability implements CapabilityAdapter.
func (a *SegmentationHTTPAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter has a usable endpoint.
func (a *SegmentationHTTPAdapter) Configured() bool { return a != nil && a.endpoint != "" }

type segPolygonJSON struct {
	ClassName string      `json:"class_name"`
	ClassID   int         `json:"class_id"`
	Points    [][]float64 `json:"points"`
	BBox      []float64   `json:"bbox"`
	Score     float64     `json:"score"`
}

type segResponseJSON struct {
	Polygons []segPolygonJSON `json:"polygons"`
	Model    string           `json:"model"`
	Version  string           `json:"version"`
}

// Invoke implements CapabilityAdapter.
func (a *SegmentationHTTPAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   a.provider,
			ModelID:        a.model,
			CapabilityType: a.capability,
			EndpointMode:   EndpointModeAdapter,
		},
	}
	if a.endpoint == "" {
		resp.Error = "seg endpoint not configured"
		return resp, errors.New(resp.Error)
	}
	if a.reader == nil {
		resp.Error = "asset reader not bound"
		return resp, errors.New(resp.Error)
	}

	body, err := a.reader(ctx, req.AssetURI)
	if err != nil {
		resp.Error = fmt.Sprintf("read asset: %v", err)
		return resp, err
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		resp.Error = fmt.Sprintf("read body: %v", err)
		return resp, err
	}

	mime := req.MIME
	if mime == "" {
		mime = "image/jpeg"
	}
	payload := map[string]interface{}{
		"image_base64": base64.StdEncoding.EncodeToString(raw),
		"mime":         mime,
		"task_id":      req.TaskID,
		"trace_id":     req.TraceID,
		"capability":   req.CapabilityType,
	}
	if req.Width > 0 {
		payload["width"] = req.Width
	}
	if req.Height > 0 {
		payload["height"] = req.Height
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		resp.Error = fmt.Sprintf("marshal seg req: %v", err)
		return resp, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/segment", bytes.NewReader(bodyBytes))
	if err != nil {
		resp.Error = fmt.Sprintf("new seg req: %v", err)
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	start := time.Now()
	httpResp, err := a.hc.Do(httpReq)
	resp.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		resp.Error = fmt.Sprintf("seg call: %v", err)
		return resp, err
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		resp.Error = fmt.Sprintf("seg http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
		return resp, errors.New(resp.Error)
	}

	var parsed segResponseJSON
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		resp.Error = fmt.Sprintf("decode seg resp: %v", err)
		return resp, err
	}
	if parsed.Model != "" {
		resp.Provider.ModelID = parsed.Model
	}
	if parsed.Version != "" {
		resp.Provider.Version = parsed.Version
	}

	seg := &paymodel.SegResult{
		RunID:       req.RunID,
		TaskID:      req.TaskID,
		AssetID:     req.AssetID,
		TraceID:     req.TraceID,
		Provider:    resp.Provider,
		Polygons:    make([]paymodel.SegPolygon, 0, len(parsed.Polygons)),
		RawResponse: json.RawMessage(rawResp),
		LatencyMs:   resp.LatencyMs,
		Status:      "success",
	}
	for _, p := range parsed.Polygons {
		seg.Polygons = append(seg.Polygons, paymodel.SegPolygon{
			ClassName: p.ClassName,
			ClassID:   p.ClassID,
			Points:    p.Points,
			BBox:      p.BBox,
			Score:     p.Score,
		})
	}
	resp.Seg = seg
	resp.Status = "success"
	return resp, nil
}
