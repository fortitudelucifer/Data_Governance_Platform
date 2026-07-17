package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// EnvAdapterSnapshot records metadata for an adapter registered from environment
// variables at startup. These are visible in the management center as read-only
// entries so admins know which capabilities are running even without DB records.
type EnvAdapterSnapshot struct {
	CapabilityType string `json:"capability_type"`
	ProviderName   string `json:"provider_name"`
	ProviderKind   string `json:"provider_kind"`
	Endpoint       string `json:"endpoint"`
	Model          string `json:"model,omitempty"`
}

// CapabilityConfigService manages CRUD for all capability providers
// (text.chat / vlm.* / ocr.* / seg.*) and hot-reloads adapters after each mutation.
//
// Phase 2 provider_kind dispatch:
//   - "openai" / "ollama" / "litellm" → LiteLLMClient (OpenAI-compatible API)
//   - "anthropic"                      → not yet implemented, skipped with log
//   - "paddlex" / "http"               → OCRHTTPAdapter or SegmentationHTTPAdapter
//
// text.chat providers are reloaded via llmService.ReloadAdapters (LLMService).
// vlm/ocr/seg providers are reloaded via CapabilityService.Register.
type CapabilityConfigService struct {
	dbRepo         *repository.DB
	capabilityService *CapabilityService
	llmService        *LLMService
	assetReader       AssetReader
	aiTimeout         time.Duration
	envAdapters       []EnvAdapterSnapshot
}

// NewCapabilityConfigService creates a CapabilityConfigService.
func NewCapabilityConfigService(
	dbRepo *repository.DB,
	capSvc *CapabilityService,
	llmService *LLMService,
	assetReader AssetReader,
	aiTimeout time.Duration,
) *CapabilityConfigService {
	return &CapabilityConfigService{
		dbRepo:         dbRepo,
		capabilityService: capSvc,
		llmService:        llmService,
		assetReader:       assetReader,
		aiTimeout:         aiTimeout,
	}
}

// reloadAll fires both the vlm/ocr/seg adapter reload (via CapabilityService)
// and the text.chat adapter reload (via LLMService) after any mutation.
func (s *CapabilityConfigService) reloadAll(ctx context.Context) {
	if err := s.ReloadAdapters(ctx); err != nil {
		slog.Error("capability-config: ReloadAdapters failed", "error", err)
	}
	if s.llmService != nil {
		if err := s.llmService.ReloadAdapters(ctx); err != nil {
			slog.Error("capability-config: LLMService.ReloadAdapters failed", "error", err)
		}
	}
}

// ReloadAdapters reads all enabled vlm/ocr/seg providers from DB and registers
// adapters with CapabilityService. DB-registered adapters overwrite env-based
// ones for the same capability_type. Env-based adapters for capabilities not
// present in DB are left untouched.
func (s *CapabilityConfigService) ReloadAdapters(ctx context.Context) error {
	var providers []dbmodel.LLMProvider
	err := s.dbRepo.DB.
		Where("enabled = ? AND capability_type != '' AND capability_type != 'text.chat'", true).
		Order("priority DESC, id ASC").
		Find(&providers).Error
	if err != nil {
		return fmt.Errorf("query capability providers: %w", err)
	}

	for _, p := range providers {
		if adapter := s.buildAdapter(p); adapter != nil && s.capabilityService != nil {
			s.capabilityService.Register(adapter)
		}
	}
	return nil
}

// buildAdapter dispatches by capability_type and provider_kind.
func (s *CapabilityConfigService) buildAdapter(p dbmodel.LLMProvider) CapabilityAdapter {
	timeout := s.aiTimeout
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}

	switch {
	case strings.HasPrefix(p.CapabilityType, "vlm."):
		return s.buildVLMAdapter(p, timeout)
	case strings.HasPrefix(p.CapabilityType, "ocr."):
		return s.buildOCRAdapter(p, timeout)
	case p.CapabilityType == CapabilitySegInstance:
		return s.buildSegAdapter(p, timeout)
	case p.CapabilityType == CapabilitySegInteractive:
		return s.buildSAMInteractiveAdapter(p, timeout)
	case p.CapabilityType == CapabilityASRTranscribe:
		return s.buildASRAdapter(p, timeout)
	case strings.HasPrefix(p.CapabilityType, "audio."), strings.HasPrefix(p.CapabilityType, "video."):
		return s.buildMediaAdapter(p, timeout)
	default:
		return nil
	}
}

// buildASRAdapter creates an ASRHTTPAdapter for an asr.transcribe provider —
// lets an alternative/local ASR sidecar be registered from 能力配置 (overrides
// the env MM_ASR_ENDPOINT adapter of the same capability).
func (s *CapabilityConfigService) buildASRAdapter(p dbmodel.LLMProvider, timeout time.Duration) CapabilityAdapter {
	if p.Endpoint == "" {
		return nil
	}
	return NewASRHTTPAdapter(ASRAdapterConfig{
		Endpoint:     p.Endpoint,
		APIKey:       p.APIKey,
		Model:        p.Model,
		ProviderName: p.Name,
		Timeout:      timeout,
		Reader:       s.assetReader,
	})
}

// buildMediaAdapter creates a generic MediaHTTPAdapter for the remaining
// audio.* / video.* families (emotion/classifier/…). Segment-shaped responses
// seed the workspace like ASR; other payloads are preserved in Raw. Onboarding
// a local audio/video model then needs zero code — just a provider row.
func (s *CapabilityConfigService) buildMediaAdapter(p dbmodel.LLMProvider, timeout time.Duration) CapabilityAdapter {
	if p.Endpoint == "" {
		return nil
	}
	return NewMediaHTTPAdapter(MediaAdapterConfig{
		Capability:   p.CapabilityType,
		Endpoint:     p.Endpoint,
		APIKey:       p.APIKey,
		Model:        p.Model,
		ProviderName: p.Name,
		Timeout:      timeout,
		Reader:       s.assetReader,
	})
}

// buildVLMAdapter creates a VLMAdapter backed by an OpenAI-compatible client.
// openai / ollama / litellm all speak the same /v1/chat/completions protocol —
// only the endpoint URL and api_key differ. Anthropic is not yet supported.
func (s *CapabilityConfigService) buildVLMAdapter(p dbmodel.LLMProvider, timeout time.Duration) CapabilityAdapter {
	switch p.ProviderKind {
	case "openai", "ollama", "litellm":
		// all three use OpenAI-compatible /v1/chat/completions
	case "anthropic":
		// AnthropicClient not yet built; will be added in a future step
		return nil
	default:
		return nil
	}
	if p.Endpoint == "" {
		return nil
	}
	client := NewLiteLLMClient(p.Endpoint, p.APIKey, timeout)
	return NewVLMAdapter(VLMAdapterConfig{
		Capability:       p.CapabilityType,
		Model:            p.Model,
		ProviderName:     p.Name,
		Client:           client,
		Reader:           s.assetReader,
		GenerationParams: generationParamsFromExtraConfig(p.ExtraConfig),
	})
}

// buildOCRAdapter creates an OCRHTTPAdapter for paddlex / http OCR services.
func (s *CapabilityConfigService) buildOCRAdapter(p dbmodel.LLMProvider, timeout time.Duration) CapabilityAdapter {
	if p.Endpoint == "" {
		return nil
	}
	return NewOCRHTTPAdapter(OCRAdapterConfig{
		Capability:   p.CapabilityType,
		Endpoint:     p.Endpoint,
		APIKey:       p.APIKey,
		ProviderName: p.Name,
		Timeout:      timeout,
		Reader:       s.assetReader,
	})
}

// buildSegAdapter creates a SegmentationHTTPAdapter for seg.instance services.
func (s *CapabilityConfigService) buildSegAdapter(p dbmodel.LLMProvider, timeout time.Duration) CapabilityAdapter {
	if p.Endpoint == "" {
		return nil
	}
	return NewSegmentationHTTPAdapter(SegAdapterConfig{
		Capability:   p.CapabilityType,
		Endpoint:     p.Endpoint,
		APIKey:       p.APIKey,
		ProviderName: p.Name,
		Timeout:      timeout,
		Reader:       s.assetReader,
	})
}

// buildSAMInteractiveAdapter creates a SAMInteractiveAdapter for seg.interactive (MobileSAM).
func (s *CapabilityConfigService) buildSAMInteractiveAdapter(p dbmodel.LLMProvider, timeout time.Duration) CapabilityAdapter {
	if p.Endpoint == "" {
		return nil
	}
	return NewSAMInteractiveAdapter(SAMAdapterConfig{
		Endpoint: p.Endpoint,
		APIKey:   p.APIKey,
		Timeout:  timeout,
		Reader:   s.assetReader,
	})
}

// ---------------------------------------------------------------------------
// CRUD — mirrors LLMConfigService pattern; calls ReloadAdapters after writes.
// ---------------------------------------------------------------------------

// Create inserts a new capability provider and reloads adapters.
func (s *CapabilityConfigService) Create(ctx context.Context, p *dbmodel.LLMProvider) error {
	if err := s.dbRepo.DB.Create(p).Error; err != nil {
		return fmt.Errorf("create capability provider: %w", err)
	}
	s.reloadAll(ctx)
	return nil
}

// Update modifies a capability provider by ID and reloads adapters.
// Uses map-based updates so zero-value fields (enabled=false, priority=0) are
// persisted when the caller sets EnabledSet or passes explicit non-zero values.
func (s *CapabilityConfigService) Update(ctx context.Context, id uint, p *dbmodel.LLMProvider) error {
	updates := map[string]interface{}{}
	if p.Name != "" {
		updates["name"] = p.Name
	}
	if p.CapabilityType != "" {
		updates["capability_type"] = p.CapabilityType
	}
	if p.ProviderKind != "" {
		updates["provider_kind"] = p.ProviderKind
	}
	if p.Endpoint != "" {
		updates["endpoint"] = p.Endpoint
	}
	if p.APIKey != "" {
		updates["api_key"] = p.APIKey
	}
	if p.Model != "" {
		updates["model"] = p.Model
	}
	if p.ExtraConfig != "" {
		updates["extra_config"] = p.ExtraConfig
	}
	if p.TimeoutSeconds != 0 {
		updates["timeout_seconds"] = p.TimeoutSeconds
	}
	if p.MaxRetries != 0 {
		updates["max_retries"] = p.MaxRetries
	}
	if p.Priority != 0 {
		updates["priority"] = p.Priority
	}
	if p.EnabledSet {
		updates["enabled"] = p.Enabled
	}
	if p.LastTestSuccess != nil {
		updates["last_test_success"] = *p.LastTestSuccess
	}
	if p.LastTestAt != nil {
		updates["last_test_at"] = *p.LastTestAt
	}
	if p.LastTestLatencyMs != nil {
		updates["last_test_latency_ms"] = *p.LastTestLatencyMs
	}
	if len(updates) == 0 {
		return nil
	}
	if err := s.dbRepo.DB.Model(&dbmodel.LLMProvider{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("update capability provider: %w", err)
	}
	s.reloadAll(ctx)
	return nil
}

// Delete removes a capability provider and reloads adapters.
func (s *CapabilityConfigService) Delete(ctx context.Context, id uint) error {
	result := s.dbRepo.DB.Delete(&dbmodel.LLMProvider{}, id)
	if result.Error != nil {
		return fmt.Errorf("delete capability provider: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("capability provider not found (id=%d)", id)
	}
	s.reloadAll(ctx)
	return nil
}

// List returns capability providers filtered by capability_type.
// If capabilityType is empty, all providers are returned for the admin
// capability-config page.
func (s *CapabilityConfigService) List(ctx context.Context, capabilityType string) ([]dbmodel.LLMProvider, error) {
	q := s.dbRepo.DB
	if capabilityType != "" {
		q = q.Where("capability_type = ?", capabilityType)
	}
	var providers []dbmodel.LLMProvider
	if err := q.Order("priority DESC, id ASC").Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("list capability providers: %w", err)
	}
	return providers, nil
}

// RegisterEnvAdapter records an env-sourced adapter for display in the UI.
// Call once per env-configured adapter after main.go registers it with CapabilityService.
func (s *CapabilityConfigService) RegisterEnvAdapter(snap EnvAdapterSnapshot) {
	s.envAdapters = append(s.envAdapters, snap)
}

// ListEnvAdapters returns all env-sourced adapters, optionally filtered by capability_type.
// Always returns a non-nil slice so JSON serialisation emits [] instead of null.
func (s *CapabilityConfigService) ListEnvAdapters(capabilityType string) []EnvAdapterSnapshot {
	out := make([]EnvAdapterSnapshot, 0, len(s.envAdapters))
	for _, e := range s.envAdapters {
		if capabilityType == "" || e.CapabilityType == capabilityType {
			out = append(out, e)
		}
	}
	return out
}

// InvokableModel is a selectable model for on-demand capability invocation,
// unifying env adapters + enabled DB providers (能力配置). The workspace AI
// panel reads this to populate a model picker. Endpoint/key are intentionally
// omitted — annotators don't need them.
type InvokableModel struct {
	CapabilityType string `json:"capability_type"`
	ProviderName   string `json:"provider_name"`
	Model          string `json:"model"`
	Source         string `json:"source"` // env | db
}

// ListInvokableModels returns the selectable models for a capability (or all),
// combining env adapters and enabled DB providers — the single "能力→模型"
// source the workspace uses to let users pick a model before invoking.
func (s *CapabilityConfigService) ListInvokableModels(ctx context.Context, capabilityType string) []InvokableModel {
	out := []InvokableModel{}
	for _, e := range s.ListEnvAdapters(capabilityType) {
		out = append(out, InvokableModel{CapabilityType: e.CapabilityType, ProviderName: e.ProviderName, Model: e.Model, Source: "env"})
	}
	if dbs, err := s.List(ctx, capabilityType); err == nil {
		for _, p := range dbs {
			if !p.Enabled {
				continue
			}
			out = append(out, InvokableModel{CapabilityType: p.CapabilityType, ProviderName: p.Name, Model: p.Model, Source: "db"})
		}
	}
	return out
}

// GetByID returns a single capability provider.
func (s *CapabilityConfigService) GetByID(ctx context.Context, id uint) (*dbmodel.LLMProvider, error) {
	var p dbmodel.LLMProvider
	if err := s.dbRepo.DB.First(&p, id).Error; err != nil {
		return nil, fmt.Errorf("capability provider not found (id=%d): %w", id, err)
	}
	return &p, nil
}
