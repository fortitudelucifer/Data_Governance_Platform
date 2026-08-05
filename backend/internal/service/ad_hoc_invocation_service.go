package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// AdHocInvocationService lets operators invoke ANY registered capability on a
// task on demand, independent of the L1 router decision. Use cases:
//
//   - Street scene routed to OCR_FIRST but reviewer wants VLM caption too.
//   - Document routed to VLM_FIRST but contains text the reviewer wants OCR'd.
//   - Re-run the same capability after upstream model change.
//
// The service writes a new mm_ai_run + mm_ocr_result / mm_vlm_result with a
// fresh run_id, but does NOT alter the task state machine, the route
// strategy, or the task.ai_run_ids list (which tracks the *auto* run lineage
// per plan §11.4.4 design invariant #2: all AI calls still go through
// CapabilityService.Invoke; ad-hoc is just another caller of the same path).
type AdHocInvocationService struct {
	db   *repository.DB
	payload   *repository.DB
	cap     *CapabilityService
	timeout time.Duration
	cache   *cache.Cache // nil = no Redis
}

// NewAdHocInvocationService composes the dependencies.
func NewAdHocInvocationService(dbRepo *repository.DB, payloadRepo *repository.DB, capSvc *CapabilityService, timeout time.Duration) *AdHocInvocationService {
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &AdHocInvocationService{db: dbRepo, payload: payloadRepo, cap: capSvc, timeout: timeout}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *AdHocInvocationService) WithCache(c *cache.Cache) *AdHocInvocationService {
	s.cache = c
	return s
}

// AdHocInvocationResult is the API-facing summary returned to the operator.
type AdHocInvocationResult struct {
	RunID       string                       `json:"run_id"`
	TaskID      uint                         `json:"task_id"`
	Capability  string                       `json:"capability"`
	Status      string                       `json:"status"`
	LatencyMs   int64                        `json:"latency_ms"`
	Error       string                       `json:"error,omitempty"`
	OCR         *paymodel.OCRResult        `json:"ocr,omitempty"`
	VLM         *paymodel.VLMResult        `json:"vlm,omitempty"`
	Seg         *paymodel.SegResult        `json:"seg,omitempty"`
	ASR         *paymodel.ASRResult        `json:"asr,omitempty"`
	Provider    paymodel.ModelProviderRef  `json:"provider"`
}

// State guard: ad-hoc invoke requires the task to have an asset that's past
// QC. Pre-routing tasks (CREATED / ROUTING / QC_FAILED) are rejected.
var adHocAllowedStates = map[string]bool{
	dbmodel.TaskStateAIPending:        true,
	dbmodel.TaskStateHumanPending:     true,
	dbmodel.TaskStateHumanInProgress:  true,
	dbmodel.TaskStateQAPending:        true,
	dbmodel.TaskStateQARejected:       true,
	dbmodel.TaskStateFinalized:        true,
	dbmodel.TaskStateExported:         true,
}

// ErrCapabilityNotRegistered is returned when the caller asks for a capability
// that isn't wired at startup (e.g. asking for vlm.* with no LiteLLM env).
var ErrCapabilityNotRegistered = errors.New("capability not registered")

// ErrTaskNotInvocable is returned when the task is in a state where ad-hoc
// invoke makes no sense (e.g. CREATED, ROUTING, QC failed).
var ErrTaskNotInvocable = errors.New("task is not in an ad-hoc-invocable state")

// InvokeForTask runs the requested capability against the task's asset and
// persists the result. The task state machine is not touched. The optional
// model overrides the adapter default (e.g. qwen-vl-max vs qwen-vl-plus);
// pass "" for the adapter default. Non-LLM adapters ignore model.
func (s *AdHocInvocationService) InvokeForTask(ctx context.Context, taskID uint, capability string, model string) (*AdHocInvocationResult, error) {
	if s.cap == nil || !s.cap.Has(capability) {
		return nil, fmt.Errorf("%w: %s", ErrCapabilityNotRegistered, capability)
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("load task: %w", err)
	}
	if !adHocAllowedStates[task.State] {
		return nil, fmt.Errorf("%w: state=%s", ErrTaskNotInvocable, task.State)
	}
	asset, err := s.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		return nil, fmt.Errorf("load asset: %w", err)
	}
	runID, err := newRunID()
	if err != nil {
		return nil, fmt.Errorf("gen run id: %w", err)
	}

	startedAt := time.Now()
	invokeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	resp, callErr := s.cap.Invoke(invokeCtx, CapabilityRequest{
		TaskID:         task.ID,
		AssetID:        task.AssetID,
		TraceID:        task.TraceID,
		RunID:          runID,
		CapabilityType: capability,
		AssetURI:       asset.StorageURI,
		MIME:           asset.MIME,
		Width:          asset.Width,
		Height:         asset.Height,
		Model:          model,
	})

	// Persist the AI run entry whether the call succeeded or failed, so
	// operators can audit ad-hoc invocations the same way they audit auto
	// ones. Attempt=0 marks the row as ad-hoc (auto attempts start at 1).
	run := &paymodel.AIRun{
		RunID:          runID,
		TaskID:         task.ID,
		AssetID:        task.AssetID,
		TraceID:        task.TraceID,
		CapabilityType: capability,
		Provider:       resp.Provider,
		Status:         resp.Status,
		Error:          resp.Error,
		LatencyMs:      resp.LatencyMs,
		Cost:           resp.Cost,
		EstimatedCost:  resp.EstimatedCost,
		Attempt:        0,
		StartedAt:      startedAt,
		FinishedAt:     time.Now(),
	}
	if err := s.payload.UpsertAIRun(ctx, run); err != nil {
		slog.Error("adhoc: upsert ai_run", "task_id", taskID, "capability", capability, "error", err)
	}

	out := &AdHocInvocationResult{
		RunID:      runID,
		TaskID:     task.ID,
		Capability: capability,
		Status:     resp.Status,
		LatencyMs:  resp.LatencyMs,
		Error:      resp.Error,
		Provider:   resp.Provider,
	}

	if callErr != nil || resp.Status != "success" {
		slog.Warn("adhoc: invocation unsuccessful", "task_id", taskID, "capability", capability, "status", resp.Status, "error", callErr)
		if callErr != nil && out.Error == "" {
			out.Error = callErr.Error()
		}
		return out, nil
	}

	if resp.OCR != nil {
		resp.OCR.Status = "success"
		if err := s.payload.UpsertOCRResult(ctx, resp.OCR); err != nil {
			slog.Error("adhoc: upsert ocr_result", "task_id", taskID, "error", err)
		} else if s.cache != nil {
			s.cache.SetJSON(ctx, fmt.Sprintf("ocr_result:latest:%d", taskID), resp.OCR, aiResultTTL)
		}
		out.OCR = resp.OCR
	}
	if resp.VLM != nil {
		resp.VLM.Status = "success"
		if err := s.payload.UpsertVLMResult(ctx, resp.VLM); err != nil {
			slog.Error("adhoc: upsert vlm_result", "task_id", taskID, "error", err)
		} else if s.cache != nil {
			s.cache.SetJSON(ctx, fmt.Sprintf("vlm_result:latest:%d", taskID), resp.VLM, aiResultTTL)
		}
		out.VLM = resp.VLM
	}
	if resp.Seg != nil {
		resp.Seg.Status = "success"
		if err := s.payload.UpsertSegResult(ctx, resp.Seg); err != nil {
			slog.Error("adhoc: upsert seg_result", "task_id", taskID, "error", err)
		}
		out.Seg = resp.Seg
	}
	if resp.ASR != nil {
		resp.ASR.Status = "success"
		if err := s.payload.UpsertASRResult(ctx, resp.ASR); err != nil {
			slog.Error("adhoc: upsert asr_result", "task_id", taskID, "error", err)
		} else if s.cache != nil {
			s.cache.SetJSON(ctx, fmt.Sprintf("asr_result:latest:%d", taskID), resp.ASR, aiResultTTL)
		}
		out.ASR = resp.ASR
	}
	return out, nil
}
