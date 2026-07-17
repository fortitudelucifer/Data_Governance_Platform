package service

import (
	"context"
	"fmt"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// TraceService exposes read access to the unified trace log + AI run history
// for a task. P0 surfaces the three views needed by the workbench:
//
//	- ai_runs        : every AI capability invocation
//	- trace_logs     : every adapter / litellm call (includes failures)
//	- routing_result : the latest L1 routing decision
type TraceService struct {
	payload *repository.DB
	cache *cache.Cache // nil = no Redis
}

// NewTraceService composes the dependencies.
func NewTraceService(payloadRepo *repository.DB) *TraceService {
	return &TraceService{payload: payloadRepo}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *TraceService) WithCache(c *cache.Cache) *TraceService {
	s.cache = c
	return s
}

// TaskTrace bundles all trace artefacts for one task.
type TaskTrace struct {
	Routing  *paymodel.RoutingResult `json:"routing"`
	AIRuns   []paymodel.AIRun        `json:"ai_runs"`
	TraceLogs []paymodel.TraceLog    `json:"trace_logs"`
	OCR      *paymodel.OCRResult     `json:"ocr"`
	VLM      *paymodel.VLMResult     `json:"vlm"`
	Final    *paymodel.FinalAnnotation `json:"final,omitempty"`
}

// GetByTask returns the full trace bundle for a task.
func (s *TraceService) GetByTask(ctx context.Context, taskID uint) (*TaskTrace, error) {
	t := &TaskTrace{}

	// Routing result: Redis first.
	routingFetched := false
	if s.cache != nil {
		var cached paymodel.RoutingResult
		if hit, _ := s.cache.GetJSON(ctx, fmt.Sprintf("routing:latest:%d", taskID), &cached); hit {
			t.Routing = &cached
			routingFetched = true
		}
	}
	if !routingFetched {
		if r, err := s.payload.FindLatestRoutingResult(ctx, taskID); err == nil {
			t.Routing = r
		}
	}

	runs, err := s.payload.FindAIRunsByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	t.AIRuns = runs

	logs, err := s.payload.FindTraceLogsByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	t.TraceLogs = logs

	// OCR result: Redis first.
	if s.cache != nil {
		var cached paymodel.OCRResult
		if hit, _ := s.cache.GetJSON(ctx, fmt.Sprintf("ocr_result:latest:%d", taskID), &cached); hit {
			t.OCR = &cached
		} else if ocr, err := s.payload.FindLatestOCRResult(ctx, taskID); err == nil {
			t.OCR = ocr
		}
	} else if ocr, err := s.payload.FindLatestOCRResult(ctx, taskID); err == nil {
		t.OCR = ocr
	}

	// VLM result: Redis first.
	if s.cache != nil {
		var cached paymodel.VLMResult
		if hit, _ := s.cache.GetJSON(ctx, fmt.Sprintf("vlm_result:latest:%d", taskID), &cached); hit {
			t.VLM = &cached
		} else if vlm, err := s.payload.FindLatestVLMResult(ctx, taskID); err == nil {
			t.VLM = vlm
		}
	} else if vlm, err := s.payload.FindLatestVLMResult(ctx, taskID); err == nil {
		t.VLM = vlm
	}

	if fa, err := s.payload.FindLatestFinalAnnotation(ctx, taskID); err == nil {
		t.Final = fa
	}
	return t, nil
}
