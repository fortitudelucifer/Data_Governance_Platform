package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// HumanAnnotationService coordinates draft / submit operations on the active
// HumanAnnotation document for a task. It does not own QA logic — that lives
// in QAService.
type HumanAnnotationService struct {
	db *repository.DB
	payload *repository.DB
}

// NewHumanAnnotationService composes the dependencies.
func NewHumanAnnotationService(dbRepo *repository.DB, payloadRepo *repository.DB) *HumanAnnotationService {
	return &HumanAnnotationService{db: dbRepo, payload: payloadRepo}
}

// HumanAnnotationDraft is the editable subset clients send when saving a
// draft.
type HumanAnnotationDraft struct {
	Shapes []paymodel.Shape       `json:"shapes"`
	Texts  map[string]string        `json:"texts"`
	Fields map[string]interface{}   `json:"fields"`
	Diff   map[string]interface{}   `json:"diff"`
}

// GetActive returns the active human annotation for a task. If none exists
// (i.e. the user has not saved yet), nil is returned.
func (s *HumanAnnotationService) GetActive(ctx context.Context, taskID uint) (*paymodel.HumanAnnotation, error) {
	return s.payload.FindActiveHumanAnnotation(ctx, taskID)
}

// Save persists or updates the active draft. The task must be in
// HUMAN_PENDING / HUMAN_IN_PROGRESS / QA_REJECTED to accept new edits. The
// state is advanced to HUMAN_IN_PROGRESS to reflect that a human is actively
// working on the task.
func (s *HumanAnnotationService) Save(ctx context.Context, taskID, userID uint, draft HumanAnnotationDraft) (*paymodel.HumanAnnotation, error) {
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !canEditHuman(task.State) {
		return nil, fmt.Errorf("task state %s does not accept human edits", task.State)
	}
	prev, err := s.payload.FindActiveHumanAnnotation(ctx, taskID)
	if err != nil {
		return nil, err
	}
	// 部分更新语义：draft 未提供的 texts/fields 沿用上一版（draft 内已有的键优先），
	// 避免"只保存 shapes"时把图片级描述等整体替换清空（前端缓存可能滞后）。
	if prev != nil {
		if draft.Texts == nil {
			draft.Texts = prev.Texts
		} else {
			for k, v := range prev.Texts {
				if _, ok := draft.Texts[k]; !ok {
					draft.Texts[k] = v
				}
			}
		}
		if draft.Fields == nil {
			draft.Fields = prev.Fields
		} else {
			for k, v := range prev.Fields {
				if _, ok := draft.Fields[k]; !ok {
					draft.Fields[k] = v
				}
			}
		}
	}
	version := 1
	basedOn := []string{}
	if prev != nil {
		version = prev.Version + 1
		basedOn = prev.BasedOnAIRuns
	}
	ha := &paymodel.HumanAnnotation{
		TaskID:         taskID,
		AssetID:        task.AssetID,
		TraceID:        task.TraceID,
		AnnotatorID:    userID,
		BasedOnAIRuns:  basedOn,
		Shapes:         draft.Shapes,
		Texts:          draft.Texts,
		Fields:         draft.Fields,
		Diff:           draft.Diff,
		QAStatus:       "draft",
		Version:        version,
		LastModifiedBy: userID,
		IsActive:       true,
		UpdatedAt:      time.Now(),
	}
	if err := s.payload.UpsertActiveHumanAnnotation(ctx, ha); err != nil {
		return nil, fmt.Errorf("save human annotation: %w", err)
	}
	// Try to bump state to HUMAN_IN_PROGRESS if not already.
	if task.State == dbmodel.TaskStateHumanPending || task.State == dbmodel.TaskStateQARejected {
		if _, err := s.db.CASUpdateState(ctx, taskID, task.State, dbmodel.TaskStateHumanInProgress, map[string]interface{}{
			"assignee_id": userID,
		}); err != nil {
			slog.Error("human_annotation: CASUpdateState →human_in_progress", "task_id", taskID, "error", err)
		}
	}
	return ha, nil
}

// canEditHuman returns whether a task's state accepts further human edits.
func canEditHuman(state string) bool {
	switch state {
	case dbmodel.TaskStateHumanPending,
		dbmodel.TaskStateHumanInProgress,
		dbmodel.TaskStateQARejected:
		return true
	}
	return false
}

// ErrNoActiveHumanAnnotation is returned when an operation expects a draft to
// exist but none does.
var ErrNoActiveHumanAnnotation = errors.New("no active human annotation")
