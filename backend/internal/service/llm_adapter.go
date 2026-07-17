package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMProviderAdapter abstracts different LLM API protocols.
type LLMProviderAdapter interface {
	Call(ctx context.Context, systemPrompt, userPrompt string, opts ...LLMCallOptions) (string, error)
	TestConnection(ctx context.Context) (latencyMs int64, preview string, err error)
	ProviderType() string
}

type LLMCallOptions struct {
	Temperature    *float64
	TopP           *float64
	MaxTokens      *int
	Seed           *int
	ResponseFormat map[string]interface{}
}

// ---------------------------------------------------------------------------
// OllamaAdapter —/api/generate protocol
// ---------------------------------------------------------------------------

// OllamaAdapter implements LLMProviderAdapter for Ollama's /api/generate endpoint.
type OllamaAdapter struct {
	endpoint string
	model    string
	client   *http.Client
}

// NewOllamaAdapter creates an OllamaAdapter.
func NewOllamaAdapter(endpoint, model string, timeout time.Duration) *OllamaAdapter {
	return &OllamaAdapter{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		client:   &http.Client{Timeout: timeout},
	}
}

type ollamaGenerateRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	Stream  bool                   `json:"stream"`
	Options map[string]interface{} `json:"options,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
}

func (a *OllamaAdapter) Call(ctx context.Context, systemPrompt, userPrompt string, opts ...LLMCallOptions) (string, error) {
	prompt := systemPrompt + "\n\n" + userPrompt

	reqBody := ollamaGenerateRequest{
		Model:  a.model,
		Prompt: prompt,
		Stream: false,
	}
	callOpts := mergeLLMCallOptions(opts...)
	if callOpts.Temperature != nil {
		reqBody.Options = map[string]interface{}{"temperature": *callOpts.Temperature}
	}
	if callOpts.TopP != nil {
		if reqBody.Options == nil {
			reqBody.Options = map[string]interface{}{}
		}
		reqBody.Options["top_p"] = *callOpts.TopP
	}
	if callOpts.MaxTokens != nil {
		if reqBody.Options == nil {
			reqBody.Options = map[string]interface{}{}
		}
		reqBody.Options["num_predict"] = *callOpts.MaxTokens
	}
	if callOpts.Seed != nil {
		if reqBody.Options == nil {
			reqBody.Options = map[string]interface{}{}
		}
		reqBody.Options["seed"] = *callOpts.Seed
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal Ollama request failed: %w", err)
	}

	url := a.endpoint + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create Ollama request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Ollama call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Ollama returned status %d: %s", resp.StatusCode, string(body))
	}

	var result ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode Ollama response failed: %w", err)
	}
	return result.Response, nil
}

func (a *OllamaAdapter) TestConnection(ctx context.Context) (latencyMs int64, preview string, err error) {
	start := time.Now()
	resp, callErr := a.Call(ctx, "", "hello")
	latencyMs = time.Since(start).Milliseconds()
	if callErr != nil {
		return latencyMs, "", callErr
	}
	preview = resp
	if len([]rune(preview)) > 100 {
		preview = string([]rune(preview)[:100])
	}
	return latencyMs, preview, nil
}

func (a *OllamaAdapter) ProviderType() string { return "ollama" }

// ---------------------------------------------------------------------------
// OpenAICompatibleAdapter —/v1/chat/completions protocol
// ---------------------------------------------------------------------------

// OpenAICompatibleAdapter implements LLMProviderAdapter for OpenAI-compatible APIs
// (DeepSeek, Qwen, etc.).
type OpenAICompatibleAdapter struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
}

// NewOpenAICompatibleAdapter creates an OpenAICompatibleAdapter.
func NewOpenAICompatibleAdapter(endpoint, apiKey, model string, timeout time.Duration) *OpenAICompatibleAdapter {
	return &OpenAICompatibleAdapter{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		model:    model,
		client:   &http.Client{Timeout: timeout},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model          string                 `json:"model"`
	Messages       []chatMessage          `json:"messages"`
	Temperature    *float64               `json:"temperature,omitempty"`
	TopP           *float64               `json:"top_p,omitempty"`
	MaxTokens      *int                   `json:"max_tokens,omitempty"`
	Seed           *int                   `json:"seed,omitempty"`
	ResponseFormat map[string]interface{} `json:"response_format,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (a *OpenAICompatibleAdapter) Call(ctx context.Context, systemPrompt, userPrompt string, opts ...LLMCallOptions) (string, error) {
	if strings.TrimSpace(a.apiKey) == "" {
		return "", fmt.Errorf("该提供商需要配置 API Key，请检查认证信息")
	}

	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	reqBody := chatCompletionRequest{
		Model:    a.model,
		Messages: messages,
	}
	callOpts := mergeLLMCallOptions(opts...)
	reqBody.Temperature = callOpts.Temperature
	reqBody.TopP = callOpts.TopP
	reqBody.MaxTokens = callOpts.MaxTokens
	reqBody.Seed = callOpts.Seed
	reqBody.ResponseFormat = callOpts.ResponseFormat
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal OpenAI request failed: %w", err)
	}

	url := a.endpoint + "/v1/chat/completions"
	// If endpoint already ends with /v1, don't duplicate it
	if strings.HasSuffix(a.endpoint, "/v1") || strings.HasSuffix(a.endpoint, "/v1/") {
		url = strings.TrimRight(a.endpoint, "/") + "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create OpenAI request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenAI call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenAI API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode OpenAI response failed: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("OpenAI API returned empty choices")
	}
	return result.Choices[0].Message.Content, nil
}

func (a *OpenAICompatibleAdapter) TestConnection(ctx context.Context) (latencyMs int64, preview string, err error) {
	start := time.Now()
	resp, callErr := a.Call(ctx, "You are a helpful assistant.", "hello")
	latencyMs = time.Since(start).Milliseconds()
	if callErr != nil {
		return latencyMs, "", callErr
	}
	preview = resp
	if len([]rune(preview)) > 100 {
		preview = string([]rune(preview)[:100])
	}
	return latencyMs, preview, nil
}

func (a *OpenAICompatibleAdapter) ProviderType() string { return "openai_compatible" }

func mergeLLMCallOptions(opts ...LLMCallOptions) LLMCallOptions {
	var out LLMCallOptions
	for _, opt := range opts {
		if opt.Temperature != nil {
			out.Temperature = opt.Temperature
		}
		if opt.TopP != nil {
			out.TopP = opt.TopP
		}
		if opt.MaxTokens != nil {
			out.MaxTokens = opt.MaxTokens
		}
		if opt.Seed != nil {
			out.Seed = opt.Seed
		}
		if opt.ResponseFormat != nil {
			out.ResponseFormat = opt.ResponseFormat
		}
	}
	return out
}
