package api

import (
	"errors"
	"net/http"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ReviewCommentHandler serves reviewer comments anchored to a frame/track, so a
// rejected task hands the annotator a clickable to-do list instead of a
// paragraph. Reviewers file them; annotators resolve them.
type ReviewCommentHandler struct {
	comments *service.ReviewCommentService
}

// NewReviewCommentHandler wires the dependencies.
func NewReviewCommentHandler(comments *service.ReviewCommentService) *ReviewCommentHandler {
	return &ReviewCommentHandler{comments: comments}
}

// ListComments handles GET /tasks/:id/review-comments.
func (h *ReviewCommentHandler) ListComments(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	list, err := h.comments.List(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": list})
}

// CreateComment handles POST /tasks/:id/review-comments (reviewer/admin).
func (h *ReviewCommentHandler) CreateComment(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		service.CommentAnchor
		Body string `json:"body"`
	}
	if err := c.BindJSON(&body); err != nil {
		Error(c, http.StatusBadRequest, "invalid body")
		return
	}
	uc := middleware.GetUserContext(c)
	uid := uint(0)
	if uc != nil {
		uid = uc.UserID
	}
	cm, err := h.comments.Add(c.Request.Context(), taskID, uid, body.CommentAnchor, body.Body)
	if err != nil {
		if errors.Is(err, service.ErrEmptyCommentBody) {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, cm)
}

// ResolveComment handles PATCH /tasks/:id/review-comments/:cid — the annotator
// marks a comment fixed (or reopens it).
func (h *ReviewCommentHandler) ResolveComment(c *gin.Context) {
	if _, err := taskIDParam(c); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Resolved bool `json:"resolved"`
	}
	if err := c.BindJSON(&body); err != nil {
		Error(c, http.StatusBadRequest, "invalid body")
		return
	}
	uc := middleware.GetUserContext(c)
	uid := uint(0)
	if uc != nil {
		uid = uc.UserID
	}
	if err := h.comments.SetResolved(c.Request.Context(), c.Param("cid"), uid, body.Resolved); err != nil {
		if errors.Is(err, repository.ErrReviewCommentNotFound) {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeleteComment handles DELETE /tasks/:id/review-comments/:cid (author or admin).
func (h *ReviewCommentHandler) DeleteComment(c *gin.Context) {
	if _, err := taskIDParam(c); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	uc := middleware.GetUserContext(c)
	uid, isAdmin := uint(0), false
	if uc != nil {
		uid, isAdmin = uc.UserID, uc.Role == "admin"
	}
	if err := h.comments.Delete(c.Request.Context(), c.Param("cid"), uid, isAdmin); err != nil {
		switch {
		case errors.Is(err, repository.ErrReviewCommentNotFound):
			Error(c, http.StatusNotFound, err.Error())
		case errors.Is(err, service.ErrNotCommentAuthor):
			Error(c, http.StatusForbidden, err.Error())
		default:
			Error(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
