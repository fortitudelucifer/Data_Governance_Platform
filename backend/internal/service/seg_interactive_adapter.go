package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// SAMInteractiveAdapter implements CapabilityAdapter for seg.interactive via
// the sam-server MobileSAM FastAPI service. Unlike the batch seg.instance
// adapter (YOLOv8), this adapter is driven by user-supplied point prompts
// and returns a single best-scoring mask polygon for workbench preview.
//
// sam-server request:
//
//	POST {endpoint}/segment
//	{ "image_b64": "<base64>", "points": [[x,y,label],...], "box": [x1,y1,x2,y2] }
//
// sam-server response:
//
//	{ "polygons": [[x1,y1,x2,y2,...]], "score": 0.93, "mask_png_b64": "<base64>" }
//
// The caller passes point prompts via req.Extras["points"] ([][]float64) and
// an optional box prompt via req.Extras["box"] ([]float64).
// The mask_png_b64 and raw score are forwarded in resp.Raw for the HTTP handler
// to return directly to the frontend.
type SAMInteractiveAdapter struct {
	endpoint string
	apiKey   string
	timeout  time.Duration
	hc       *http.Client
	reader   AssetReader
}

// SAMAdapterConfig captures the wiring options for the SAM interactive adapter.
type SAMAdapterConfig struct {
	Endpoint string
	APIKey   string
	Timeout  time.Duration
	Reader   AssetReader
}

// NewSAMInteractiveAdapter constructs the adapter.
func NewSAMInteractiveAdapter(cfg SAMAdapterConfig) *SAMInteractiveAdapter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &SAMInteractiveAdapter{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		apiKey:   cfg.APIKey,
		timeout:  cfg.Timeout,
		hc:       &http.Client{Timeout: cfg.Timeout},
		reader:   cfg.Reader,
	}
}

// Capability implements CapabilityAdapter.
func (a *SAMInteractiveAdapter) Capability() string { return CapabilitySegInteractive }

// Configured reports whether the adapter has a usable endpoint.
func (a *SAMInteractiveAdapter) Configured() bool { return a != nil && a.endpoint != "" }

type samSegRequest struct {
	ImageB64 string      `json:"image_b64"`
	Points   [][]float64 `json:"points"`
	Box      []float64   `json:"box,omitempty"`
}

type samSegResponse struct {
	Polygons   [][]float64 `json:"polygons"`
	Score      float64     `json:"score"`
	MaskPNGB64 string      `json:"mask_png_b64"`
}

// SAMResult is embedded in CapabilityResponse.Raw so the handler can return
// it directly without re-parsing the SegResult.
type SAMResult struct {
	Polygons   [][]float64 `json:"polygons"`
	Score      float64     `json:"score"`
	MaskPNGB64 string      `json:"mask_png_b64"`
}

// Invoke implements CapabilityAdapter.
func (a *SAMInteractiveAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   "mobile-sam",
			ModelID:        "vit_t",
			CapabilityType: CapabilitySegInteractive,
			EndpointMode:   EndpointModeAdapter,
		},
	}
	if a.endpoint == "" {
		resp.Error = "SAM endpoint not configured"
		return resp, errors.New(resp.Error)
	}

	// Image source: for images the adapter reads the static asset; for video the
	// caller supplies the current frame directly via Extras["image_b64"] (the
	// exact displayed pixels, so the mask lands in the canvas coordinate space).
	var imgB64 string
	if s, ok := req.Extras["image_b64"].(string); ok && s != "" {
		imgB64 = s
	} else {
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
		imgB64 = base64.StdEncoding.EncodeToString(raw)
	}

	points := extractPoints(req.Extras)
	if len(points) == 0 {
		resp.Error = "no points provided in extras"
		return resp, errors.New(resp.Error)
	}
	// SAM 期望每个点为 [x, y, label]（label 1=前景 / 0=背景）。前端通常只传 [x, y]，
	// 这里补默认前景标签 1，否则 SAM 端 p[2] 会越界（IndexError → 500）。
	for i, p := range points {
		if len(p) == 2 {
			points[i] = []float64{p[0], p[1], 1}
		}
	}
	box := extractBox(req.Extras)

	samReq := samSegRequest{
		ImageB64: imgB64,
		Points:   points,
		Box:      box,
	}
	bodyBytes, err := json.Marshal(samReq)
	if err != nil {
		resp.Error = fmt.Sprintf("marshal sam req: %v", err)
		return resp, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/segment", bytes.NewReader(bodyBytes))
	if err != nil {
		resp.Error = fmt.Sprintf("new sam req: %v", err)
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
		resp.Error = fmt.Sprintf("sam call: %v", err)
		return resp, err
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		resp.Error = fmt.Sprintf("sam http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
		return resp, errors.New(resp.Error)
	}

	var parsed samSegResponse
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		resp.Error = fmt.Sprintf("decode sam resp: %v", err)
		return resp, err
	}

	// The sam-server "polygons" field is only the mask bounding box; trace the
	// real contour from mask_png_b64 for a precise outline (fall back to the box).
	usePolys := parsed.Polygons
	if traced := maskToPolygon(parsed.MaskPNGB64); len(traced) >= 12 {
		usePolys = [][]float64{traced}
	}

	// Convert flat polygons [x1,y1,x2,y2,...] → [[x1,y1],[x2,y2],...]
	seg := &paymodel.SegResult{
		RunID:       req.RunID,
		TaskID:      req.TaskID,
		AssetID:     req.AssetID,
		TraceID:     req.TraceID,
		Provider:    resp.Provider,
		Polygons:    make([]paymodel.SegPolygon, 0, len(usePolys)),
		RawResponse: json.RawMessage(rawResp),
		LatencyMs:   resp.LatencyMs,
		Status:      "success",
	}
	for _, flatPoly := range usePolys {
		pts := make([][]float64, 0, len(flatPoly)/2)
		for i := 0; i+1 < len(flatPoly); i += 2 {
			pts = append(pts, []float64{flatPoly[i], flatPoly[i+1]})
		}
		seg.Polygons = append(seg.Polygons, paymodel.SegPolygon{
			ClassName: "",
			ClassID:   -1,
			Points:    pts,
			Score:     parsed.Score,
		})
	}
	resp.Seg = seg
	resp.Raw = SAMResult{
		Polygons:   usePolys,
		Score:      parsed.Score,
		MaskPNGB64: parsed.MaskPNGB64,
	}
	resp.Status = "success"
	return resp, nil
}

// extractPoints pulls [][]float64 from extras["points"], handling both typed
// slices (from Go callers) and []interface{} (from JSON-decoded request bodies).
func extractPoints(extras map[string]interface{}) [][]float64 {
	if extras == nil {
		return nil
	}
	p, ok := extras["points"]
	if !ok {
		return nil
	}
	switch v := p.(type) {
	case [][]float64:
		return v
	case []interface{}:
		var out [][]float64
		for _, row := range v {
			if r, ok := row.([]interface{}); ok {
				var pt []float64
				for _, c := range r {
					if f, ok := c.(float64); ok {
						pt = append(pt, f)
					}
				}
				if len(pt) >= 3 {
					out = append(out, pt)
				}
			}
		}
		return out
	}
	return nil
}

// maskToPolygon traces the outline of a SAM mask PNG into a simplified polygon
// (flat [x1,y1,x2,y2,...] in mask pixel coords). The sam-server only returns the
// mask's bounding box in its "polygons" field, so we derive the real shape here.
//
// Method: per-row left/right envelope (silhouette) → closed loop → Ramer-Douglas-
// Peucker simplification. Robust (no boundary-following edge cases / infinite
// loops); captures vertical shape well. Annotators refine on the canvas.
func maskToPolygon(maskB64 string) []float64 {
	if maskB64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(maskB64)
	if err != nil {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	if w < 3 || h < 3 {
		return nil
	}
	// Decide foreground by alpha if the image uses it, else by luminance.
	hasAlpha := false
	for y := 0; y < h && !hasAlpha; y++ {
		for x := 0; x < w; x++ {
			if _, _, _, a := img.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA(); (a >> 8) < 250 {
				hasAlpha = true
				break
			}
		}
	}
	on := func(x, y int) bool {
		r, g, b, a := img.At(bnd.Min.X+x, bnd.Min.Y+y).RGBA()
		if hasAlpha {
			return (a >> 8) > 127
		}
		return (299*(r>>8)+587*(g>>8)+114*(b>>8))/1000 > 127
	}
	// Per-row min/max x of foreground.
	type span struct{ lo, hi, y int }
	rows := make([]span, 0, h)
	for y := 0; y < h; y++ {
		lo, hi := -1, -1
		for x := 0; x < w; x++ {
			if on(x, y) {
				if lo < 0 {
					lo = x
				}
				hi = x
			}
		}
		if lo >= 0 {
			rows = append(rows, span{lo, hi, y})
		}
	}
	if len(rows) < 2 {
		return nil
	}
	// Closed loop: left edge top→bottom, then right edge bottom→top.
	loop := make([][2]int, 0, len(rows)*2)
	for _, r := range rows {
		loop = append(loop, [2]int{r.lo, r.y})
	}
	for i := len(rows) - 1; i >= 0; i-- {
		loop = append(loop, [2]int{rows[i].hi, rows[i].y})
	}
	simp := rdp(loop, 2.5)
	if len(simp) < 3 {
		return nil
	}
	out := make([]float64, 0, len(simp)*2)
	for _, p := range simp {
		out = append(out, float64(p[0]), float64(p[1]))
	}
	return out
}

// rdp is Ramer-Douglas-Peucker polyline simplification (epsilon in pixels).
func rdp(pts [][2]int, eps float64) [][2]int {
	if len(pts) < 3 {
		return pts
	}
	a, b := pts[0], pts[len(pts)-1]
	idx, maxD := 0, 0.0
	for i := 1; i < len(pts)-1; i++ {
		if d := perpDist(pts[i], a, b); d > maxD {
			maxD, idx = d, i
		}
	}
	if maxD > eps {
		left := rdp(pts[:idx+1], eps)
		right := rdp(pts[idx:], eps)
		return append(left[:len(left)-1], right...)
	}
	return [][2]int{a, b}
}

// perpDist is the perpendicular distance from point p to segment a-b.
func perpDist(p, a, b [2]int) float64 {
	ax, ay, bx, by := float64(a[0]), float64(a[1]), float64(b[0]), float64(b[1])
	px, py := float64(p[0]), float64(p[1])
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	return math.Abs(dy*px-dx*py+bx*ay-by*ax) / math.Hypot(dx, dy)
}

// extractBox pulls []float64 from extras["box"].
func extractBox(extras map[string]interface{}) []float64 {
	if extras == nil {
		return nil
	}
	b, ok := extras["box"]
	if !ok {
		return nil
	}
	switch v := b.(type) {
	case []float64:
		return v
	case []interface{}:
		var out []float64
		for _, c := range v {
			if f, ok := c.(float64); ok {
				out = append(out, f)
			}
		}
		if len(out) == 4 {
			return out
		}
	}
	return nil
}
