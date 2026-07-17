package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// Capability type constants per plan_v1/01 §6.2 / §6.3.
const (
	CapabilityTextChat          = "text.chat"
	CapabilityOCRDetection      = "ocr.detection"
	CapabilityOCRStructure      = "ocr.structure"
	CapabilityOCRVLM            = "ocr.vlm" // P1
	CapabilityVLMCaption        = "vlm.caption"
	CapabilityVLMGrounding      = "vlm.grounding"
	CapabilityVLMStructured     = "vlm.structured_extract"
	CapabilityImageClassifier   = "image.classifier" // P1
	CapabilitySegInstance       = "seg.instance"     // YOLOv8-seg instance segmentation
	CapabilityASRTranscribe     = "asr.transcribe"   // P1
	CapabilityVLMVideoCaption   = "vlm.video_caption"
	CapabilityVLMVideoGrounding = "vlm.video_grounding"
	CapabilityVideoDetectTrack  = "video.detect_track"   // det-server: YOLO26x + ByteTrack/BoT-SORT (B2)
	CapabilityVideoSAM2Propagate = "video.sam2_propagate" // sam2-video: 点选→跨帧 mask 传播 (B2.2)
	CapabilityAudioClassifier   = "audio.classifier"
	CapabilityAudioTranscribe   = "audio.transcribe" // qwen-audio: Qwen2.5-Omni 整段转写
	CapabilitySegInteractive    = "seg.interactive"       // MobileSAM point-prompt segmentation
	CapabilitySemanticRouter    = "image.semantic_router" // SigLIP2 zero-shot L2 routing probe
)

// EndpointMode is "litellm" for generative gateway calls and "adapter" for
// non-generative capability adapters (OCR, classifier, etc.).
const (
	EndpointModeLiteLLM = "litellm"
	EndpointModeAdapter = "adapter"
)

// CapabilityRequest is the input contract every adapter accepts. Concrete
// adapters interpret the fields they need.
type CapabilityRequest struct {
	TaskID         uint
	AssetID        uint
	TraceID        string
	RunID          string
	CapabilityType string
	// AssetURI is the storage URI as persisted in asset.storage_uri. The
	// adapter is expected to resolve the URI to a body (typically by calling
	// CapabilityService.openAsset via the shared dependency).
	AssetURI string
	// MIME of the asset (helpful for VLM data-URL building).
	MIME string
	// Width / Height of the image in pixels (originals). Used for coordinate
	// normalisation per C-02.
	Width  int
	Height int
	// Prompt is the user-supplied instruction for VLM / classifier flows.
	Prompt string
	// Model optionally overrides the adapter's default model (e.g. pick
	// qwen-vl-max vs qwen-vl-plus at invoke time). Empty = adapter default.
	// Non-LLM adapters (OCR) ignore this field.
	Model string
	// Schema is the JSON schema constraining structured outputs (VLM).
	Schema map[string]interface{}
	// Extras is a free-form bag the adapter may consume (provider hints,
	// override timeouts, ...).
	Extras map[string]interface{}
}

// CapabilityResponse is the canonical adapter return shape. The concrete
// payload lives in OCRResult / VLMResult typed fields below; raw response is
// preserved for trace.
type CapabilityResponse struct {
	Provider      paymodel.ModelProviderRef
	Status        string // success | failed | timeout
	Error         string // empty on success
	LatencyMs     int64
	Cost          float64
	EstimatedCost float64
	// GenerationParams records the effective model parameters for generative
	// adapters after provider defaults and request overrides are merged.
	GenerationParams map[string]interface{}

	// Optional structured outputs. Adapters fill the field that matches
	// their capability.
	OCR  *paymodel.OCRResult
	VLM  *paymodel.VLMResult
	Seg  *paymodel.SegResult
	ASR  *paymodel.ASRResult
	Text string // text.chat: raw LLM text response
	Raw  interface{}
}

// CapabilityAdapter is the abstraction that every capability provider must
// implement (plan_v1/05 §5).
type CapabilityAdapter interface {
	// Capability returns the capability_type this adapter serves.
	Capability() string
	// Invoke performs a single capability call. Implementations must NOT
	// throw on business errors — they encode them in the response with
	// Status=failed/timeout so the worker can advance state and write trace.
	Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error)
}

// CapabilityService is the single entry point for AI capability invocations.
// Per plan_v1/01 §6 (C-01-v3) business code MUST NOT bypass this service to
// call provider SDKs / LiteLLM / local model endpoints directly.
type CapabilityService struct {
	mu        sync.RWMutex
	adapters  map[string]CapabilityAdapter
	payloadRepo *repository.DB
}

// QueueReporter is implemented by adapters metering a bounded GPU queue (B2.8).
// Nothing else has a backlog worth showing.
type QueueReporter interface {
	QueueStats() GPUQueueStats
}

// QueueStats snapshots the backlog of every GPU-gated capability, keyed by
// capability name. The workbench polls this to render "队列 2/4" and to grey out
// the trigger before the user clicks into a 429.
func (s *CapabilityService) QueueStats() map[string]GPUQueueStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]GPUQueueStats, len(s.adapters))
	for name, a := range s.adapters {
		if r, ok := a.(QueueReporter); ok {
			out[name] = r.QueueStats()
		}
	}
	return out
}

// NewCapabilityService creates an empty capability service. Use Register to
// add adapters.
func NewCapabilityService(payloadRepo *repository.DB) *CapabilityService {
	return &CapabilityService{
		adapters:  make(map[string]CapabilityAdapter),
		payloadRepo: payloadRepo,
	}
}

// Register adds an adapter. The latest registration for a given capability
// wins.
func (s *CapabilityService) Register(adapter CapabilityAdapter) {
	if adapter == nil {
		return
	}
	s.mu.Lock()
	if old, exists := s.adapters[adapter.Capability()]; exists {
		slog.Warn("capability: overwriting adapter", "capability", adapter.Capability(), "old_type", fmt.Sprintf("%T", old), "new_type", fmt.Sprintf("%T", adapter))
	}
	s.adapters[adapter.Capability()] = adapter
	s.mu.Unlock()
}

// Has reports whether the given capability has an adapter registered.
func (s *CapabilityService) Has(capabilityType string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.adapters[capabilityType]
	return ok
}

// ListCapabilities returns all registered capability types.
func (s *CapabilityService) ListCapabilities() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.adapters))
	for k := range s.adapters {
		out = append(out, k)
	}
	return out
}

// ErrCapabilityNotConfigured is returned when no adapter is registered for the
// requested capability_type.
var ErrCapabilityNotConfigured = errors.New("capability not configured")

// Invoke routes the request to the adapter for req.CapabilityType, writes a
// TraceLog row regardless of outcome, and returns the response.
func (s *CapabilityService) Invoke(ctx context.Context, req CapabilityRequest) (CapabilityResponse, error) {
	s.mu.RLock()
	adapter, ok := s.adapters[req.CapabilityType]
	s.mu.RUnlock()
	if !ok {
		return CapabilityResponse{Status: "failed", Error: "capability not configured"}, fmt.Errorf("%w: %s", ErrCapabilityNotConfigured, req.CapabilityType)
	}

	start := time.Now()
	resp, err := adapter.Invoke(ctx, req)
	if resp.LatencyMs == 0 {
		resp.LatencyMs = time.Since(start).Milliseconds()
	}
	if resp.Provider.CapabilityType == "" {
		resp.Provider.CapabilityType = req.CapabilityType
	}

	// Persist trace regardless of outcome. Best-effort: failures here do not
	// flip the response status because the adapter call has already happened.
	if s.payloadRepo != nil {
		_ = s.payloadRepo.InsertTraceLog(ctx, &paymodel.TraceLog{
			TraceID:             req.TraceID,
			TaskID:              req.TaskID,
			RunID:               req.RunID,
			CapabilityType:      req.CapabilityType,
			Provider:            resp.Provider.ProviderName,
			Model:               resp.Provider.ModelID,
			Version:             resp.Provider.Version,
			EndpointMode:        resp.Provider.EndpointMode,
			LatencyMs:           resp.LatencyMs,
			CostOrEstimatedCost: pickCost(resp),
			Status:              resp.Status,
			Error:               resp.Error,
			CreatedAt:           time.Now(),
		})
	}
	return resp, err
}

func pickCost(r CapabilityResponse) float64 {
	if r.Cost > 0 {
		return r.Cost
	}
	return r.EstimatedCost
}
