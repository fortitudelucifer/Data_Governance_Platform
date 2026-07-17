package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// CapabilityConfigHandler handles HTTP endpoints for capability provider management.
// Routes are intentionally parallel to /llm/providers so the two can coexist
// during the Phase 2 transition period (Step 4 alias, Step 5 cleanup).
type CapabilityConfigHandler struct {
	svc                     *service.CapabilityConfigService
	liteLLMConfigPath       string
	defaultProviderTimeoutSec int
}

// NewCapabilityConfigHandler creates a CapabilityConfigHandler.
// defaultProviderTimeoutSec is used when an admin creates a new provider via
// the UI without specifying TimeoutSeconds; 0 falls back to 90s.
func NewCapabilityConfigHandler(svc *service.CapabilityConfigService, liteLLMConfigPath string, defaultProviderTimeoutSec int) *CapabilityConfigHandler {
	if defaultProviderTimeoutSec <= 0 {
		defaultProviderTimeoutSec = 90
	}
	return &CapabilityConfigHandler{
		svc:                       svc,
		liteLLMConfigPath:         liteLLMConfigPath,
		defaultProviderTimeoutSec: defaultProviderTimeoutSec,
	}
}

// ---------------------------------------------------------------------------
// GET /capabilities/types
// Returns the known capability_type enum with their required form fields.
// Used by the frontend to build provider-add forms dynamically.
// ---------------------------------------------------------------------------

type capabilityTypeMeta struct {
	CapabilityType string   `json:"capability_type"`
	Label          string   `json:"label"`
	ProviderKinds  []string `json:"provider_kinds"`
	// RequiredFields lists which top-level fields the UI must show.
	RequiredFields []string `json:"required_fields"`
}

// ListTypes handles GET /capabilities/types.
func (h *CapabilityConfigHandler) ListTypes(c *gin.Context) {
	types := []capabilityTypeMeta{
		{
			CapabilityType: "text.chat",
			Label:          "文本 LLM",
			ProviderKinds:  []string{"openai", "ollama", "litellm"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint", "api_key", "model"},
		},
		{
			CapabilityType: "vlm.structured_extract",
			Label:          "VLM 结构化提取",
			ProviderKinds:  []string{"openai", "ollama", "litellm"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint", "api_key", "model"},
		},
		{
			CapabilityType: "vlm.caption",
			Label:          "VLM 图片描述",
			ProviderKinds:  []string{"openai", "ollama", "litellm"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint", "api_key", "model"},
		},
		{
			CapabilityType: "ocr.structure",
			Label:          "OCR 结构化识别",
			ProviderKinds:  []string{"paddlex", "http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
		{
			CapabilityType: "seg.instance",
			Label:          "实例分割",
			ProviderKinds:  []string{"http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
		{
			CapabilityType: "seg.interactive",
			Label:          "交互式分割 (SAM)",
			ProviderKinds:  []string{"http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
		{
			CapabilityType: "asr.transcribe",
			Label:          "音频·语音转写 (ASR)",
			ProviderKinds:  []string{"http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
		{
			CapabilityType: "audio.classifier",
			Label:          "音频·分类",
			ProviderKinds:  []string{"http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
		{
			CapabilityType: "audio.emotion",
			Label:          "音频·情绪识别",
			ProviderKinds:  []string{"http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
		{
			CapabilityType: "vlm.video_caption",
			Label:          "视频·描述 (VLM)",
			ProviderKinds:  []string{"openai", "ollama", "litellm"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint", "api_key", "model"},
		},
		{
			CapabilityType: "video.classifier",
			Label:          "视频·分类",
			ProviderKinds:  []string{"http"},
			RequiredFields: []string{"name", "capability_type", "provider_kind", "endpoint"},
		},
	}
	c.JSON(http.StatusOK, types)
}

// ---------------------------------------------------------------------------
// GET /capabilities/providers?capability_type=vlm.caption
// ---------------------------------------------------------------------------

// ListProviders handles GET /capabilities/providers.
func (h *CapabilityConfigHandler) ListProviders(c *gin.Context) {
	capType := c.Query("capability_type")
	providers, err := h.svc.List(c.Request.Context(), capType)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, providers)
}

// ---------------------------------------------------------------------------
// GET /capabilities/providers/:id
// ---------------------------------------------------------------------------

// GetProvider handles GET /capabilities/providers/:id.
func (h *CapabilityConfigHandler) GetProvider(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid provider id")
		return
	}
	p, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		Error(c, http.StatusNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, p)
}

// ---------------------------------------------------------------------------
// POST /capabilities/providers
// ---------------------------------------------------------------------------

type createCapabilityProviderRequest struct {
	Name           string `json:"name"            binding:"required"`
	CapabilityType string `json:"capability_type" binding:"required"`
	ProviderKind   string `json:"provider_kind"   binding:"required"`
	Endpoint       string `json:"endpoint"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	ExtraConfig    string `json:"extra_config"`
	Enabled        *bool  `json:"enabled"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxRetries     int    `json:"max_retries"`
	Priority       int    `json:"priority"`
}

// CreateProvider handles POST /capabilities/providers.
func (h *CapabilityConfigHandler) CreateProvider(c *gin.Context) {
	var req createCapabilityProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request: " + err.Error())
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = h.defaultProviderTimeoutSec
	}
	maxRetries := req.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}

	p := &dbmodel.LLMProvider{
		Name:           req.Name,
		Type:           req.ProviderKind, // keep Type in sync for V1 compat
		CapabilityType: req.CapabilityType,
		ProviderKind:   req.ProviderKind,
		Endpoint:       req.Endpoint,
		APIKey:         req.APIKey,
		Model:          req.Model,
		ExtraConfig:    req.ExtraConfig,
		Enabled:        enabled,
		TimeoutSeconds: timeout,
		MaxRetries:     maxRetries,
		Priority:       req.Priority,
	}

	if err := h.svc.Create(c.Request.Context(), p); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusCreated, p)
}

// ---------------------------------------------------------------------------
// PUT /capabilities/providers/:id
// ---------------------------------------------------------------------------

type updateCapabilityProviderRequest struct {
	Name           string `json:"name"`
	CapabilityType string `json:"capability_type"`
	ProviderKind   string `json:"provider_kind"`
	Endpoint       string `json:"endpoint"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	ExtraConfig    string `json:"extra_config"`
	Enabled        *bool  `json:"enabled"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxRetries     int    `json:"max_retries"`
	Priority       int    `json:"priority"`
}

// UpdateProvider handles PUT /capabilities/providers/:id.
func (h *CapabilityConfigHandler) UpdateProvider(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid provider id")
		return
	}

	var req updateCapabilityProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request: " + err.Error())
		return
	}

	p := &dbmodel.LLMProvider{
		Name:           req.Name,
		CapabilityType: req.CapabilityType,
		ProviderKind:   req.ProviderKind,
		Endpoint:       req.Endpoint,
		APIKey:         req.APIKey,
		Model:          req.Model,
		ExtraConfig:    req.ExtraConfig,
		TimeoutSeconds: req.TimeoutSeconds,
		MaxRetries:     req.MaxRetries,
		Priority:       req.Priority,
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
		p.EnabledSet = true
	}

	if err := h.svc.Update(c.Request.Context(), id, p); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	OK(c, "updated")
}

// ---------------------------------------------------------------------------
// DELETE /capabilities/providers/:id
// ---------------------------------------------------------------------------

// DeleteProvider handles DELETE /capabilities/providers/:id.
func (h *CapabilityConfigHandler) DeleteProvider(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid provider id")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	OK(c, "deleted")
}

// ---------------------------------------------------------------------------
// POST /capabilities/providers/:id/test
// Performs a lightweight connectivity probe appropriate for the provider_kind.
// ---------------------------------------------------------------------------

// TestProvider handles POST /capabilities/providers/:id/test.
func (h *CapabilityConfigHandler) TestProvider(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid provider id")
		return
	}

	ctx := c.Request.Context()
	p, err := h.svc.GetByID(ctx, id)
	if err != nil {
		Error(c, http.StatusNotFound, err.Error())
		return
	}

	start := time.Now()
	testErr := probeProvider(ctx, p)
	latencyMs := time.Since(start).Milliseconds()

	// Persist test result.
	now := time.Now()
	success := testErr == nil
	updates := &dbmodel.LLMProvider{
		LastTestSuccess:   &success,
		LastTestAt:        &now,
		LastTestLatencyMs: intPtrCap(int(latencyMs)),
	}
	_ = h.svc.Update(ctx, id, updates)

	if testErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"error":      testErr.Error(),
			"latency_ms": latencyMs,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"latency_ms": latencyMs,
	})
}

// ---------------------------------------------------------------------------
// POST /capabilities/providers/probe
// Probe connectivity without a saved record (used by the create/edit form).
// Body: { endpoint, provider_kind, api_key }
// ---------------------------------------------------------------------------

type probeRequest struct {
	Endpoint     string `json:"endpoint"      binding:"required"`
	ProviderKind string `json:"provider_kind"`
	APIKey       string `json:"api_key"`
}

// ProbeProvider handles POST /capabilities/providers/probe.
func (h *CapabilityConfigHandler) ProbeProvider(c *gin.Context) {
	var req probeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request: " + err.Error())
		return
	}

	p := &dbmodel.LLMProvider{
		Endpoint:     req.Endpoint,
		ProviderKind: req.ProviderKind,
		APIKey:       req.APIKey,
	}

	start := time.Now()
	testErr := probeEndpoint(c.Request.Context(), p)
	latencyMs := time.Since(start).Milliseconds()

	if testErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"error":      testErr.Error(),
			"latency_ms": latencyMs,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success":    true,
		"latency_ms": latencyMs,
	})
}

// ---------------------------------------------------------------------------
// GET /capabilities/providers/env
// Returns env-sourced adapters registered at startup (read-only in the UI).
// ---------------------------------------------------------------------------

// ListEnvAdapters handles GET /capabilities/providers/env.
func (h *CapabilityConfigHandler) ListEnvAdapters(c *gin.Context) {
	capType := c.Query("capability_type")
	c.JSON(http.StatusOK, h.svc.ListEnvAdapters(capType))
}

// ListModels handles GET /capabilities/models?capability_type=... — the
// selectable models for on-demand invocation (env adapters + enabled DB
// providers). Registered in the annotator-accessible group (read-only, no
// endpoints/keys exposed) so the workspace AI panel can populate a model picker.
func (h *CapabilityConfigHandler) ListModels(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.ListInvokableModels(c.Request.Context(), c.Query("capability_type")))
}

// probeEndpoint checks that the provider's endpoint is reachable.
// For OpenAI-compatible VLM providers (openai/ollama/litellm), it hits
// GET {endpoint}/models. For OCR/seg HTTP services, it hits GET {endpoint}/healthz.
// probeProvider validates a saved provider. For chat/LLM-kind providers with a
// configured model it does a real minimal invocation so per-model access errors
// (e.g. DashScope 403 access_denied for a model the key hasn't activated) are
// caught at config time rather than when a task runs. Other kinds (OCR/seg
// containers) fall back to a connectivity probe.
func probeProvider(ctx context.Context, p *dbmodel.LLMProvider) error {
	switch p.ProviderKind {
	case "openai", "litellm", "ollama":
		if strings.TrimSpace(p.Model) != "" {
			return probeModelAccess(ctx, p)
		}
	}
	return probeEndpoint(ctx, p)
}

// probeModelAccess sends a 1-token text ping to the provider's configured model.
// Access/auth errors (403/401) surface before any content validation, so a
// text ping is enough to verify the key can actually use this model. Returns a
// friendly, operator-facing message on failure.
func probeModelAccess(ctx context.Context, p *dbmodel.LLMProvider) error {
	to := time.Duration(p.TimeoutSeconds) * time.Second
	if to <= 0 {
		to = 15 * time.Second
	}
	client := service.NewLiteLLMClient(p.Endpoint, p.APIKey, to)
	maxTok := 1
	_, err := client.ChatCompletion(ctx, service.ChatCompletionRequest{
		Model:     p.Model,
		Messages:  []service.LLMMessage{{Role: "user", Content: "ping"}},
		MaxTokens: &maxTok,
	})
	if err != nil {
		return errors.New(service.FriendlyLLMError(err))
	}
	return nil
}

func probeEndpoint(ctx context.Context, p *dbmodel.LLMProvider) error {
	if p.Endpoint == "" {
		return nil // no endpoint to test (e.g. anthropic with cloud key only — skip for now)
	}

	endpoint := strings.TrimRight(p.Endpoint, "/")
	// Try the likely health paths per kind. Our sidecars are inconsistent:
	// det-server / sam2-video / SAM use /health; OCR / SEG / ASR use /healthz;
	// OpenAI-compatible (litellm) exposes /models. First path that connects wins
	// (any HTTP response — even 4xx/401 — means the server is reachable).
	var paths []string
	switch p.ProviderKind {
	case "openai", "litellm", "ollama":
		paths = []string{"/models", "/health"}
	default:
		paths = []string{"/health", "/healthz"}
	}
	hc := &http.Client{Timeout: 8 * time.Second}
	var lastErr error
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
		resp, err := hc.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil // reachable
		}
		lastErr = err
	}
	return lastErr
}

// ---------------------------------------------------------------------------
// GET /capabilities/litellm/config
// PUT /capabilities/litellm/config
// Read / write the litellm-config.yaml file. Path is set server-side via
// MM_LITELLM_CONFIG_PATH so the client only sends content, never a path.
// ---------------------------------------------------------------------------

// GetLiteLLMConfig handles GET /capabilities/litellm/config.
func (h *CapabilityConfigHandler) GetLiteLLMConfig(c *gin.Context) {
	if h.liteLLMConfigPath == "" {
		Error(c, http.StatusNotFound, "MM_LITELLM_CONFIG_PATH not configured on server")
		return
	}
	data, err := os.ReadFile(h.liteLLMConfigPath)
	if err != nil {
		Error(c, http.StatusInternalServerError, "read config: " + err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"content": string(data),
		"path":    h.liteLLMConfigPath,
	})
}

type updateLiteLLMConfigRequest struct {
	Content string `json:"content" binding:"required"`
}

// UpdateLiteLLMConfig handles PUT /capabilities/litellm/config.
func (h *CapabilityConfigHandler) UpdateLiteLLMConfig(c *gin.Context) {
	if h.liteLLMConfigPath == "" {
		Error(c, http.StatusNotFound, "MM_LITELLM_CONFIG_PATH not configured on server")
		return
	}
	var req updateLiteLLMConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request: " + err.Error())
		return
	}
	if err := os.WriteFile(h.liteLLMConfigPath, []byte(req.Content), 0644); err != nil {
		Error(c, http.StatusInternalServerError, "write config: " + err.Error())
		return
	}
	OK(c, "saved")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func parseID(c *gin.Context) (uint, error) {
	v, err := strconv.ParseUint(c.Param("id"), 10, 64)
	return uint(v), err
}

func intPtrCap(v int) *int { return &v }
