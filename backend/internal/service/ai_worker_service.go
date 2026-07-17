package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// AIWorkerConfig captures the runtime tuning knobs for the AIWorker.
type AIWorkerConfig struct {
	Interval        time.Duration
	BatchSize       int
	Concurrency     int // PH-11：批内并发上限；<=0 时回退到 BatchSize
	LeaseTTL        time.Duration
	MaxRetries      int
	InvokeTimeout   time.Duration
	RetryMaxBackoff time.Duration // cap on exponential backoff between retries
}

// DefaultAIWorkerConfig returns sensible dev defaults.
func DefaultAIWorkerConfig() AIWorkerConfig {
	return AIWorkerConfig{
		Interval:        2 * time.Second,
		BatchSize:       4,
		Concurrency:     4,
		LeaseTTL:        60 * time.Second,
		MaxRetries:      2,
		InvokeTimeout:   90 * time.Second,
		RetryMaxBackoff: 30 * time.Second,
	}
}

// AIWorker is the DB-polling worker that drives ROUTING and AI_PENDING tasks
// through the state machine (plan_v1/05 §6 step 4 + ADR-08). It enforces the
// retry budget, applies exponential backoff via next_attempt_at, and falls
// back to HUMAN_PENDING when retries are exhausted.
const aiResultTTL = 30 * time.Minute

type AIWorker struct {
	cfg    AIWorkerConfig
	db     *repository.DB
	payload  *repository.DB
	router *RouterService
	cap    *CapabilityService
	cache  *cache.Cache // nil = no Redis

	stopCh chan struct{}
	once   sync.Once
	wg     sync.WaitGroup // tracks in-flight runRouting/runAI calls for graceful shutdown
	sem    chan struct{}  // PH-11：批内并发上限信号量（缓冲大小 = Concurrency）
}

// WithCache injects the Redis cache; call from main.go after construction.
func (w *AIWorker) WithCache(c *cache.Cache) *AIWorker {
	w.cache = c
	return w
}

// NewAIWorker composes the dependencies. router and cap may be nil during
// boot; in that case no state will advance and the worker logs a warning.
func NewAIWorker(cfg AIWorkerConfig, dbRepo *repository.DB, payloadRepo *repository.DB, router *RouterService, capSvc *CapabilityService) *AIWorker {
	if cfg.Interval <= 0 {
		cfg = DefaultAIWorkerConfig()
	}
	if cfg.RetryMaxBackoff <= 0 {
		cfg.RetryMaxBackoff = DefaultAIWorkerConfig().RetryMaxBackoff
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = cfg.BatchSize
		if cfg.Concurrency <= 0 {
			cfg.Concurrency = DefaultAIWorkerConfig().Concurrency
		}
	}
	return &AIWorker{
		cfg:    cfg,
		db:     dbRepo,
		payload:  payloadRepo,
		router: router,
		cap:    capSvc,
		stopCh: make(chan struct{}),
		sem:    make(chan struct{}, cfg.Concurrency),
	}
}

// Start launches the worker loop in a goroutine. Safe to call once per
// process; subsequent calls are no-ops.
//
// The loop goroutine is tracked via wg so Stop can block until both the loop
// itself and any in-flight task work have drained.
func (w *AIWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.loop(ctx)
}

// Stop signals the worker to shut down and waits for in-flight task processing
// to finish. It closes the stop channel (idempotent), then blocks until either:
//
//   - all running runRouting / runAI invocations return, or
//   - the supplied ctx is cancelled (typically a 30s grace deadline from main).
//
// Returns ctx.Err() if the grace period elapsed before workers drained.
// Returns nil on a clean drain.
//
// Concurrent calls are safe — the stop signal is fired exactly once via once.Do,
// and the WaitGroup.Wait is shared across callers.
func (w *AIWorker) Stop(ctx context.Context) error {
	w.once.Do(func() { close(w.stopCh) })

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *AIWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	tick := time.NewTicker(w.cfg.Interval)
	defer tick.Stop()
	slog.Info("ai_worker started", "interval", w.cfg.Interval, "batch", w.cfg.BatchSize, "concurrency", w.cfg.Concurrency, "lease", w.cfg.LeaseTTL, "retries", w.cfg.MaxRetries)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-tick.C:
			w.tickRouting(ctx)
			w.tickAI(ctx)
		}
	}
}

// tickRouting picks ROUTING tasks and runs the L1 router against them.
// Each runRouting is tracked via WaitGroup so Stop can block until they drain.
func (w *AIWorker) tickRouting(ctx context.Context) {
	leaseUntil := time.Now().Add(w.cfg.LeaseTTL)
	tasks, err := w.db.LeaseDueTasks(ctx, []string{dbmodel.TaskStateRouting}, leaseUntil, w.cfg.BatchSize)
	if err != nil {
		slog.Error("ai_worker: lease routing failed", "error", err)
		return
	}
	for i := range tasks {
		w.dispatch(ctx, &tasks[i], w.runRouting)
	}
}

// dispatch 在并发上限内异步执行 run（PH-11）：用信号量限流，慢调用不再阻塞整批。
// 信号量获取受 ctx 约束（停机时不会卡住）；wg 跟踪以便优雅停机排空。
func (w *AIWorker) dispatch(ctx context.Context, task *dbmodel.AnnotationTask, run func(context.Context, *dbmodel.AnnotationTask)) {
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() { <-w.sem }()
		run(ctx, task)
	}()
}

func (w *AIWorker) runRouting(ctx context.Context, task *dbmodel.AnnotationTask) {
	asset, err := w.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		slog.Error("ai_worker: load asset failed", "asset_id", task.AssetID, "error", err)
		w.markFailure(ctx, task, "asset lookup failed", err)
		return
	}
	if w.router == nil {
		slog.Warn("ai_worker: router not configured, falling back to HUMAN_ONLY", "task_id", task.ID)
		if _, err := w.db.CASUpdateState(ctx, task.ID, dbmodel.TaskStateRouting, dbmodel.TaskStateHumanPending, map[string]interface{}{
			"route_strategy":      dbmodel.RouteHumanOnly,
			"human_only_fallback": true,
			"lease_until":         nil,
		}); err != nil {
			slog.Error("ai_worker: CASUpdateState routing→human_pending", "task_id", task.ID, "error", err)
		}
		return
	}
	rr, err := w.router.Route(ctx, task, asset, nil)
	if err != nil {
		slog.Error("ai_worker: routing failed", "error", err)
		w.markFailure(ctx, task, "routing failed", err)
		return
	}
	nextState := dbmodel.TaskStateAIPending
	if rr.Strategy == dbmodel.RouteHumanOnly {
		nextState = dbmodel.TaskStateHumanPending
	}
	_, err = w.db.CASUpdateState(ctx, task.ID, dbmodel.TaskStateRouting, nextState, map[string]interface{}{
		"route_strategy":  rr.Strategy,
		"strategy_origin": dbmodel.StrategyOriginAuto,
		"lease_until":     nil,
		"next_attempt_at": nil,
	})
	if err != nil {
		slog.Error("ai_worker: CASUpdateState routing→next_state", "next_state", nextState, "task_id", task.ID, "error", err)
	}
}

// tickAI picks AI_PENDING tasks and runs the configured capability.
// Each runAI is tracked via WaitGroup so Stop can block until they drain.
func (w *AIWorker) tickAI(ctx context.Context) {
	leaseUntil := time.Now().Add(w.cfg.LeaseTTL)
	tasks, err := w.db.LeaseDueTasks(ctx, []string{dbmodel.TaskStateAIPending}, leaseUntil, w.cfg.BatchSize)
	if err != nil {
		slog.Error("ai_worker: lease AI tasks failed", "error", err)
		return
	}
	for i := range tasks {
		w.dispatch(ctx, &tasks[i], w.runAI)
	}
}

func (w *AIWorker) runAI(ctx context.Context, task *dbmodel.AnnotationTask) {
	asset, err := w.db.FindAssetByID(ctx, task.AssetID)
	if err != nil {
		w.markFailure(ctx, task, "asset lookup failed", err)
		return
	}
	if w.cap == nil {
		w.fallbackToHuman(ctx, task, "capability service not configured")
		return
	}
	capability := capabilityForStrategy(task.RouteStrategy)
	if capability == "" {
		w.fallbackToHuman(ctx, task, "no capability mapping for strategy "+task.RouteStrategy)
		return
	}
	if !w.cap.Has(capability) {
		w.fallbackToHuman(ctx, task, "capability "+capability+" not registered")
		return
	}

	runID, err := newRunID()
	if err != nil {
		w.markFailure(ctx, task, "run id", err)
		return
	}

	startedAt := time.Now()
	invokeCtx, cancel := context.WithTimeout(ctx, w.cfg.InvokeTimeout)
	defer cancel()

	resp, callErr := w.cap.Invoke(invokeCtx, CapabilityRequest{
		TaskID:         task.ID,
		AssetID:        task.AssetID,
		TraceID:        task.TraceID,
		RunID:          runID,
		CapabilityType: capability,
		AssetURI:       asset.StorageURI,
		MIME:           asset.MIME,
		Width:          asset.Width,
		Height:         asset.Height,
	})

	// Persist the AI run entry idempotently.
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
		Attempt:        task.RetryCount + 1,
		StartedAt:      startedAt,
		FinishedAt:     time.Now(),
	}
	if err := w.payload.UpsertAIRun(ctx, run); err != nil {
		slog.Error("ai_worker: upsert ai_run", "task_id", task.ID, "error", err)
	}

	if callErr != nil || resp.Status != "success" {
		w.handleAIFailure(ctx, task, runID, resp, callErr)
		return
	}

	// Persist OCR / VLM result idempotently; write-through to Redis.
	if resp.OCR != nil {
		resp.OCR.Status = "success"
		if err := w.payload.UpsertOCRResult(ctx, resp.OCR); err != nil {
			slog.Error("ai_worker: upsert ocr_result", "task_id", task.ID, "error", err)
		} else if w.cache != nil {
			w.cache.SetJSON(ctx, fmt.Sprintf("ocr_result:latest:%d", task.ID), resp.OCR, aiResultTTL)
		}
	}
	if resp.VLM != nil {
		resp.VLM.Status = "success"
		if err := w.payload.UpsertVLMResult(ctx, resp.VLM); err != nil {
			slog.Error("ai_worker: upsert vlm_result", "task_id", task.ID, "error", err)
		} else if w.cache != nil {
			w.cache.SetJSON(ctx, fmt.Sprintf("vlm_result:latest:%d", task.ID), resp.VLM, aiResultTTL)
		}
	}
	if resp.ASR != nil {
		resp.ASR.Status = "success"
		if err := w.payload.UpsertASRResult(ctx, resp.ASR); err != nil {
			slog.Error("ai_worker: upsert asr_result", "task_id", task.ID, "error", err)
		} else if w.cache != nil {
			w.cache.SetJSON(ctx, fmt.Sprintf("asr_result:latest:%d", task.ID), resp.ASR, aiResultTTL)
		}
	}

	runs := append(decodeAIRunIDs(task.AIRunIDs), runID)
	runJSON, _ := json.Marshal(runs)

	_, err = w.db.CASUpdateState(ctx, task.ID, dbmodel.TaskStateAIPending, dbmodel.TaskStateHumanPending, map[string]interface{}{
		"ai_run_ids":      dbmodel.JSON(runJSON),
		"cost":            task.Cost + resp.Cost,
		"latency_ms":      task.LatencyMs + resp.LatencyMs,
		"lease_until":     nil,
		"next_attempt_at": nil,
		"error":           nil,
	})
	if err != nil {
		slog.Error("ai_worker: CASUpdateState ai→human_pending", "task_id", task.ID, "error", err)
	}
}

func (w *AIWorker) handleAIFailure(ctx context.Context, task *dbmodel.AnnotationTask, runID string, resp CapabilityResponse, callErr error) {
	errMsg := resp.Error
	if errMsg == "" && callErr != nil {
		errMsg = FriendlyLLMError(callErr)
	}
	// Permanent errors (auth / access-denied / model-not-found / bad-request)
	// can never succeed on retry — fail straight to human fallback instead of
	// burning the retry budget + cost + latency on a guaranteed-403.
	permanent := isPermanentLLMError(callErr)
	retry := task.RetryCount + 1
	if permanent || retry > w.cfg.MaxRetries {
		runs := append(decodeAIRunIDs(task.AIRunIDs), runID)
		runJSON, _ := json.Marshal(runs)
		if _, casErr := w.db.CASUpdateState(ctx, task.ID, dbmodel.TaskStateAIPending, dbmodel.TaskStateHumanPending, map[string]interface{}{
			"ai_run_ids":          dbmodel.JSON(runJSON),
			"retry_count":         retry,
			"human_only_fallback": true,
			"error":               errorJSON(errMsg),
			"lease_until":         nil,
			"next_attempt_at":     nil,
		}); casErr != nil {
			slog.Error("ai_worker: CASUpdateState ai_pending→human_pending", "task_id", task.ID, "error", casErr)
		}
		if permanent {
			slog.Warn("ai_worker: permanent error, no retry", "task_id", task.ID, "error", errMsg)
		} else {
			slog.Warn("ai_worker: retries exhausted", "task_id", task.ID, "retries", retry, "error", errMsg)
		}
		return
	}
	// Schedule a backoff retry by clearing lease and bumping next_attempt_at.
	// w.cfg.RetryMaxBackoff is guaranteed non-zero by NewAIWorker.
	backoff := time.Duration(1<<retry) * time.Second
	if backoff > w.cfg.RetryMaxBackoff {
		backoff = w.cfg.RetryMaxBackoff
	}
	next := time.Now().Add(backoff)
	updates := map[string]interface{}{
		"retry_count":     retry,
		"error":           errorJSON(errMsg),
		"lease_until":     nil,
		"next_attempt_at": next,
	}
	if err := w.db.UpdateAnnotationTask(ctx, task.ID, updates); err != nil {
		slog.Error("ai_worker: schedule retry UpdateAnnotationTask", "task_id", task.ID, "error", err)
	}
	slog.Info("ai_worker: retry scheduled", "task_id", task.ID, "retry", retry, "backoff", backoff, "error", errMsg)
}

func (w *AIWorker) fallbackToHuman(ctx context.Context, task *dbmodel.AnnotationTask, reason string) {
	if _, err := w.db.CASUpdateState(ctx, task.ID, dbmodel.TaskStateAIPending, dbmodel.TaskStateHumanPending, map[string]interface{}{
		"human_only_fallback": true,
		"error":               errorJSON(reason),
		"lease_until":         nil,
		"next_attempt_at":     nil,
	}); err != nil {
		slog.Error("ai_worker: fallbackToHuman CASUpdateState", "task_id", task.ID, "error", err)
	}
}

func (w *AIWorker) markFailure(ctx context.Context, task *dbmodel.AnnotationTask, reason string, err error) {
	msg := reason
	if err != nil {
		msg = fmt.Sprintf("%s: %v", reason, err)
	}
	if updateErr := w.db.UpdateAnnotationTask(ctx, task.ID, map[string]interface{}{
		"error":           errorJSON(msg),
		"lease_until":     nil,
		"next_attempt_at": time.Now().Add(15 * time.Second),
	}); updateErr != nil {
		slog.Error("ai_worker: markFailure UpdateAnnotationTask", "task_id", task.ID, "error", updateErr)
	}
}

func capabilityForStrategy(strategy string) string {
	switch strategy {
	case dbmodel.RouteOCRFirst:
		return CapabilityOCRStructure
	case dbmodel.RouteVLMFirst:
		return CapabilityVLMStructured
	case dbmodel.RouteASRFirst:
		return CapabilityASRTranscribe
	case dbmodel.RouteVideoDetectFirst:
		return CapabilityVideoDetectTrack
	}
	return ""
}

func errorJSON(msg string) dbmodel.JSON {
	if msg == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]string{"message": msg})
	return dbmodel.JSON(b)
}

func decodeAIRunIDs(j dbmodel.JSON) []string {
	if len(j) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(j, &ids); err != nil {
		return nil
	}
	return ids
}

func newRunID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
