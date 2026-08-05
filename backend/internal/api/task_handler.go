package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// TaskHandler bundles task-lifecycle endpoints: creation, listing, assignment,
// reprocessing, and prev/next navigation.
type TaskHandler struct {
	asset *service.AssetService
	task  *service.AnnotationTaskService
}

// NewTaskHandler wires the dependencies.
func NewTaskHandler(asset *service.AssetService, task *service.AnnotationTaskService) *TaskHandler {
	return &TaskHandler{asset: asset, task: task}
}

// CreateTask handles POST /assets/:id/tasks. Currently the body is empty; in
// future P1 we may accept job_id / deadline overrides.
func (h *TaskHandler) CreateTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid asset id")
		return
	}
	asset, err := h.asset.GetAsset(c.Request.Context(), uint(id))
	if err != nil {
		Error(c, http.StatusNotFound, "asset not found")
		return
	}
	task, err := h.task.CreateForAsset(c.Request.Context(), asset, service.CreateTaskOptions{})
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, task)
}

// GetTask handles GET /tasks/:id.
func (h *TaskHandler) GetTask(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	task, err := h.task.Get(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusNotFound, "task not found")
		return
	}
	c.JSON(http.StatusOK, task)
}

// ListTasks handles GET /tasks?dataset_id=...&state=...&mine=true
// mine=true filters tasks to those assigned to the currently authenticated user.
func (h *TaskHandler) ListTasks(c *gin.Context) {
	page, pageSize := ParsePageParams(c)
	filter := repository.AnnotationTaskFilter{}
	if v := c.Query("dataset_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			ds := uint(id)
			filter.DatasetID = &ds
		}
	}
	if v := c.Query("state"); v != "" {
		filter.State = &v
	}
	if v := c.Query("asset_ids"); v != "" {
		for _, part := range strings.Split(v, ",") {
			if id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64); err == nil {
				filter.AssetIDs = append(filter.AssetIDs, uint(id))
			}
		}
	} else if v := c.Query("asset_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			a := uint(id)
			filter.AssetID = &a
		}
	}
	if v := c.Query("assignee_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			uid := uint(id)
			filter.AssigneeID = &uid
		}
	} else if c.Query("mine") == "true" {
		uid := c.GetUint("user_id")
		if uid > 0 {
			filter.MineUserID = &uid
		}
	}
	items, total, err := h.task.List(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	RespondPage(c, items, total, page, pageSize)
}

// AssignTask handles PUT /tasks/:id/assign.
// Body: { "assignee_id": 3, "reviewer_id": 5, "deadline_at": "2026-06-01T00:00:00Z" }
// Any field is optional. Pass 0 for uint fields to clear; pass "" for deadline to clear.
// Restricted to admin role.
func (h *TaskHandler) AssignTask(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		AssigneeID *uint   `json:"assignee_id"`
		ReviewerID *uint   `json:"reviewer_id"`
		DeadlineAt *string `json:"deadline_at"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		Error(c, http.StatusBadRequest, "invalid body: " + err.Error())
		return
	}
	if body.AssigneeID == nil && body.ReviewerID == nil && body.DeadlineAt == nil {
		Error(c, http.StatusBadRequest, "at least one field required")
		return
	}
	deadline, err := parseDeadline(body.DeadlineAt)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid deadline_at: " + err.Error())
		return
	}
	if err := h.task.Assign(c.Request.Context(), taskID, body.AssigneeID, body.ReviewerID, deadline); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ := h.task.Get(c.Request.Context(), taskID)
	c.JSON(http.StatusOK, task)
}

// BatchAssignTasks handles POST /tasks/batch-assign.
// Body: { "task_ids": [1,2,3], "assignee_id": 5, "reviewer_id": 7, "deadline_at": "2026-06-01T00:00:00Z" }
// Restricted to admin role.
func (h *TaskHandler) BatchAssignTasks(c *gin.Context) {
	var body struct {
		TaskIDs    []uint  `json:"task_ids"`
		AssigneeID *uint   `json:"assignee_id"`
		ReviewerID *uint   `json:"reviewer_id"`
		DeadlineAt *string `json:"deadline_at"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		Error(c, http.StatusBadRequest, "invalid body: " + err.Error())
		return
	}
	if len(body.TaskIDs) == 0 {
		Error(c, http.StatusBadRequest, "task_ids must not be empty")
		return
	}
	if body.AssigneeID == nil && body.ReviewerID == nil && body.DeadlineAt == nil {
		Error(c, http.StatusBadRequest, "at least one field required")
		return
	}
	deadline, err := parseDeadline(body.DeadlineAt)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid deadline_at: " + err.Error())
		return
	}
	updated, err := h.task.BatchAssign(c.Request.Context(), body.TaskIDs, body.AssigneeID, body.ReviewerID, deadline)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

// Reprocess handles POST /tasks/:id/reprocess.
func (h *TaskHandler) Reprocess(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.task.Reprocess(c.Request.Context(), taskID); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GetAdjacentTasks handles GET /tasks/:id/adjacent?mine=true.
// Returns {prev_task_id, next_task_id} (either may be null) for navigating
// the workbench queue without returning to the asset list.
func (h *TaskHandler) GetAdjacentTasks(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var mineUserID *uint
	if c.Query("mine") == "true" {
		uid := c.GetUint("user_id")
		if uid > 0 {
			mineUserID = &uid
		}
	}
	prev, next, err := h.task.AdjacentTaskIDs(c.Request.Context(), taskID, mineUserID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"prev_task_id": prev, "next_task_id": next})
}

// parseDeadline converts a *string from the JSON body to *time.Time.
// nil input → nil output (no change). "" → &zero (clear). RFC3339 string → &parsed.
func parseDeadline(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	if *s == "" {
		zero := time.Time{}
		return &zero, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// taskIDParam extracts the :id URL parameter as a non-zero uint.
// Shared by all multi-modal handlers that operate on a single task.
func taskIDParam(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid task id")
	}
	return uint(id), nil
}
