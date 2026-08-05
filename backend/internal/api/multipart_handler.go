package api

import (
	"errors"
	"net/http"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// MultipartHandler exposes the resumable chunked-upload control plane (T0.2):
// init → (browser PUTs parts to presigned URLs) → complete, plus status
// (resume) and abort. Bytes never pass through the app process.
type MultipartHandler struct {
	svc *service.MultipartUploadService
}

// NewMultipartHandler wires the service.
func NewMultipartHandler(svc *service.MultipartUploadService) *MultipartHandler {
	return &MultipartHandler{svc: svc}
}

// Init handles POST /uploads/init.
func (h *MultipartHandler) Init(c *gin.Context) {
	var req struct {
		DatasetID   uint   `json:"dataset_id" binding:"required"`
		Filename    string `json:"filename" binding:"required"`
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes" binding:"required"`
		ClientSHA   string `json:"client_sha256"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	uid := c.GetUint("user_id")
	if uid == 0 {
		Error(c, http.StatusUnauthorized, "unauthenticated")
		return
	}
	res, err := h.svc.Init(c.Request.Context(), uid, req.DatasetID, req.Filename, req.ContentType, req.SizeBytes, req.ClientSHA)
	if err != nil {
		if errors.Is(err, service.ErrMultipartUnsupported) {
			Error(c, http.StatusNotImplemented, err.Error())
			return
		}
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// Status handles GET /uploads/:session_id (resume).
func (h *MultipartHandler) Status(c *gin.Context) {
	uid := c.GetUint("user_id")
	res, err := h.svc.Status(c.Request.Context(), uid, c.Param("session_id"))
	if err != nil {
		Error(c, http.StatusNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// Complete handles POST /uploads/complete.
func (h *MultipartHandler) Complete(c *gin.Context) {
	var req struct {
		SessionID string                   `json:"session_id" binding:"required"`
		UploadID  string                   `json:"upload_id"`
		Parts     []service.MultipartPart  `json:"parts" binding:"required"`
		ClientSHA string                   `json:"client_sha256"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	uid := c.GetUint("user_id")
	res, err := h.svc.Complete(c.Request.Context(), uid, req.SessionID, req.UploadID, req.Parts, req.ClientSHA)
	if err != nil {
		if errors.Is(err, service.ErrHashMismatch) {
			Error(c, http.StatusBadRequest, "hash_mismatch")
			return
		}
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"asset":        res.Asset,
		"task":         res.Task,
		"deduplicated": res.Deduplicated,
	})
}

// Abort handles POST /uploads/abort.
func (h *MultipartHandler) Abort(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	uid := c.GetUint("user_id")
	if err := h.svc.Abort(c.Request.Context(), uid, req.SessionID); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
