package repository

// payload_text.go — 文本多模型候选 / Judge 运行的 Postgres 载荷仓储
// (执行方案-07)。
// 幂等键 = run_id(UNIQUE 列),与旧实现同一把键。

import (
	"context"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// ---- Candidates ------------------------------------------------------------

// UpsertTextAICandidate writes one provider/model candidate result keyed by run_id.
func (r *DB) UpsertTextAICandidate(ctx context.Context, cand *paymodel.TextAICandidate) error {
	if cand.CreatedAt.IsZero() {
		cand.CreatedAt = time.Now()
	}
	if cand.ID == "" {
		cand.ID = NewHexID()
	}
	payload, err := marshalPayload(cand)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO text_ai_candidates (id, run_id, dataset_id, doc_key, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?::jsonb, ?, now())
		 ON CONFLICT (run_id)
		 DO UPDATE SET payload    = EXCLUDED.payload || jsonb_build_object('id', text_ai_candidates.id),
		               updated_at = now()`,
		cand.ID, cand.RunID, cand.DatasetID, cand.DocKey, payload, cand.CreatedAt,
	).Error
}

// FindTextAICandidates returns recent candidates for a document, newest first.
func (r *DB) FindTextAICandidates(ctx context.Context, datasetID uint, docKey string) ([]paymodel.TextAICandidate, error) {
	return listPayloads[paymodel.TextAICandidate](ctx, r.DB,
		`SELECT payload FROM text_ai_candidates WHERE dataset_id = ? AND doc_key = ? ORDER BY created_at DESC`,
		datasetID, docKey)
}

// FindTextAICandidateByRunID returns one candidate run; nil when absent.
func (r *DB) FindTextAICandidateByRunID(ctx context.Context, runID string) (*paymodel.TextAICandidate, error) {
	return firstPayload[paymodel.TextAICandidate](ctx, r.DB,
		`SELECT payload FROM text_ai_candidates WHERE run_id = ? LIMIT 1`, runID)
}

// DeleteTextAICandidate removes one candidate run scoped to a document.
func (r *DB) DeleteTextAICandidate(ctx context.Context, datasetID uint, docKey string, runID string) (bool, error) {
	res := r.DB.WithContext(ctx).Exec(
		`DELETE FROM text_ai_candidates WHERE dataset_id = ? AND doc_key = ? AND run_id = ?`,
		datasetID, docKey, runID)
	return res.RowsAffected > 0, res.Error
}

// MarkTextAICandidateAdopted records adoption metrics(采纳率/血缘审计)。
// adopted_count 是累加语义($inc 的 jsonb 对等物)。
func (r *DB) MarkTextAICandidateAdopted(ctx context.Context, runID string, adoptedCount int, userID uint) error {
	now := time.Now()
	return r.DB.WithContext(ctx).Exec(
		`UPDATE text_ai_candidates
		 SET updated_at = now(),
		     payload = payload || jsonb_build_object(
		        'adopted_count', COALESCE((payload->>'adopted_count')::int, 0) + ?,
		        'last_adopted_by', ?::bigint,
		        'last_adopted_at', to_jsonb(?::timestamptz))
		 WHERE run_id = ?`,
		adoptedCount, userID, now, runID,
	).Error
}

// ---- Judge runs -------------------------------------------------------------

// UpsertTextAIJudgeRun writes one Judge Agent run keyed by run_id.
func (r *DB) UpsertTextAIJudgeRun(ctx context.Context, run *paymodel.TextAIJudgeRun) error {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	if run.ID == "" {
		run.ID = NewHexID()
	}
	payload, err := marshalPayload(run)
	if err != nil {
		return err
	}
	return r.DB.WithContext(ctx).Exec(
		`INSERT INTO text_ai_judge_runs (id, run_id, dataset_id, doc_key, payload, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?::jsonb, ?, now())
		 ON CONFLICT (run_id)
		 DO UPDATE SET payload    = EXCLUDED.payload || jsonb_build_object('id', text_ai_judge_runs.id),
		               updated_at = now()`,
		run.ID, run.RunID, run.DatasetID, run.DocKey, payload, run.CreatedAt,
	).Error
}

// FindTextAIJudgeRuns returns recent Judge Agent runs for a document, newest first.
func (r *DB) FindTextAIJudgeRuns(ctx context.Context, datasetID uint, docKey string) ([]paymodel.TextAIJudgeRun, error) {
	return listPayloads[paymodel.TextAIJudgeRun](ctx, r.DB,
		`SELECT payload FROM text_ai_judge_runs WHERE dataset_id = ? AND doc_key = ? ORDER BY created_at DESC`,
		datasetID, docKey)
}

// FindTextAIJudgeRunByRunID returns one Judge Agent run; nil when absent.
func (r *DB) FindTextAIJudgeRunByRunID(ctx context.Context, runID string) (*paymodel.TextAIJudgeRun, error) {
	return firstPayload[paymodel.TextAIJudgeRun](ctx, r.DB,
		`SELECT payload FROM text_ai_judge_runs WHERE run_id = ? LIMIT 1`, runID)
}

// MarkTextAIJudgeRunAdopted records adoption metrics for Judge suggestions.
func (r *DB) MarkTextAIJudgeRunAdopted(ctx context.Context, runID string, adoptedCount int, userID uint) error {
	now := time.Now()
	return r.DB.WithContext(ctx).Exec(
		`UPDATE text_ai_judge_runs
		 SET updated_at = now(),
		     payload = payload || jsonb_build_object(
		        'adopted_count', COALESCE((payload->>'adopted_count')::int, 0) + ?,
		        'last_adopted_by', ?::bigint,
		        'last_adopted_at', to_jsonb(?::timestamptz))
		 WHERE run_id = ?`,
		adoptedCount, userID, now, runID,
	).Error
}
