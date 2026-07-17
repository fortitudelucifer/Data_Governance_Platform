package service

import (
	"context"
	"fmt"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// TextLLMAdapter routes text.chat calls through CapabilityService so that
// V1 auto-annotation and LLM-refinement get unified trace logging alongside
// VLM / OCR / seg calls.
//
// Provider selection order:
//  1. Extras["provider_id"] (uint / float64 / int) — explicit pick
//  2. req.Model — first enabled provider whose Model name contains the string
//  3. First enabled text.chat provider by priority DESC, id ASC
type TextLLMAdapter struct {
	llmSvc    *LLMService
	dbRepo *repository.DB
}

// NewTextLLMAdapter creates a TextLLMAdapter backed by llmSvc.
func NewTextLLMAdapter(llmSvc *LLMService, dbRepo *repository.DB) *TextLLMAdapter {
	return &TextLLMAdapter{llmSvc: llmSvc, dbRepo: dbRepo}
}

func (a *TextLLMAdapter) Capability() string { return CapabilityTextChat }

func (a *TextLLMAdapter) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	systemPrompt, _ := req.Extras["system_prompt"].(string)
	userPrompt := req.Prompt

	providerID := a.resolveProviderID(req.Extras, req.Model)
	if providerID == 0 {
		return CapabilityResponse{
			Status: "failed",
			Error:  "no text.chat provider available",
		}, fmt.Errorf("no text.chat provider available")
	}

	p, _ := a.llmSvc.GetProvider(providerID)
	effectiveParams := generationParamsFromExtras(req.Extras)

	ref := paymodel.ModelProviderRef{
		CapabilityType: CapabilityTextChat,
		EndpointMode:   "openai",
	}
	if p != nil {
		ref.ProviderID = p.ID
		ref.ProviderName = p.Name
		ref.ModelID = p.Model
		effectiveParams = mergeGenerationParams(generationParamsFromExtraConfig(p.ExtraConfig), effectiveParams)
	}

	start := time.Now()
	text, err := a.llmSvc.CallWithRetry(ctx, providerID, systemPrompt, userPrompt, effectiveParams.toLLMCallOptions())
	latency := time.Since(start).Milliseconds()
	paramsSnapshot := effectiveParams.toMap()

	if err != nil {
		return CapabilityResponse{
			Provider:         ref,
			Status:           "failed",
			Error:            err.Error(),
			LatencyMs:        latency,
			GenerationParams: paramsSnapshot,
		}, err
	}
	return CapabilityResponse{
		Provider:         ref,
		Status:           "success",
		LatencyMs:        latency,
		Text:             text,
		GenerationParams: paramsSnapshot,
	}, nil
}

func (a *TextLLMAdapter) resolveProviderID(extras map[string]interface{}, model string) uint {
	if extras != nil {
		switch v := extras["provider_id"].(type) {
		case uint:
			return v
		case float64:
			return uint(v)
		case int:
			return uint(v)
		case int64:
			return uint(v)
		}
	}
	return a.llmSvc.ProviderIDByModel(model)
}

func float64PtrFromExtras(extras map[string]interface{}, key string) *float64 {
	if extras == nil {
		return nil
	}
	switch v := extras[key].(type) {
	case float64:
		return &v
	case float32:
		f := float64(v)
		return &f
	case int:
		f := float64(v)
		return &f
	case int64:
		f := float64(v)
		return &f
	case uint:
		f := float64(v)
		return &f
	case string:
		if v == "" {
			return nil
		}
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return &f
		}
	}
	return nil
}
