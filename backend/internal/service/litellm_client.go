package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LiteLLMClient is the HTTP client for the LiteLLM Proxy. It only supports the
// OpenAI-compatible /v1/chat/completions endpoint, which is the path P0 needs
// for Qwen-VL / GPT / Gemini / Claude routing (plan_v1/01 §6 / 05 §4).
//
// The client deliberately stays minimal: no streaming, no function calling,
// no batching. P1 can layer those on top.
type LiteLLMClient struct {
	endpoint string
	apiKey   string
	timeout  time.Duration
	hc       *http.Client
}

// NewLiteLLMClient constructs a client. endpoint is the base URL of the
// LiteLLM proxy (e.g. http://127.0.0.1:4000); apiKey is the master key. The
// returned pointer is nil-safe: a nil client can still be type-checked but
// will return an error on Invoke.
func NewLiteLLMClient(endpoint, apiKey string, timeout time.Duration) *LiteLLMClient {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &LiteLLMClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		timeout:  timeout,
		hc:       &http.Client{Timeout: timeout},
	}
}

// Configured reports whether the client has a usable endpoint.
func (c *LiteLLMClient) Configured() bool {
	return c != nil && strings.TrimSpace(c.endpoint) != ""
}

// LLMMessage matches the OpenAI Chat Completion message shape. Content can be
// a string (text-only) or a slice of content parts (multimodal).
type LLMMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// LLMTextPart / LLMImagePart are the multi-modal content parts the OpenAI
// API expects when sending images.
type LLMTextPart struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type LLMImageURLPart struct {
	Type     string `json:"type"` // "image_url"
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

// ChatCompletionRequest is the subset of fields P0 uses.
type ChatCompletionRequest struct {
	Model       string       `json:"model"`
	Messages    []LLMMessage `json:"messages"`
	Temperature *float64     `json:"temperature,omitempty"`
	TopP        *float64     `json:"top_p,omitempty"`
	MaxTokens   *int         `json:"max_tokens,omitempty"`
	Seed        *int         `json:"seed,omitempty"`
	// ResponseFormat is OpenAI's structured output hint
	// ({"type":"json_object"} forces JSON).
	ResponseFormat map[string]interface{} `json:"response_format,omitempty"`
}

// ChatCompletionResponse is the subset we read back. LiteLLM populates `usage`
// with token counts and (when configured) cost.
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int        `json:"index"`
		Message LLMMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalTokens      int     `json:"total_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
	// LiteLLM enriches responses with provider metadata under various keys
	// depending on configuration; callers should rely on ChatCompletionResult
	// for the canonical shape.
}

// ChatCompletionResult is the canonical result returned by ChatCompletion. It
// flattens what we need for trace/cost/latency persistence.
type ChatCompletionResult struct {
	Content      string
	Model        string
	PromptTokens int
	OutputTokens int
	Cost         float64
	LatencyMs    int64
	Raw          json.RawMessage
}

// ChatCompletion calls /v1/chat/completions on the LiteLLM proxy.
func (c *LiteLLMClient) ChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResult, error) {
	if !c.Configured() {
		return ChatCompletionResult{}, errors.New("litellm endpoint not configured")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ChatCompletionResult{}, fmt.Errorf("marshal litellm req: %w", err)
	}

	url := c.endpoint + "/v1/chat/completions"
	if strings.HasSuffix(c.endpoint, "/v1") || strings.HasSuffix(c.endpoint, "/v1/") {
		url = strings.TrimRight(c.endpoint, "/") + "/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatCompletionResult{}, fmt.Errorf("new litellm req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	start := time.Now()
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return ChatCompletionResult{}, fmt.Errorf("litellm call: %w", err)
	}
	defer resp.Body.Close()
	rawBody, _ := io.ReadAll(resp.Body)
	latency := time.Since(start).Milliseconds()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatCompletionResult{LatencyMs: latency, Raw: rawBody}, parseLLMError(resp.StatusCode, rawBody)
	}

	var parsed ChatCompletionResponse
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return ChatCompletionResult{LatencyMs: latency, Raw: rawBody}, fmt.Errorf("decode litellm resp: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return ChatCompletionResult{LatencyMs: latency, Raw: rawBody}, errors.New("litellm returned no choices")
	}
	content := stringContent(parsed.Choices[0].Message.Content)
	return ChatCompletionResult{
		Content:      content,
		Model:        parsed.Model,
		PromptTokens: parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		Cost:         parsed.Usage.Cost,
		LatencyMs:    latency,
		Raw:          rawBody,
	}, nil
}

// LLMAPIError is returned by ChatCompletion on a non-2xx gateway response. It
// carries the HTTP status plus the upstream provider's structured error
// code/type/message so callers can classify the failure (permanent auth/access
// error vs transient rate-limit/5xx) and render an actionable message.
type LLMAPIError struct {
	StatusCode int    // HTTP status returned by the gateway
	Code       string // upstream error code, e.g. "403", "invalid_api_key"
	Type       string // upstream error type, e.g. "access_denied"
	Message    string // upstream human-readable message
	Raw        string // truncated raw body (for trace)
}

func (e *LLMAPIError) Error() string {
	tag := e.Type
	if tag == "" {
		tag = e.Code
	}
	if tag != "" {
		return fmt.Sprintf("litellm http %d (%s): %s", e.StatusCode, tag, e.Message)
	}
	return fmt.Sprintf("litellm http %d: %s", e.StatusCode, e.Message)
}

// Permanent reports whether retrying is futile (auth / access / bad-request
// class). Rate limits (429) and 5xx are treated as transient. Classification
// uses the HTTP status, the parsed code/type, AND message substrings, because
// LiteLLM sometimes wraps an upstream 403 in a 500 with the real reason only in
// the message ("Access denied ... Received Model Group=...").
func (e *LLMAPIError) Permanent() bool {
	// Unambiguous client/auth status codes.
	switch e.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	}
	// Message/code detection runs BEFORE the transient default, because LiteLLM
	// sometimes wraps an upstream 403 in a 500 with the real reason only in the
	// message ("Access denied ... Received Model Group=...").
	hay := strings.ToLower(e.Type + " " + e.Code + " " + e.Message)
	for _, m := range []string{"access denied", "access_denied", "invalid_api_key",
		"invalid api key", "incorrect api key", "not authorized", "unauthorized",
		"model_not_found", "permission", "forbidden"} {
		if strings.Contains(hay, m) {
			return true
		}
	}
	// Everything else (429 rate-limit, 5xx, timeouts, unknown) is transient and
	// keeps the worker's existing backoff-retry behaviour.
	return false
}

// parseLLMError builds an *LLMAPIError from a non-2xx response body, extracting
// the OpenAI/DashScope-style {"error":{"message","type","code"}} envelope.
func parseLLMError(status int, body []byte) *LLMAPIError {
	e := &LLMAPIError{StatusCode: status, Raw: truncate(string(body), 512)}
	var env struct {
		Error struct {
			Message string      `json:"message"`
			Type    string      `json:"type"`
			Code    interface{} `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil {
		e.Message = env.Error.Message
		e.Type = env.Error.Type
		switch c := env.Error.Code.(type) {
		case string:
			e.Code = c
		case float64:
			e.Code = fmt.Sprintf("%d", int(c))
		}
	}
	if e.Message == "" {
		e.Message = truncate(string(body), 256)
	}
	return e
}

// isPermanentLLMError reports whether err (possibly wrapped) is a non-retryable
// LLM gateway error. Non-LLM errors (network, timeout, context) are treated as
// transient so the worker keeps its existing backoff behaviour for them.
func isPermanentLLMError(err error) bool {
	if err == nil {
		return false
	}
	var api *LLMAPIError
	if errors.As(err, &api) {
		return api.Permanent()
	}
	return false
}

// FriendlyLLMError maps a (possibly typed) LLM error to an actionable,
// operator-facing Chinese message stored in ai_run.error. Non-LLM errors and
// unrecognised codes fall back to the original error text.
func FriendlyLLMError(err error) string {
	if err == nil {
		return ""
	}
	var api *LLMAPIError
	if !errors.As(err, &api) {
		return err.Error()
	}
	hay := strings.ToLower(api.Type + " " + api.Code + " " + api.Message)
	switch {
	case strings.Contains(hay, "access denied") || strings.Contains(hay, "access_denied") ||
		(api.StatusCode == http.StatusForbidden):
		return "该模型未对当前账号开通或无访问权限（access_denied）。请在『能力配置』改用已开通的模型，或在阿里云百炼开通该模型后重试。原始信息：" + api.Message
	case strings.Contains(hay, "invalid_api_key") || strings.Contains(hay, "api key") ||
		api.StatusCode == http.StatusUnauthorized:
		return "API Key 无效或区域不匹配（如国内/国际站点混用）。请检查该 provider 的 Key 与 endpoint。原始信息：" + api.Message
	case strings.Contains(hay, "model_not_found") || api.StatusCode == http.StatusNotFound:
		return "模型名不存在，请检查 LiteLLM 配置中的 model 映射。原始信息：" + api.Message
	case api.StatusCode == http.StatusTooManyRequests:
		return "调用频率超限（429），请稍后重试或降低并发。原始信息：" + api.Message
	default:
		return api.Error()
	}
}

func stringContent(c interface{}) string {
	switch v := c.(type) {
	case string:
		return v
	case []interface{}:
		// concatenate text parts
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, _ := m["type"].(string); t == "text" {
					if s, _ := m["text"].(string); s != "" {
						b.WriteString(s)
					}
				}
			}
		}
		return b.String()
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
