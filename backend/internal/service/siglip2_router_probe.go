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

// SigLIPProbe is the L2 semantic routing interface. It classifies an image into
// a semantic category and returns the top category together with all scores.
// A nil probe means L2 is disabled and the router stays with its L1 gray-zone
// default.
type SigLIPProbe interface {
	Probe(ctx context.Context, asset *dbmodel.Asset) (topCategory string, scores map[string]float64, err error)
}

// sigLIPDocCategories maps top_category values that indicate heavy text content
// to OCR_FIRST routing.
var sigLIPDocCategories = map[string]bool{
	"document":    true,
	"form":        true,
	"receipt":     true,
	"invoice":     true,
	"table":       true,
	"text_heavy":  true,
	"handwriting": true,
	"slide":       true,
}

// sigLIPVLMCategories maps top_category values that indicate natural / visual
// content to VLM_FIRST routing.
var sigLIPVLMCategories = map[string]bool{
	"natural_scene": true,
	"person":        true,
	"animal":        true,
	"object":        true,
	"photo":         true,
	"artwork":       true,
	"diagram":       true,
}

// sigLIPMinConfidence is the minimum top-category score to act on. Below this
// threshold the probe result is treated as inconclusive and L1 gray default
// is kept.
const sigLIPMinConfidence = 0.60

// SigLIP2StrategyFromCategory converts a SigLIP2 top_category + score to an
// annotation routing strategy. Returns "" when the probe is inconclusive.
func SigLIP2StrategyFromCategory(topCategory string, topScore float64) string {
	if topScore < sigLIPMinConfidence {
		return ""
	}
	if sigLIPDocCategories[topCategory] {
		return dbmodel.RouteOCRFirst
	}
	if sigLIPVLMCategories[topCategory] {
		return dbmodel.RouteVLMFirst
	}
	return ""
}

// HTTPSigLIPProbe calls a SigLIP2 HTTP service to classify an image.
//
// Expected request:
//
//	POST {endpoint}/classify
//	Content-Type: application/json
//	Authorization: Bearer <api-key>   // optional
//	{
//	  "image_base64": "...",
//	  "mime": "image/jpeg",
//	  "task_id": 0,
//	  "trace_id": "l2-probe"
//	}
//
// Expected response:
//
//	{
//	  "top_category": "document",
//	  "scores": {"document": 0.85, "natural_scene": 0.08, ...},
//	  "model": "siglip2-large",
//	  "version": "..."
//	}
type HTTPSigLIPProbe struct {
	endpoint string
	apiKey   string
	timeout  time.Duration
	hc       *http.Client
	reader   AssetReader
}

// SigLIPProbeConfig captures the wiring options for HTTPSigLIPProbe.
type SigLIPProbeConfig struct {
	Endpoint string
	APIKey   string
	Timeout  time.Duration
	Reader   AssetReader
}

// NewHTTPSigLIPProbe returns nil when endpoint or reader is missing, preserving
// the nil-probe-equals-no-probe contract used by OCRDetProbe.
func NewHTTPSigLIPProbe(cfg SigLIPProbeConfig) *HTTPSigLIPProbe {
	if cfg.Endpoint == "" || cfg.Reader == nil {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &HTTPSigLIPProbe{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		apiKey:   cfg.APIKey,
		timeout:  cfg.Timeout,
		hc:       &http.Client{Timeout: cfg.Timeout},
		reader:   cfg.Reader,
	}
}

// Probe implements SigLIPProbe.
func (p *HTTPSigLIPProbe) Probe(ctx context.Context, asset *dbmodel.Asset) (string, map[string]float64, error) {
	if p == nil {
		return "", nil, errors.New("nil siglip probe")
	}
	if asset == nil || asset.StorageURI == "" {
		return "", nil, errors.New("asset missing storage uri")
	}

	body, err := p.reader(ctx, asset.StorageURI)
	if err != nil {
		return "", nil, fmt.Errorf("read asset: %w", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", nil, fmt.Errorf("read body: %w", err)
	}

	mime := asset.MIME
	if mime == "" {
		mime = "image/jpeg"
	}
	payload := map[string]interface{}{
		"image_base64": base64.StdEncoding.EncodeToString(raw),
		"mime":         mime,
		"task_id":      0,
		"trace_id":     "l2-probe",
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshal siglip req: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/classify", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("new siglip req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	httpResp, err := p.hc.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("siglip call: %w", err)
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("siglip http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
	}

	var parsed struct {
		TopCategory string             `json:"top_category"`
		Scores      map[string]float64 `json:"scores"`
	}
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		return "", nil, fmt.Errorf("decode siglip resp: %w", err)
	}
	return parsed.TopCategory, parsed.Scores, nil
}
