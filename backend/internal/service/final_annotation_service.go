package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// FinalAnnotationService writes the immutable platform output for a finalized
// task. P0 emits a flat internal JSON; P1 will additionally emit strict W3C
// JSON-LD via an export plugin (plan_v1/01 §5.1, 03 §2.4).
type FinalAnnotationService struct {
	db      *repository.DB
	payload *repository.DB
	cache   *cache.Cache // nil = no Redis
}

// NewFinalAnnotationService composes the dependencies.
func NewFinalAnnotationService(dbRepo *repository.DB, payloadRepo *repository.DB) *FinalAnnotationService {
	return &FinalAnnotationService{db: dbRepo, payload: payloadRepo}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *FinalAnnotationService) WithCache(c *cache.Cache) *FinalAnnotationService {
	s.cache = c
	return s
}

// Finalize gathers the active human annotation, the latest AI artefacts and
// the trace metadata, then writes a FinalAnnotation row idempotently per
// (task_id, version).
func (s *FinalAnnotationService) Finalize(ctx context.Context, task *dbmodel.AnnotationTask) (*paymodel.FinalAnnotation, error) {
	return s.FinalizeWith(ctx, s.payload, task)
}

// FinalizeWith is Finalize against an explicit repo view — pass a WithTx view
// to make「终稿 + 快照 + 状态迁移」一个事务(07·N4)。载荷与关系行同库之后,
// 这里曾经的「跨库非原子 + 幂等重放兜底」降级为普通事务;幂等键保留作重放安全网。
func (s *FinalAnnotationService) FinalizeWith(ctx context.Context, repo *repository.DB, task *dbmodel.AnnotationTask) (*paymodel.FinalAnnotation, error) {
	if task == nil {
		return nil, errors.New("task required")
	}
	human, err := repo.FindActiveHumanAnnotation(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	// Video tasks keep their work in mm_tracks, not HumanAnnotation.Shapes — so a
	// video finalize has no human annotation but does have active tracks.
	activeTracks, err := repo.ListActiveTracksByTask(ctx, task.ID, "", "")
	if err != nil {
		return nil, err
	}
	if human == nil && len(activeTracks) == 0 {
		return nil, ErrNoActiveHumanAnnotation
	}

	provenance := map[string]interface{}{
		"trace_id":   task.TraceID,
		"strategy":   task.RouteStrategy,
		"ai_run_ids": decodeAIRunIDs(task.AIRunIDs),
	}
	if human != nil {
		provenance["reviewer_id"] = human.ReviewerID
		provenance["annotator_id"] = human.AnnotatorID
		provenance["based_on_runs"] = human.BasedOnAIRuns
	}
	if len(activeTracks) > 0 {
		provenance["track_count"] = len(activeTracks)
	}

	// Pull caption / tags from the latest VLM result if present; Redis first.
	caption := ""
	var tags []string
	vlmFetched := false
	if s.cache != nil {
		var cached paymodel.VLMResult
		if hit, _ := s.cache.GetJSON(ctx, fmt.Sprintf("vlm_result:latest:%d", task.ID), &cached); hit {
			caption = cached.Caption
			tags = cached.Tags
			vlmFetched = true
		}
	}
	if !vlmFetched {
		if vlm, err := repo.FindLatestVLMResult(ctx, task.ID); err == nil && vlm != nil {
			caption = vlm.Caption
			tags = vlm.Tags
		}
	}

	fa := &paymodel.FinalAnnotation{
		TaskID:     task.ID,
		AssetID:    task.AssetID,
		DatasetID:  task.DatasetID,
		TraceID:    task.TraceID,
		Strategy:   task.RouteStrategy,
		Caption:    caption,
		Tags:       tags,
		Provenance: provenance,
		Version:    task.Version,
		CreatedAt:  time.Now(),
	}
	if human != nil {
		fa.Shapes, fa.Texts, fa.Fields = human.Shapes, human.Texts, human.Fields
	}
	fa.ID = repository.NewHexID() // stable string id so snapshots can reference it
	if err := repo.InsertFinalAnnotation(ctx, fa); err != nil {
		return nil, err
	}

	// B3.2: snapshot active tracks (video) into mm_track_snapshots — the
	// drift-safe export source, keyed by {final_annotation_id, track_id}
	// (idempotent, replay-safe).
	var finalizedBy uint
	if human != nil && human.ReviewerID != nil {
		finalizedBy = *human.ReviewerID
	}
	for _, t := range activeTracks {
		snap := &paymodel.TrackSnapshot{
			TaskID: task.ID, DatasetID: task.DatasetID, AssetID: task.AssetID,
			FinalAnnotationID: fa.ID, TrackID: t.TrackID,
			SourceTrackObjectID: t.ID, SourceTrackVersion: t.Version,
			Label: t.Label, Kind: t.Kind, Color: t.Color, Attrs: t.Attrs,
			Keyframes: t.Keyframes, FinalizedBy: finalizedBy, FinalizedAt: fa.CreatedAt,
		}
		if err := repo.UpsertTrackSnapshot(ctx, snap); err != nil {
			return nil, fmt.Errorf("snapshot track %s: %w", t.ID, err)
		}
	}
	return fa, nil
}

// GetLatest returns the most recent FinalAnnotation for a task.
func (s *FinalAnnotationService) GetLatest(ctx context.Context, taskID uint) (*paymodel.FinalAnnotation, error) {
	return s.payload.FindLatestFinalAnnotation(ctx, taskID)
}
