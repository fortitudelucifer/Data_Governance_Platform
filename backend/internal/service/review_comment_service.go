package service

import (
	"context"
	"errors"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/repository"
)

// Review comment errors surfaced to the API layer.
var (
	ErrEmptyCommentBody = errors.New("批注内容不能为空")
	ErrNotCommentAuthor = errors.New("只能删除自己发表的批注")
	// ErrOpenCommentsRemain blocks re-submitting a rejected task while the
	// reviewer's anchored comments are still unresolved.
	ErrOpenCommentsRemain = errors.New("仍有未处理的审核批注，请逐条修复并标记「已修复」后再提交")
)

const maxCommentBodyLen = 2000

// ReviewCommentService manages reviewer comments anchored to a frame/track, the
// mechanism that turns a rejection into a to-do list the annotator can click
// through rather than a paragraph they have to decode (执行方案-02 B3.1).
type ReviewCommentService struct {
	db *repository.DB
	payload *repository.DB
}

// NewReviewCommentService wires the dependencies.
func NewReviewCommentService(db *repository.DB, payload *repository.DB) *ReviewCommentService {
	return &ReviewCommentService{db: db, payload: payload}
}

// CommentAnchor points at the place in the asset the comment is about. All
// fields optional: no anchor = a whole-task remark.
type CommentAnchor struct {
	Frame   *int     `json:"frame,omitempty"`
	TrackID *int     `json:"track_id,omitempty"`
	TimeMs  *float64 `json:"time_ms,omitempty"`
}

// normalizeCommentBody trims and caps the body. Truncation is by rune, not byte:
// the comments are Chinese, and a byte-slice would split a codepoint and emit
// invalid UTF-8.
func normalizeCommentBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ErrEmptyCommentBody
	}
	if r := []rune(body); len(r) > maxCommentBodyLen {
		body = string(r[:maxCommentBodyLen])
	}
	return body, nil
}

// canDeleteComment: only the author (or an admin) may retract a comment. An
// annotator must not be able to make a reviewer's objection disappear.
func canDeleteComment(authorID, userID uint, isAdmin bool) bool {
	return isAdmin || authorID == userID
}

// Add files a comment on a task. The task's dataset/asset and the author's name
// are denormalised onto the comment so the workbench can render it without a
// second lookup — and so it survives the author being renamed or removed.
func (s *ReviewCommentService) Add(ctx context.Context, taskID, authorID uint, anchor CommentAnchor, body string) (*paymodel.ReviewComment, error) {
	body, err := normalizeCommentBody(body)
	if err != nil {
		return nil, err
	}
	task, err := s.db.FindAnnotationTaskByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	authorName := ""
	if u, uerr := s.db.FindUserByID(ctx, authorID); uerr == nil && u != nil {
		authorName = u.Username
	}
	c := &paymodel.ReviewComment{
		TaskID:     task.ID,
		DatasetID:  task.DatasetID,
		AssetID:    task.AssetID,
		Frame:      anchor.Frame,
		TrackID:    anchor.TrackID,
		TimeMs:     anchor.TimeMs,
		Body:       body,
		Status:     paymodel.ReviewCommentOpen,
		AuthorID:   authorID,
		AuthorName: authorName,
	}
	if _, err := s.payload.InsertReviewComment(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// List returns every comment on a task, oldest first.
func (s *ReviewCommentService) List(ctx context.Context, taskID uint) ([]paymodel.ReviewComment, error) {
	return s.payload.ListReviewCommentsByTask(ctx, taskID)
}

// SetResolved marks a comment fixed (or reopens it). This is the annotator's
// action: the reviewer files a comment, the annotator closes it.
func (s *ReviewCommentService) SetResolved(ctx context.Context, commentID string, userID uint, resolved bool) error {
	status := paymodel.ReviewCommentOpen
	if resolved {
		status = paymodel.ReviewCommentResolved
	}
	return s.payload.SetReviewCommentStatus(ctx, commentID, status, userID)
}

// Delete removes a comment, if canDeleteComment allows it.
func (s *ReviewCommentService) Delete(ctx context.Context, commentID string, userID uint, isAdmin bool) error {
	c, err := s.payload.FindReviewComment(ctx, commentID)
	if err != nil {
		return err
	}
	if !canDeleteComment(c.AuthorID, userID, isAdmin) {
		return ErrNotCommentAuthor
	}
	return s.payload.DeleteReviewComment(ctx, commentID)
}

// CountOpen reports how many comments still await the annotator.
func (s *ReviewCommentService) CountOpen(ctx context.Context, taskID uint) (int64, error) {
	return s.payload.CountOpenReviewComments(ctx, taskID)
}
