package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"gorm.io/gorm"
)

// QAService implements the P0 minimal QA gate (plan_v1/02 §2.1 +
// plan_v1/01 §7.1). No sampling, dual review, or scoring — that is P1.
type QAService struct {
	db      *repository.DB
	payload *repository.DB
	final   *FinalAnnotationService
}

// NewQAService composes the dependencies. The final service is invoked on
// QA pass to immediately persist the FinalAnnotation row.
func NewQAService(dbRepo *repository.DB, payloadRepo *repository.DB, final *FinalAnnotationService) *QAService {
	return &QAService{db: dbRepo, payload: payloadRepo, final: final}
}

// Submit transitions a task with an active draft into QA_PENDING.
func (s *QAService) Submit(ctx context.Context, taskID, userID uint) error {
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	switch task.State {
	case dbmodel.TaskStateHumanInProgress, dbmodel.TaskStateHumanPending, dbmodel.TaskStateQARejected:
	default:
		return fmt.Errorf("task state %s cannot be submitted", task.State)
	}
	// Rework: every anchored comment the reviewer left must be addressed, or the
	// task bounces straight back on the next review. Only gates re-submission —
	// a first submission has no comments to answer.
	if task.State == dbmodel.TaskStateQARejected {
		open, err := s.payload.CountOpenReviewComments(ctx, taskID)
		if err != nil {
			return err
		}
		if open > 0 {
			return fmt.Errorf("%w（剩余 %d 条）", ErrOpenCommentsRemain, open)
		}
	}
	ha, err := s.payload.FindActiveHumanAnnotation(ctx, taskID)
	if err != nil {
		return err
	}
	if ha != nil {
		if err := s.payload.UpdateHumanAnnotationQAStatus(ctx, taskID, "submitted", nil, ""); err != nil {
			return err
		}
	} else {
		// Video tasks keep their work in mm_tracks (no HumanAnnotation) — allow
		// submit when active tracks exist.
		tracks, err := s.payload.ListActiveTracksByTask(ctx, taskID, "", "")
		if err != nil {
			return err
		}
		if len(tracks) == 0 {
			return ErrNoActiveHumanAnnotation
		}
	}
	rows, err := s.db.CASUpdateState(ctx, taskID, task.State, dbmodel.TaskStateQAPending, map[string]interface{}{
		"assignee_id": userID,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("submit raced; task state changed")
	}
	// Rework is in; last round's per-track verdicts are stale. Wipe them so the
	// next review starts from a clean slate rather than inheriting old ticks.
	// Best-effort: the task is already in QA_PENDING and must not bounce back.
	_ = s.payload.ClearTrackReviews(ctx, taskID)
	_ = s.captureRound(ctx, taskID, userID)
	return nil
}

// captureRound snapshots the submitted track list so a later rework can be
// diffed against it (B3.1). mm_tracks is updated in place, so this is the only
// record of what round N looked like. Best-effort: the state transition already
// committed and a missing round degrades the diff view, nothing else.
func (s *QAService) captureRound(ctx context.Context, taskID, userID uint) error {
	tracks, err := s.payload.ListActiveTracksByTask(ctx, taskID, "", "")
	if err != nil || len(tracks) == 0 {
		return err
	}
	prev, err := s.payload.MaxTrackRound(ctx, taskID)
	if err != nil {
		return err
	}
	return s.payload.InsertTrackRound(ctx, &paymodel.TrackRound{
		TaskID: taskID, Round: prev + 1, Tracks: tracks, TrackCount: len(tracks),
		SubmittedBy: userID, SubmittedAt: time.Now().UTC(),
	})
}

// ErrSelfReview is returned when a reviewer tries to review a task they
// submitted themselves (four-eyes rule, 执行方案-02 B3.1). Mapped to 403.
var ErrSelfReview = errors.New("四眼规则：不能审核自己提交的任务")

// ErrRejectedTracksRemain blocks passing a task that still carries a per-track
// rejection — the two verdicts contradict each other.
var ErrRejectedTracksRemain = errors.New("仍有被驳回的 track，无法整体通过；请先撤销该 track 的驳回或驳回整个任务")

// fourEyes enforces reviewer != submitter. Submit() records the submitter in
// assignee_id. Admins may bypass (single-operator installs would otherwise be
// unable to close any task).
func fourEyes(task *dbmodel.AnnotationTask, reviewerID uint, isAdmin bool) error {
	if isAdmin || task.AssigneeID == nil {
		return nil
	}
	if *task.AssigneeID == reviewerID {
		return ErrSelfReview
	}
	return nil
}

// Pass approves the QA-pending task, finalises it and writes the
// FinalAnnotation row. Caller is expected to have role image_reviewer / admin.
func (s *QAService) Pass(ctx context.Context, taskID, reviewerID uint, note string, isAdmin bool) error {
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task.State != dbmodel.TaskStateQAPending {
		return fmt.Errorf("task state %s is not awaiting QA", task.State)
	}
	if err := fourEyes(task, reviewerID, isAdmin); err != nil {
		return err
	}
	// A reviewer cannot pass a task while they still have a track marked
	// rejected — the two verdicts contradict each other.
	rejected, err := s.payload.CountRejectedActiveTracks(ctx, taskID)
	if err != nil {
		return err
	}
	if rejected > 0 {
		return fmt.Errorf("%w（%d 条）", ErrRejectedTracksRemain, rejected)
	}
	// 07·N4:QA 通过 =「标注状态 + 终稿 + 快照 + 任务状态迁移」一个事务。
	// 双库时代这四步非原子,靠幂等键 + 重放兜底;单库后要么全成要么全不成。
	return s.db.DB.Transaction(func(tx *gorm.DB) error {
		txRepo := s.db.WithTx(tx)
		if err := txRepo.UpdateHumanAnnotationQAStatus(ctx, taskID, "passed", &reviewerID, note); err != nil {
			return err
		}
		if s.final != nil {
			if _, err := s.final.FinalizeWith(ctx, txRepo, task); err != nil {
				return fmt.Errorf("finalize: %w", err)
			}
		}
		rows, err := txRepo.CASUpdateState(ctx, taskID, dbmodel.TaskStateQAPending, dbmodel.TaskStateFinalized, map[string]interface{}{
			"reviewer_id": reviewerID,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("qa pass raced; task state changed")
		}
		return nil
	})
}

// Reject sends the task back to HUMAN_IN_PROGRESS with a review note.
func (s *QAService) Reject(ctx context.Context, taskID, reviewerID uint, note string, isAdmin bool) error {
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if task.State != dbmodel.TaskStateQAPending {
		return fmt.Errorf("task state %s is not awaiting QA", task.State)
	}
	if err := fourEyes(task, reviewerID, isAdmin); err != nil {
		return err
	}
	// 同 Pass:驳回的两笔写进一个事务。
	var rows int64
	err = s.db.DB.Transaction(func(tx *gorm.DB) error {
		txRepo := s.db.WithTx(tx)
		if err := txRepo.UpdateHumanAnnotationQAStatus(ctx, taskID, "rejected", &reviewerID, note); err != nil {
			return err
		}
		var terr error
		rows, terr = txRepo.CASUpdateState(ctx, taskID, dbmodel.TaskStateQAPending, dbmodel.TaskStateQARejected, map[string]interface{}{
			"reviewer_id": reviewerID,
		})
		return terr
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("qa reject raced; task state changed")
	}
	return nil
}
