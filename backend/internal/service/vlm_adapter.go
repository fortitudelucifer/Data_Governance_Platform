package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// AssetReader is the dependency adapters use to fetch the image bytes for an
// asset by its storage_uri. P0 wires this to ObjectStore.Get via a closure in
// main.go.
type AssetReader func(ctx context.Context, storageURI string) (io.ReadCloser, error)

// VLMAdapter implements CapabilityAdapter for vlm.caption / vlm.grounding /
// vlm.structured_extract via the LiteLLM proxy (plan_v1/05 §5.1).
type VLMAdapter struct {
	capability       string
	client           *LiteLLMClient
	model            string
	providerKey      string
	reader           AssetReader
	generationParams GenerationParams
}

// VLMAdapterConfig captures the wiring options for a single VLM adapter
// registration.
type VLMAdapterConfig struct {
	Capability       string // ocr.detection / vlm.caption / ...
	Model            string // LiteLLM model_group, e.g. "qwen-vl"
	ProviderName     string // human-readable provider name for trace
	Client           *LiteLLMClient
	Reader           AssetReader
	GenerationParams GenerationParams
}

// NewVLMAdapter constructs a VLM adapter. The capability and model are
// required; everything else may be nil/empty for tests.
func NewVLMAdapter(cfg VLMAdapterConfig) *VLMAdapter {
	if cfg.Capability == "" {
		cfg.Capability = CapabilityVLMCaption
	}
	if cfg.ProviderName == "" {
		cfg.ProviderName = "litellm"
	}
	return &VLMAdapter{
		capability:       cfg.Capability,
		client:           cfg.Client,
		model:            cfg.Model,
		providerKey:      cfg.ProviderName,
		reader:           cfg.Reader,
		generationParams: cfg.GenerationParams,
	}
}

// Capability implements CapabilityAdapter.
func (a *VLMAdapter) Capability() string { return a.capability }

// Invoke implements CapabilityAdapter. The flow is:
//
//  1. Read the asset bytes via the injected AssetReader.
//  2. Build an OpenAI-style multimodal message with a base64 image_url.
//  3. POST to LiteLLM and parse the response.
//  4. Try to parse the response content as JSON; if successful, populate
//     StructuredJSON, else keep it in Caption.
//  5. Wrap the trace metadata into the canonical CapabilityResponse.
func (a *VLMAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	// Per-request model override (e.g. operator picks qwen-vl-max vs
	// qwen-vl-plus). Falls back to the adapter's configured default.
	model := a.model
	if req.Model != "" {
		model = req.Model
	}
	resp := CapabilityResponse{
		Status: "failed",
		Provider: paymodel.ModelProviderRef{
			ProviderName:   a.providerKey,
			ModelID:        model,
			CapabilityType: a.capability,
			EndpointMode:   EndpointModeLiteLLM,
		},
	}
	generationParams := mergeGenerationParams(a.generationParams, generationParamsFromExtras(req.Extras))
	if a.capability == CapabilityVLMStructured && len(req.Schema) > 0 && generationParams.ResponseFormat == "" {
		generationParams.ResponseFormat = "json_object"
	}
	resp.GenerationParams = generationParams.toMap()

	if a.client == nil || !a.client.Configured() {
		resp.Error = "litellm endpoint not configured"
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
	dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(raw))

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = a.defaultPrompt()
	}

	textPart := LLMTextPart{Type: "text", Text: prompt}
	imgPart := LLMImageURLPart{Type: "image_url"}
	imgPart.ImageURL.URL = dataURL

	msg := LLMMessage{
		Role:    "user",
		Content: []interface{}{textPart, imgPart},
	}

	chatReq := ChatCompletionRequest{
		Model:          model,
		Messages:       []LLMMessage{msg},
		Temperature:    generationParams.Temperature,
		TopP:           generationParams.TopP,
		MaxTokens:      generationParams.MaxTokens,
		Seed:           generationParams.Seed,
		ResponseFormat: responseFormatOption(generationParams.ResponseFormat),
	}
	if a.capability == CapabilityVLMStructured && len(req.Schema) > 0 {
		chatReq.ResponseFormat = map[string]interface{}{"type": "json_object"}
	}

	start := time.Now()
	out, err := a.client.ChatCompletion(ctx, chatReq)
	resp.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		// Friendly message for display/storage; return the raw typed error so the
		// worker can classify permanent vs transient for retry decisions.
		resp.Error = FriendlyLLMError(err)
		return resp, err
	}
	if out.Model != "" {
		resp.Provider.ModelID = out.Model
	}
	resp.Cost = out.Cost
	if resp.Cost == 0 {
		// LiteLLM cost may be unavailable when provider is local; record an
		// estimated cost as 0 and let the dashboard treat it as such.
		resp.EstimatedCost = 0
	}

	vlm := &paymodel.VLMResult{
		RunID:     req.RunID,
		TaskID:    req.TaskID,
		AssetID:   req.AssetID,
		TraceID:   req.TraceID,
		Provider:  resp.Provider,
		LatencyMs: resp.LatencyMs,
		Cost:      resp.Cost,
		Status:    "success",
	}

	// Try to interpret the response as JSON for structured / grounding.
	// VL 模型常把 JSON 包在 Markdown 代码围栏里（```json ... ```），先剥掉再判断/解析，
	// 否则首字符是反引号会被误判为非 JSON，导致整段（含围栏）落进 caption、tags 丢失。
	content := stripCodeFence(out.Content)
	if a.capability == CapabilityVLMStructured || a.capability == CapabilityVLMGrounding || looksLikeJSON(content) {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(content), &parsed); err == nil {
			vlm.StructuredJSON = parsed
			if cap, ok := parsed["caption"].(string); ok {
				vlm.Caption = cap
			}
			if tags, ok := parsed["tags"].([]interface{}); ok {
				for _, t := range tags {
					if s, ok := t.(string); ok {
						vlm.Tags = append(vlm.Tags, s)
					}
				}
			}
			vlm.GroundingBoxes = parseGroundingBoxes(parsed)
		} else {
			vlm.Caption = content
		}
	} else {
		vlm.Caption = content
	}

	vlm.RawResponse = json.RawMessage(out.Raw)
	resp.VLM = vlm
	resp.Status = "success"
	return resp, nil
}

func (a *VLMAdapter) defaultPrompt() string {
	switch a.capability {
	case CapabilityVLMCaption:
		return "请用一句简短的中文描述这张图片的主要内容，并输出 3 到 5 个关键词标签。返回 JSON：{\"caption\": string, \"tags\": [string]}"
	case CapabilityVLMGrounding:
		return "请输出图片中显著物体的边界框，使用原图绝对像素坐标，返回 JSON：{\"objects\": [{\"label\": string, \"bbox\": [x,y,w,h], \"confidence\": number}]}"
	case CapabilityVLMStructured:
		return "请按照下游 schema 抽取图片中的结构化信息，并仅返回符合 schema 的 JSON。"
	}
	return "请描述这张图片。"
}

func looksLikeJSON(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '{' || s[0] == '[' {
		return true
	}
	return false
}

// stripCodeFence removes a single surrounding Markdown code fence from a model
// response so fenced JSON can be parsed. Handles ```json / ``` openers and the
// trailing ```. If no fence is present the input is returned trimmed unchanged.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line, including any language hint (```json, ```JSON, …).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	// Drop the trailing fence, if any.
	if end := strings.LastIndex(s, "```"); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(s)
}

// parseGroundingBoxes converts a structured JSON's `objects` array into the
// canonical OCRBox shape used for grounding visualisation.
func parseGroundingBoxes(parsed map[string]interface{}) []paymodel.OCRBox {
	objs, ok := parsed["objects"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]paymodel.OCRBox, 0, len(objs))
	for _, raw := range objs {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		box := paymodel.OCRBox{}
		if label, ok := m["label"].(string); ok {
			box.Text = label
		}
		if conf, ok := m["confidence"].(float64); ok {
			box.Confidence = conf
		}
		if bb, ok := m["bbox"].([]interface{}); ok && len(bb) >= 4 {
			box.X = numberFromAny(bb[0])
			box.Y = numberFromAny(bb[1])
			box.Width = numberFromAny(bb[2])
			box.Height = numberFromAny(bb[3])
		}
		out = append(out, box)
	}
	return out
}

func numberFromAny(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
