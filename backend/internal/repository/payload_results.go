package repository

// payload_results.go — AI 结果族(routing/run/ocr/vlm/seg/asr,合表 ai_results)
// 与 trace_logs 的 Postgres 载荷仓储(执行方案-07)。

import (
	"context"
	"fmt"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// AI 结果族的 kind 值(ai_results.kind;无 DB CHECK,与 asset_derivatives 同理)。
const (
	aiKindRouting = "routing"
	aiKindRun     = "run"
	aiKindOCR     = "ocr"
	aiKindVLM     = "vlm"
	aiKindSeg     = "seg"
	aiKindASR     = "asr"
)

// ---- Routing results ----------------------------------------------------

// InsertRoutingResult writes a new immutable routing result.
func (r *DB) InsertRoutingResult(ctx context.Context, result *paymodel.RoutingResult) error {
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}
	if result.ID == "" {
		result.ID = NewHexID()
	}
	payload, err := marshalPayload(result)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO ai_results (id, kind, task_id, asset_id, version, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?, now())`,
		result.ID, aiKindRouting, result.TaskID, result.AssetID, result.Version, payload, result.CreatedAt,
	).Error
}

// FindLatestRoutingResult returns the highest-version routing result for a task.
func (r *DB) FindLatestRoutingResult(ctx context.Context, taskID uint) (*paymodel.RoutingResult, error) {
	return firstPayload[paymodel.RoutingResult](ctx, r.DB,
		`SELECT payload FROM ai_results WHERE kind = ? AND task_id = ? ORDER BY version DESC LIMIT 1`,
		aiKindRouting, taskID)
}

// ---- AI runs --------------------------------------------------------------

// UpsertAIRun writes / updates a single AI run record idempotently keyed by
// (task_id, capability_type, run_id) — 与旧实现同一把幂等键,现在是部分唯一索引。
func (r *DB) UpsertAIRun(ctx context.Context, run *paymodel.AIRun) error {
	if run.ID == "" {
		run.ID = NewHexID()
	}
	payload, err := marshalPayload(run)
	if err != nil {
		return err
	}
	// 冲突时保留既有 id(EXCLUDED.payload 带着新生成的 id,用行上的旧 id 盖回去)。
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO ai_results (id, kind, task_id, asset_id, run_id, capability_type, started_at, payload, created_at, updated_at)
		 VALUES (?, 'run', ?, ?, ?, ?, ?, ?::jsonb, now(), now())
		 ON CONFLICT (task_id, capability_type, run_id) WHERE kind = 'run'
		 DO UPDATE SET payload    = EXCLUDED.payload || jsonb_build_object('id', ai_results.id),
		               started_at = EXCLUDED.started_at,
		               updated_at = now()`,
		run.ID, run.TaskID, run.AssetID, run.RunID, run.CapabilityType, run.StartedAt, payload,
	).Error
}

// FindAIRunsByTask returns all AI run records for a task, oldest first.
func (r *DB) FindAIRunsByTask(ctx context.Context, taskID uint) ([]paymodel.AIRun, error) {
	rows, err := listPayloads[paymodel.AIRun](ctx, r.DB,
		`SELECT payload FROM ai_results WHERE kind = 'run' AND task_id = ? ORDER BY started_at`, taskID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil // 旧实现返回 nil 切片,保持一致
	}
	return rows, nil
}

// ---- OCR / VLM / Seg / ASR results ----------------------------------------

// upsertAIResult idempotently stores a capability result keyed by (task_id, run_id).
func (r *DB) upsertAIResult(ctx context.Context, kind string, id, runID string, taskID, assetID uint, createdAt time.Time, doc any) error {
	payload, err := marshalPayload(doc)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO ai_results (id, kind, task_id, asset_id, run_id, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?, now())
		 ON CONFLICT (kind, task_id, run_id) WHERE kind IN ('ocr', 'vlm', 'seg', 'asr')
		 DO UPDATE SET payload    = EXCLUDED.payload || jsonb_build_object('id', ai_results.id),
		               updated_at = now()`,
		id, kind, taskID, assetID, runID, payload, createdAt,
	).Error
}

// UpsertOCRResult idempotently stores an OCR result keyed by (task_id, run_id).
func (r *DB) UpsertOCRResult(ctx context.Context, result *paymodel.OCRResult) error {
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}
	if result.ID == "" {
		result.ID = NewHexID()
	}
	return r.upsertAIResult(ctx, aiKindOCR, result.ID, result.RunID, result.TaskID, result.AssetID, result.CreatedAt, result)
}

// FindLatestOCRResult returns the most recent OCR result for a task.
func (r *DB) FindLatestOCRResult(ctx context.Context, taskID uint) (*paymodel.OCRResult, error) {
	return firstPayload[paymodel.OCRResult](ctx, r.DB,
		`SELECT payload FROM ai_results WHERE kind = ? AND task_id = ? ORDER BY created_at DESC LIMIT 1`,
		aiKindOCR, taskID)
}

// UpsertVLMResult idempotently stores a VLM result keyed by (task_id, run_id).
func (r *DB) UpsertVLMResult(ctx context.Context, result *paymodel.VLMResult) error {
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}
	if result.ID == "" {
		result.ID = NewHexID()
	}
	return r.upsertAIResult(ctx, aiKindVLM, result.ID, result.RunID, result.TaskID, result.AssetID, result.CreatedAt, result)
}

// FindLatestVLMResult returns the most recent VLM result for a task.
func (r *DB) FindLatestVLMResult(ctx context.Context, taskID uint) (*paymodel.VLMResult, error) {
	return firstPayload[paymodel.VLMResult](ctx, r.DB,
		`SELECT payload FROM ai_results WHERE kind = ? AND task_id = ? ORDER BY created_at DESC LIMIT 1`,
		aiKindVLM, taskID)
}

// UpsertSegResult writes a segmentation result idempotently per (task_id, run_id).
func (r *DB) UpsertSegResult(ctx context.Context, result *paymodel.SegResult) error {
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}
	if result.ID == "" {
		result.ID = NewHexID()
	}
	return r.upsertAIResult(ctx, aiKindSeg, result.ID, result.RunID, result.TaskID, result.AssetID, result.CreatedAt, result)
}

// FindLatestSegResult returns the most recent segmentation result for a task.
func (r *DB) FindLatestSegResult(ctx context.Context, taskID uint) (*paymodel.SegResult, error) {
	return firstPayload[paymodel.SegResult](ctx, r.DB,
		`SELECT payload FROM ai_results WHERE kind = ? AND task_id = ? ORDER BY created_at DESC LIMIT 1`,
		aiKindSeg, taskID)
}

// UpsertASRResult writes an ASR result idempotently per (task_id, run_id).
func (r *DB) UpsertASRResult(ctx context.Context, result *paymodel.ASRResult) error {
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now()
	}
	if result.ID == "" {
		result.ID = NewHexID()
	}
	return r.upsertAIResult(ctx, aiKindASR, result.ID, result.RunID, result.TaskID, result.AssetID, result.CreatedAt, result)
}

// FindLatestASRResult returns the most recent ASR result for a task.
func (r *DB) FindLatestASRResult(ctx context.Context, taskID uint) (*paymodel.ASRResult, error) {
	return firstPayload[paymodel.ASRResult](ctx, r.DB,
		`SELECT payload FROM ai_results WHERE kind = ? AND task_id = ? ORDER BY created_at DESC LIMIT 1`,
		aiKindASR, taskID)
}

// ---- Trace logs -------------------------------------------------------------

// InsertTraceLog appends a single trace record. 无外键:观测数据要活得比它指向
// 的对象久,且文本线调用没有任务(task_id=0)。
func (r *DB) InsertTraceLog(ctx context.Context, log *paymodel.TraceLog) error {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}
	if log.ID == "" {
		log.ID = NewHexID()
	}
	payload, err := marshalPayload(log)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO trace_logs (id, trace_id, task_id, run_id, capability_type, provider, model, status, latency_ms, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?)`,
		log.ID, log.TraceID, log.TaskID, log.RunID, log.CapabilityType,
		log.Provider, log.Model, log.Status, log.LatencyMs, payload, log.CreatedAt,
	).Error
}

// FindTraceLogsByTask returns all trace records for a task ordered by time.
func (r *DB) FindTraceLogsByTask(ctx context.Context, taskID uint) ([]paymodel.TraceLog, error) {
	rows, err := listPayloads[paymodel.TraceLog](ctx, r.DB,
		`SELECT payload FROM trace_logs WHERE task_id = ? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}

// ---- Cross-table cleanup ------------------------------------------------------

// DeleteMultiModalByAsset removes all payload rows for an asset (and its task
// IDs) — used when hard-deleting a sample. 外键级联在资产行删除时也会兜底;
// 这里显式先删,保持 DeleteAsset 的既有顺序(载荷先走、资产行最后)。
// trace_logs 没有 asset 维度(与旧实现一致:按 task 清)。
func (r *DB) DeleteMultiModalByAsset(ctx context.Context, assetID uint, taskIDs []uint) error {
	g := r.DB.WithContext(ctx)
	tables := []string{
		"human_annotations", "final_annotations", "ai_results",
		"annotation_tracks", "track_snapshots",
	}
	for _, t := range tables {
		q := fmt.Sprintf(`DELETE FROM %s WHERE asset_id = ?`, t)
		args := []any{assetID}
		if len(taskIDs) > 0 {
			q = fmt.Sprintf(`DELETE FROM %s WHERE asset_id = ? OR task_id IN ?`, t)
			args = append(args, taskIDs)
		}
		if err := g.Exec(q, args...).Error; err != nil {
			return fmt.Errorf("delete %s for asset %d: %w", t, assetID, err)
		}
	}
	if len(taskIDs) > 0 {
		if err := g.Exec(`DELETE FROM trace_logs WHERE task_id IN ?`, taskIDs).Error; err != nil {
			return fmt.Errorf("delete trace_logs for asset %d: %w", assetID, err)
		}
	}
	return nil
}
