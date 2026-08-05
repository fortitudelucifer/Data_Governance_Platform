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
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// QwenAudioAdapter implements CapabilityAdapter for audio.transcribe via the
// qwen-audio sidecar (Qwen2.5-Omni-7B). Unlike FunASR (asr.transcribe, which
// returns timestamped segments), Qwen2.5-Omni returns a single whole-clip
// transcription. We wrap it as one ASR segment spanning [0, duration] so it
// drops into the same audio-annotation region seeding — the annotator then
// splits/refines. Registered under audio.transcribe so it shows in the audio
// workspace's capability picker alongside asr.transcribe.
//
// qwen-audio contract (http://<host>:8383):
//
//	POST /infer {audio_b64, text, max_new_tokens} → {text, inference_ms, model}
type QwenAudioAdapter struct {
	capability string
	endpoint   string
	apiKey     string
	provider   string
	prompt     string
	maxTokens  int

	timeout time.Duration
	hc      *http.Client
	reader  AssetReader
	db   *repository.DB
}

// QwenAudioAdapterConfig wires the adapter.
type QwenAudioAdapterConfig struct {
	Endpoint     string
	APIKey       string
	ProviderName string
	Prompt       string
	MaxTokens    int
	Timeout      time.Duration
	Reader       AssetReader
	DB        *repository.DB
}

// NewQwenAudioAdapter constructs the adapter.
func NewQwenAudioAdapter(cfg QwenAudioAdapterConfig) *QwenAudioAdapter {
	if cfg.ProviderName == "" {
		cfg.ProviderName = "qwen2.5-omni"
	}
	if cfg.Prompt == "" {
		cfg.Prompt = "请将这段音频逐字转录为文字，只输出转录内容。"
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 300 * time.Second // Omni on long audio is slow
	}
	return &QwenAudioAdapter{
		capability: CapabilityAudioTranscribe,
		endpoint:   trimRightSlash(cfg.Endpoint),
		apiKey:     cfg.APIKey,
		provider:   cfg.ProviderName,
		prompt:     cfg.Prompt,
		maxTokens:  cfg.MaxTokens,
		timeout:    cfg.Timeout,
		hc:         &http.Client{Timeout: cfg.Timeout},
		reader:     cfg.Reader,
		db:      cfg.DB,
	}
}

// Capability implements CapabilityAdapter.
func (a *QwenAudioAdapter) Capability() string { return a.capability }

// Configured reports whether the adapter can run.
func (a *QwenAudioAdapter) Configured() bool {
	return a != nil && a.endpoint != "" && a.reader != nil
}

type qwenAudioReq struct {
	AudioB64     string `json:"audio_b64"`
	Text         string `json:"text"`
	MaxNewTokens int    `json:"max_new_tokens"`
}

type qwenAudioResp struct {
	Text        string  `json:"text"`
	InferenceMs float64 `json:"inference_ms"`
	Model       string  `json:"model"`
}

// Invoke reads the audio, calls /infer, and wraps the whole-clip transcription
// as a single ASR segment.
func (a *QwenAudioAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   a.provider,
			ModelID:        "Qwen2.5-Omni-7B",
			CapabilityType: a.capability,
			EndpointMode:   EndpointModeAdapter,
		},
	}
	if !a.Configured() {
		resp.Error = "qwen-audio adapter not configured"
		return resp, errors.New(resp.Error)
	}
	started := time.Now()

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

	reqBody, _ := json.Marshal(qwenAudioReq{
		AudioB64:     base64.StdEncoding.EncodeToString(raw),
		Text:         a.prompt,
		MaxNewTokens: a.maxTokens,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/infer", bytes.NewReader(reqBody))
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
		resp.Error = fmt.Sprintf("qwen-audio call: %v", err)
		return resp, err
	}
	defer res.Body.Close()
	rawResp, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		msg := string(rawResp)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		resp.Error = fmt.Sprintf("qwen-audio %d: %s", res.StatusCode, msg)
		return resp, errors.New(resp.Error)
	}
	var ar qwenAudioResp
	if err := json.Unmarshal(rawResp, &ar); err != nil {
		resp.Error = fmt.Sprintf("decode qwen-audio resp: %v", err)
		return resp, err
	}

	// Duration for the single whole-clip segment.
	var durMs int64
	if a.db != nil {
		if asset, aerr := a.db.FindAssetByID(ctx, req.AssetID); aerr == nil && asset.DurationMs != nil {
			durMs = *asset.DurationMs
		}
	}

	resp.ASR = &paymodel.ASRResult{
		RunID:      req.RunID,
		TaskID:     req.TaskID,
		AssetID:    req.AssetID,
		TraceID:    req.TraceID,
		Provider:   resp.Provider,
		DurationMs: durMs,
		Segments: []paymodel.ASRSegment{
			{StartMs: 0, EndMs: durMs, Text: ar.Text, Confidence: 1},
		},
		RawResponse: map[string]interface{}{"text": ar.Text, "inference_ms": ar.InferenceMs, "model": ar.Model},
		Status:      "success",
	}
	resp.Status = "success"
	resp.LatencyMs = time.Since(started).Milliseconds()
	return resp, nil
}
