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

// ASRHTTPAdapter implements CapabilityAdapter for asr.transcribe via a thin HTTP
// wrapper around the FunASR sidecar (asr-server). It reads the audio bytes and
// POSTs them as multipart/form-data to {endpoint}/transcribe.
//
// Expected response:
//
//	{
//	  "request_id": "...", "model": "paraformer-large", "model_version": "...",
//	  "language": "zh", "duration_ms": 9587,
//	  "segments": [
//	    {"start_ms":1200,"end_ms":3400,"text":"你好","speaker":"spk0","confidence":0.9}
//	  ]
//	}
//
// See plan_v2 执行方案-01 A2.
type ASRHTTPAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	model      string
	provider   string
	timeout    time.Duration
	hc         *http.Client
	reader     AssetReader
}

// ASRAdapterConfig captures the wiring options for the ASR adapter.
type ASRAdapterConfig struct {
	Endpoint     string
	APIKey       string
	Model        string
	ProviderName string
	Timeout      time.Duration
	Reader       AssetReader
}

// NewASRHTTPAdapter constructs an ASR HTTP adapter.
func NewASRHTTPAdapter(cfg ASRAdapterConfig) *ASRHTTPAdapter {
	if cfg.ProviderName == "" {
		cfg.ProviderName = "funasr"
	}
	if cfg.Model == "" {
		cfg.Model = "paraformer-large"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 300 * time.Second // ASR on long audio can be slow
	}
	return &ASRHTTPAdapter{
		capability: CapabilityASRTranscribe,
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
func (a *ASRHTTPAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter has a usable endpoint.
func (a *ASRHTTPAdapter) Configured() bool { return a != nil && a.endpoint != "" }

// Invoke implements CapabilityAdapter.
func (a *ASRHTTPAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
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
		resp.Error = "asr endpoint not configured"
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

	// multipart/form-data: file + metadata.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "audio"+extForMIME(req.MIME))
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
	_ = mw.Close()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/transcribe", &buf)
	if err != nil {
		resp.Error = fmt.Sprintf("new asr req: %v", err)
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
		resp.Error = fmt.Sprintf("asr call: %v", err)
		return resp, err
	}
	defer httpResp.Body.Close()
	rawResp, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		resp.Error = fmt.Sprintf("asr http %d: %s", httpResp.StatusCode, truncate(string(rawResp), 256))
		return resp, errors.New(resp.Error)
	}

	asr, err := parseASRResponse(rawResp)
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	if asr.Model != "" {
		resp.Provider.ModelID = asr.Model
	}
	if asr.ModelVersion != "" {
		resp.Provider.Version = asr.ModelVersion
	}

	result := &paymodel.ASRResult{
		RunID:       req.RunID,
		TaskID:      req.TaskID,
		AssetID:     req.AssetID,
		TraceID:     req.TraceID,
		Provider:    resp.Provider,
		Language:    asr.Language,
		DurationMs:  asr.DurationMs,
		Segments:    make([]paymodel.ASRSegment, 0, len(asr.Segments)),
		RawResponse: json.RawMessage(rawResp),
		LatencyMs:   resp.LatencyMs,
		Status:      "success",
	}
	for _, s := range asr.Segments {
		result.Segments = append(result.Segments, paymodel.ASRSegment{
			StartMs:       s.StartMs,
			EndMs:         s.EndMs,
			Text:          strings.TrimSpace(s.Text),
			Speaker:       s.Speaker,
			Confidence:    s.Confidence,
			Emotion:       s.Emotion,
			EmotionScores: s.EmotionScores,
		})
	}
	resp.ASR = result
	resp.Status = "success"
	return resp, nil
}

type asrSegmentJSON struct {
	StartMs       int64              `json:"start_ms"`
	EndMs         int64              `json:"end_ms"`
	Text          string             `json:"text"`
	Speaker       string             `json:"speaker"`
	Confidence    float64            `json:"confidence"`
	Emotion       string             `json:"emotion"`
	EmotionScores map[string]float64 `json:"emotion_scores"`
}

type asrResponseJSON struct {
	RequestID    string           `json:"request_id"`
	Model        string           `json:"model"`
	ModelVersion string           `json:"model_version"`
	Language     string           `json:"language"`
	DurationMs   int64            `json:"duration_ms"`
	Segments     []asrSegmentJSON `json:"segments"`
}

// parseASRResponse decodes the asr-server response (exposed for unit tests).
func parseASRResponse(raw []byte) (*asrResponseJSON, error) {
	var parsed asrResponseJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode asr resp: %w", err)
	}
	return &parsed, nil
}

// extForMIME maps an audio MIME to a file extension the sidecar/ffmpeg can read.
func extForMIME(mime string) string {
	switch {
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "ogg"):
		return ".ogg"
	case strings.Contains(mime, "flac"):
		return ".flac"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"), strings.Contains(mime, "aac"):
		return ".m4a"
	default:
		return ".wav"
	}
}
