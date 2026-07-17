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

// OCRHTTPAdapter implements CapabilityAdapter for ocr.detection / ocr.structure
// via a thin HTTP wrapper around PaddleOCR (or any OpenAPI-compatible OCR
// service).
//
// Expected request:
//
//	POST {endpoint}/ocr
//	Content-Type: application/json
//	Authorization: Bearer <api-key>     // optional
//	{
//	  "image_base64": "...",
//	  "mime": "image/jpeg",
//	  "task_id": 123,
//	  "trace_id": "..."
//	}
//
// Expected response:
//
//	{
//	  "boxes": [
//	    {"x":0,"y":0,"w":100,"h":30,"text":"...","confidence":0.95,
//	     "polygon":[[x1,y1],[x2,y2],[x3,y3],[x4,y4]]}
//	  ],
//	  "structured_md": "...",
//	  "structured_json": {...},
//	  "model": "PP-OCRv4",
//	  "version": "..."
//	}
type OCRHTTPAdapter struct {
	capability  string
	endpoint    string
	apiKey      string
	model       string // model id reported in trace; can be overridden by response
	provider    string
	timeout     time.Duration
	hc          *http.Client
	reader      AssetReader
}

// OCRAdapterConfig captures the wiring options for a single OCR adapter.
type OCRAdapterConfig struct {
	Capability   string
	Endpoint     string
	APIKey       string
	Model        string
	ProviderName string
	Timeout      time.Duration
	Reader       AssetReader
}

// NewOCRHTTPAdapter constructs an OCR HTTP adapter.
func NewOCRHTTPAdapter(cfg OCRAdapterConfig) *OCRHTTPAdapter {
	if cfg.Capability == "" {
		cfg.Capability = CapabilityOCRStructure
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "paddleocr"
	}
	if cfg.Model == "" {
		cfg.Model = "PP-OCRv4"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	return &OCRHTTPAdapter{
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
func (a *OCRHTTPAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter has a usable endpoint.
func (a *OCRHTTPAdapter) Configured() bool { return a != nil && a.endpoint != "" }

// Invoke implements CapabilityAdapter.
func (a *OCRHTTPAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
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
		resp.Error = "ocr endpoint not configured"
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
		resp.Error = fmt.Sprintf("marshal ocr req: %v", err)
		return resp, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/ocr", bytes.NewReader(bodyBytes))
	if err != nil {
		resp.Error = fmt.Sprintf("new ocr req: %v", err)
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
		resp.Error = fmt.Sprintf("ocr call: %v", err)
		return resp, err
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		resp.Error = fmt.Sprintf("ocr http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
		return resp, errors.New(resp.Error)
	}

	var parsed ocrResponse
	if err := json.Unmarshal(rawResp, &parsed); err != nil {
		resp.Error = fmt.Sprintf("decode ocr resp: %v", err)
		return resp, err
	}

	if parsed.Model != "" {
		resp.Provider.ModelID = parsed.Model
	}
	if parsed.Version != "" {
		resp.Provider.Version = parsed.Version
	}

	ocr := &paymodel.OCRResult{
		RunID:          req.RunID,
		TaskID:         req.TaskID,
		AssetID:        req.AssetID,
		TraceID:        req.TraceID,
		Provider:       resp.Provider,
		Boxes:          make([]paymodel.OCRBox, 0, len(parsed.Boxes)),
		StructuredJSON: parsed.StructuredJSON,
		StructuredMD:   parsed.StructuredMD,
		RawResponse:    json.RawMessage(rawResp),
		LatencyMs:      resp.LatencyMs,
		Status:         "success",
	}
	for _, b := range parsed.Boxes {
		ocr.Boxes = append(ocr.Boxes, paymodel.OCRBox{
			X:          b.X,
			Y:          b.Y,
			Width:      b.W,
			Height:     b.H,
			Text:       b.Text,
			Confidence: b.Confidence,
			Polygon:    b.Polygon,
		})
	}
	resp.OCR = ocr
	resp.Status = "success"
	return resp, nil
}

type ocrBoxJSON struct {
	X          float64       `json:"x"`
	Y          float64       `json:"y"`
	W          float64       `json:"w"`
	H          float64       `json:"h"`
	Text       string        `json:"text"`
	Confidence float64       `json:"confidence"`
	Polygon    [][]float64   `json:"polygon"`
}

type ocrResponse struct {
	Boxes          []ocrBoxJSON           `json:"boxes"`
	StructuredJSON map[string]interface{} `json:"structured_json"`
	StructuredMD   string                 `json:"structured_md"`
	Model          string                 `json:"model"`
	Version        string                 `json:"version"`
}
