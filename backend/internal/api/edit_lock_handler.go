package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// EditLockHandler exposes the distributed task edit-lock (plan_v2 执行方案-00 T0.4).
// The workspace acquires on enter, refreshes on a heartbeat, and releases on
// leave. Lock ownership is the JWT user id (set by the auth middleware).
type EditLockHandler struct {
	lock *service.EditLockService
}

// NewEditLockHandler wires the dependency.
func NewEditLockHandler(lock *service.EditLockService) *EditLockHandler {
	return &EditLockHandler{lock: lock}
}

func (h *EditLockHandler) taskAndOwner(c *gin.Context) (uint, string, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid task id")
		return 0, "", false
	}
	uid := c.GetUint("user_id")
	if uid == 0 {
		Error(c, http.StatusUnauthorized, "unauthenticated")
		return 0, "", false
	}
	return uint(id), strconv.FormatUint(uint64(uid), 10), true
}

// Acquire handles POST /tasks/:id/lock.
func (h *EditLockHandler) Acquire(c *gin.Context) {
	taskID, owner, ok := h.taskAndOwner(c)
	if !ok {
		return
	}
	res, err := h.lock.Acquire(c.Request.Context(), taskID, owner)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	// 409 makes it trivial for the client to switch to read-only mode.
	status := http.StatusOK
	if !res.Acquired {
		status = http.StatusConflict
	}
	c.JSON(status, res)
}

// Refresh handles POST /tasks/:id/lock/refresh (watchdog heartbeat).
func (h *EditLockHandler) Refresh(c *gin.Context) {
	taskID, owner, ok := h.taskAndOwner(c)
	if !ok {
		return
	}
	held, err := h.lock.Refresh(c.Request.Context(), taskID, owner)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !held {
		Error(c, http.StatusConflict, "lock lost")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Release handles DELETE /tasks/:id/lock.
func (h *EditLockHandler) Release(c *gin.Context) {
	taskID, owner, ok := h.taskAndOwner(c)
	if !ok {
		return
	}
	if _, err := h.lock.Release(c.Request.Context(), taskID, owner); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
