package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GenerationParams captures deterministic and sampling controls for generative
// providers. Nil fields mean "leave provider default unchanged".
type GenerationParams struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	TopP           *float64 `json:"top_p,omitempty"`
	MaxTokens      *int     `json:"max_tokens,omitempty"`
	Seed           *int     `json:"seed,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
}

type providerExtraConfig struct {
	GenerationParams GenerationParams `json:"generation_params,omitempty"`
}

func generationParamsFromExtraConfig(raw string) GenerationParams {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return GenerationParams{}
	}
	var cfg providerExtraConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
		return cfg.GenerationParams
	}
	return GenerationParams{}
}

func generationParamsFromExtras(extras map[string]interface{}) GenerationParams {
	if extras == nil {
		return GenerationParams{}
	}
	base := generationParamsFromAny(extras["generation_params"])
	direct := GenerationParams{
		Temperature:    float64PtrFromExtras(extras, "temperature"),
		TopP:           float64PtrFromExtras(extras, "top_p"),
		MaxTokens:      intPtrFromExtras(extras, "max_tokens"),
		Seed:           intPtrFromExtras(extras, "seed"),
		ResponseFormat: stringFromExtras(extras, "response_format"),
	}
	return mergeGenerationParams(base, direct)
}

func generationParamsFromAny(v interface{}) GenerationParams {
	switch cfg := v.(type) {
	case GenerationParams:
		return cfg
	case map[string]interface{}:
		return GenerationParams{
			Temperature:    float64PtrFromExtras(cfg, "temperature"),
			TopP:           float64PtrFromExtras(cfg, "top_p"),
			MaxTokens:      intPtrFromExtras(cfg, "max_tokens"),
			Seed:           intPtrFromExtras(cfg, "seed"),
			ResponseFormat: stringFromExtras(cfg, "response_format"),
		}
	case string:
		cfg = strings.TrimSpace(cfg)
		if cfg == "" {
			return GenerationParams{}
		}
		var params GenerationParams
		if err := json.Unmarshal([]byte(cfg), &params); err == nil {
			return params
		}
		return generationParamsFromExtraConfig(cfg)
	default:
		return GenerationParams{}
	}
}

func mergeGenerationParams(base, override GenerationParams) GenerationParams {
	out := base
	if override.Temperature != nil {
		out.Temperature = override.Temperature
	}
	if override.TopP != nil {
		out.TopP = override.TopP
	}
	if override.MaxTokens != nil {
		out.MaxTokens = override.MaxTokens
	}
	if override.Seed != nil {
		out.Seed = override.Seed
	}
	if override.ResponseFormat != "" {
		out.ResponseFormat = override.ResponseFormat
	}
	return out
}

func (p GenerationParams) toLLMCallOptions() LLMCallOptions {
	return LLMCallOptions{
		Temperature:    p.Temperature,
		TopP:           p.TopP,
		MaxTokens:      p.MaxTokens,
		Seed:           p.Seed,
		ResponseFormat: responseFormatOption(p.ResponseFormat),
	}
}

func (p GenerationParams) toMap() map[string]interface{} {
	out := map[string]interface{}{}
	if p.Temperature != nil {
		out["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		out["top_p"] = *p.TopP
	}
	if p.MaxTokens != nil {
		out["max_tokens"] = *p.MaxTokens
	}
	if p.Seed != nil {
		out["seed"] = *p.Seed
	}
	if p.ResponseFormat != "" {
		out["response_format"] = p.ResponseFormat
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func responseFormatOption(format string) map[string]interface{} {
	switch strings.TrimSpace(format) {
	case "json_object":
		return map[string]interface{}{"type": "json_object"}
	default:
		return nil
	}
}

func intPtrFromExtras(extras map[string]interface{}, key string) *int {
	switch v := extras[key].(type) {
	case int:
		return &v
	case int64:
		i := int(v)
		return &i
	case uint:
		i := int(v)
		return &i
	case float64:
		i := int(v)
		return &i
	case json.Number:
		i64, err := v.Int64()
		if err == nil {
			i := int(i64)
			return &i
		}
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		var i int
		if _, err := fmt.Sscanf(v, "%d", &i); err == nil {
			return &i
		}
	}
	return nil
}

func stringFromExtras(extras map[string]interface{}, key string) string {
	if v, ok := extras[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
