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

	dbmodel "text-annotation-platform/internal/model/relational"
)

// OCRDetProbe extracts the cheap OCR-detection-only features used by the L1
// router: how many text boxes the OCR engine sees and how much of the image
// area those boxes cover. Implementations call the same ocr-server endpoint
// as OCRHTTPAdapter but ignore everything except box geometry, which is
// strictly cheaper than full recognition because callers do not need the
// text strings.
//
// A nil probe means routing falls back to asset-only metadata
// (box_count=0, text_area_ratio=0) — the pre-probe behaviour from plan_v1/06
// §11.0 known gap.
type OCRDetProbe interface {
	Probe(ctx context.Context, asset *dbmodel.Asset) (boxCount int, textAreaRatio float64, err error)
}

// HTTPOCRDetProbe calls the ocr-server /ocr endpoint and computes geometry
// features from the returned boxes. It reuses the contract documented on
// OCRHTTPAdapter but discards rec_texts / structured output.
type HTTPOCRDetProbe struct {
	endpoint string
	apiKey   string
	timeout  time.Duration
	hc       *http.Client
	reader   AssetReader
}

// OCRDetProbeConfig captures the wiring for HTTPOCRDetProbe.
type OCRDetProbeConfig struct {
	Endpoint string
	APIKey   string
	Timeout  time.Duration
	Reader   AssetReader
}

// NewHTTPOCRDetProbe returns nil when no endpoint is configured so callers
// can use the nil-probe-equals-no-probe contract.
func NewHTTPOCRDetProbe(cfg OCRDetProbeConfig) *HTTPOCRDetProbe {
	if cfg.Endpoint == "" || cfg.Reader == nil {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &HTTPOCRDetProbe{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		apiKey:   cfg.APIKey,
		timeout:  cfg.Timeout,
		hc:       &http.Client{Timeout: cfg.Timeout},
		reader:   cfg.Reader,
	}
}

// Probe implements OCRDetProbe.
func (p *HTTPOCRDetProbe) Probe(ctx context.Context, asset *dbmodel.Asset) (int, float64, error) {
	if p == nil {
		return 0, 0, errors.New("nil probe")
	}
	if asset == nil || asset.StorageURI == "" {
		return 0, 0, errors.New("asset missing storage uri")
	}

	body, err := p.reader(ctx, asset.StorageURI)
	if err != nil {
		return 0, 0, fmt.Errorf("read asset: %w", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return 0, 0, fmt.Errorf("read body: %w", err)
	}

	mime := asset.MIME
	if mime == "" {
		mime = "image/jpeg"
	}
	payload := map[string]interface{}{
		"image_base64": base64.StdEncoding.EncodeToString(raw),
		"mime":         mime,
		"task_id":      0,
		"trace_id":     "router-probe",
		"capability":   CapabilityOCRDetection,
	}
	if asset.Width > 0 {
		payload["width"] = asset.Width
	}
	if asset.Height > 0 {
		payload["height"] = asset.Height
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return 0, 0, fmt.Errorf("marshal probe req: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/ocr", bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, 0, fmt.Errorf("new probe req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	httpResp, err := p.hc.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("probe call: %w", err)
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return 0, 0, fmt.Errorf("probe http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
	}

	var parsed struct {
		Boxes []struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
			W float64 `json:"w"`
			H float64 `json:"h"`
		} `json:"boxes"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return 0, 0, fmt.Errorf("decode probe resp: %w", err)
	}

	boxCount := len(parsed.Boxes)
	if asset.Width <= 0 || asset.Height <= 0 {
		// Without canvas dimensions, ratio is meaningless; return count only.
		return boxCount, 0, nil
	}
	canvas := float64(asset.Width) * float64(asset.Height)
	var area float64
	for _, b := range parsed.Boxes {
		if b.W > 0 && b.H > 0 {
			area += b.W * b.H
		}
	}
	ratio := area / canvas
	if ratio > 1 {
		// Cap pathological cases where boxes overlap or extend off-canvas.
		ratio = 1
	}
	return boxCount, ratio, nil
}
