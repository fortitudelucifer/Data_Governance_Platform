package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// MediaHTTPAdapter is a generic CapabilityAdapter for audio/video model
// sidecars registered through 能力配置 (DB providers). It POSTs the asset bytes
// as multipart/form-data to {endpoint}{path} (default /infer) and understands a
// deliberately small, reusable response contract:
//
//	{
//	  "model": "...", "model_version": "...", "language": "zh", "duration_ms": 9587,
//	  "segments": [ {"start_ms":1200,"end_ms":3400,"text":"…","speaker":"spk0","confidence":0.9} ]
//	}
//
// When `segments` are present they are mapped to an ASRResult so the audio
// workspace seeds them as `audio_region` shapes (annotator adopts/edits) —
// identical to the ASR path. The full JSON is always preserved in Raw for
// capabilities whose output the workspace does not yet render (e.g. video —
// pending Phase B). This lets a local audio/video model be onboarded from the
// UI with zero code once its capability_type starts with `audio.`/`video.`.
//
// asr.transcribe keeps its dedicated ASRHTTPAdapter (fixed /transcribe path);
// this generic adapter serves the other audio.* / video.* families.
type MediaHTTPAdapter struct {
	capability string
	endpoint   string
	path       string
	apiKey     string
	model      string
	provider   string
	timeout    time.Duration
	hc         *http.Client
	reader     AssetReader
}

// MediaAdapterConfig captures the wiring options for the generic media adapter.
type MediaAdapterConfig struct {
	Capability   string
	Endpoint     string
	Path         string // default "/infer"
	APIKey       string
	Model        string
	ProviderName string
	Timeout      time.Duration
	Reader       AssetReader
}

// NewMediaHTTPAdapter constructs a generic audio/video HTTP adapter.
func NewMediaHTTPAdapter(cfg MediaAdapterConfig) *MediaHTTPAdapter {
	if cfg.Path == "" {
		cfg.Path = "/infer"
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "media"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 300 * time.Second
	}
	return &MediaHTTPAdapter{
		capability: cfg.Capability,
		endpoint:   strings.TrimRight(cfg.Endpoint, "/"),
		path:       "/" + strings.TrimLeft(cfg.Path, "/"),
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		provider:   cfg.ProviderName,
		timeout:    cfg.Timeout,
		hc:         &http.Client{Timeout: cfg.Timeout},
		reader:     cfg.Reader,
	}
}

// Capability implements CapabilityAdapter.
func (a *MediaHTTPAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter has a usable endpoint.
func (a *MediaHTTPAdapter) Configured() bool { return a != nil && a.endpoint != "" }

// Invoke implements CapabilityAdapter.
func (a *MediaHTTPAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
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
		resp.Error = "media endpoint not configured"
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

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "media"+extForMIME(req.MIME))
	if err != nil {
		resp.Error = fmt.Sprintf("multipart: %v", err)
		return resp, err
	}
	if _, err := fw.Write(raw); err != nil {
		resp.Error = fmt.Sprintf("multipart write: %v", err)
		return resp, err
	}
	_ = mw.WriteField("request_id", req.RunID)
	_ = mw.WriteField("task_id", strconv.FormatUint(uint64(req.TaskID), 10))
	_ = mw.WriteField("trace_id", req.TraceID)
	_ = mw.WriteField("capability", a.capability)
	if m := req.Model; m != "" {
		_ = mw.WriteField("model", m)
	} else if a.model != "" {
		_ = mw.WriteField("model", a.model)
	}
	_ = mw.Close()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+a.path, &buf)
	if err != nil {
		resp.Error = fmt.Sprintf("new media req: %v", err)
		return resp, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	if a.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
	}

	start := time.Now()
	httpResp, err := a.hc.Do(httpReq)
	resp.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		resp.Error = fmt.Sprintf("media call: %v", err)
		return resp, err
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		resp.Error = fmt.Sprintf("media http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
		return resp, errors.New(resp.Error)
	}

	// Best-effort segment mapping (shared contract with ASR). Non-segment
	// payloads still succeed and are preserved in Raw.
	if parsed, perr := parseASRResponse(rawResp); perr == nil {
		if parsed.Model != "" {
			resp.Provider.ModelID = parsed.Model
		}
		if parsed.ModelVersion != "" {
			resp.Provider.Version = parsed.ModelVersion
		}
		if len(parsed.Segments) > 0 {
			result := &paymodel.ASRResult{
				RunID:       req.RunID,
				TaskID:      req.TaskID,
				AssetID:     req.AssetID,
				TraceID:     req.TraceID,
				Provider:    resp.Provider,
				Language:    parsed.Language,
				DurationMs:  parsed.DurationMs,
				Segments:    make([]paymodel.ASRSegment, 0, len(parsed.Segments)),
				RawResponse: json.RawMessage(rawResp),
				LatencyMs:   resp.LatencyMs,
				Status:      "success",
			}
			for _, s := range parsed.Segments {
				result.Segments = append(result.Segments, paymodel.ASRSegment{
					StartMs:    s.StartMs,
					EndMs:      s.EndMs,
					Text:       strings.TrimSpace(s.Text),
					Speaker:    s.Speaker,
					Confidence: s.Confidence,
				})
			}
			resp.ASR = result
		}
	}
	resp.Raw = json.RawMessage(rawResp)
	resp.Status = "success"
	return resp, nil
}
