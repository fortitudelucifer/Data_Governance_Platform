package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"text-annotation-platform/internal/service"
)

// DraftHandler serves the server-side annotation draft (C0.6b).
//
//	PUT    /tasks/:id/draft   — 客户端每 MM_DRAFT_FLUSH_INTERVAL 推一次
//	GET    /tasks/:id/draft   — 换机器续作时取回
//	DELETE /tasks/:id/draft   — 保存成功后清掉
//
// ⚠️ 草稿**不是标注**。它不进导出、不进 QA、永远不是真源——导出只读
// track_snapshots。这层解决的只是"换台机器接着画，最多丢一个 flush 间隔"。
// 本地 IndexedDB 草稿（C0.6a）才是崩溃兜底，两者互不替代。
type DraftHandler struct {
	drafts        *service.DraftService
	tasks         *service.AnnotationTaskService
	flushInterval time.Duration
}

func NewDraftHandler(d *service.DraftService, t *service.AnnotationTaskService, flush time.Duration) *DraftHandler {
	if flush <= 0 {
		flush = 5 * time.Minute
	}
	return &DraftHandler{drafts: d, tasks: t, flushInterval: flush}
}

type draftBody struct {
	Baseline  json.RawMessage `json:"baseline"`
	Ops       json.RawMessage `json:"ops"`
	OpCount   int             `json:"op_count"`
	ClientRev int             `json:"client_rev"`
}

// PutDraft handles PUT /tasks/:id/draft.
func (h *DraftHandler) PutDraft(c *gin.Context) {
	taskID, uid, ok := h.scope(c)
	if !ok {
		return
	}
	var body draftBody
	if err := c.ShouldBindJSON(&body); err != nil {
		Error(c, http.StatusBadRequest, "草稿格式不合法："+err.Error())
		return
	}
	if len(body.Ops) == 0 {
		Error(c, http.StatusBadRequest, "草稿为空，无需保存")
		return
	}
	err := h.drafts.Put(c.Request.Context(), service.DraftEnvelope{
		TaskID: taskID, UserID: uid,
		Baseline: body.Baseline, Ops: body.Ops,
		OpCount: body.OpCount, ClientRev: body.ClientRev,
	})
	var tooBig *service.DraftTooLargeError
	switch {
	case errors.As(err, &tooBig):
		// 413 而不是静默截断：截断的操作日志会回放成一个**错误的**掩膜。
		Error(c, http.StatusRequestEntityTooLarge, tooBig.Error())
	case err != nil:
		Error(c, http.StatusInternalServerError, err.Error())
	default:
		c.JSON(http.StatusOK, gin.H{"saved": true, "op_count": body.OpCount})
	}
}

// GetDraft handles GET /tasks/:id/draft.
//
// 恒返回 200，草稿不存在时 `draft` 为 null——"这个任务没有草稿"是完全正常的
// 状态，用错误码表达会让前端把它当异常处理。顺带下发 `flush_interval_sec`：
// 前端打开任务本来就要调这个接口，间隔属于部署配置
// （MM_DRAFT_FLUSH_INTERVAL，默认 5 分钟），不该写死在前端。
func (h *DraftHandler) GetDraft(c *gin.Context) {
	taskID, uid, ok := h.scope(c)
	if !ok {
		return
	}
	env, err := h.drafts.Get(c.Request.Context(), taskID, uid)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"draft":              env, // nil = 没有草稿
		"flush_interval_sec": int(h.flushInterval.Seconds()),
	})
}

// DeleteDraft handles DELETE /tasks/:id/draft.
func (h *DraftHandler) DeleteDraft(c *gin.Context) {
	taskID, uid, ok := h.scope(c)
	if !ok {
		return
	}
	if err := h.drafts.Delete(c.Request.Context(), taskID, uid); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

// scope resolves task id + the *acting user*.
//
// 用户 id 取自 JWT，**绝不从请求体里读**：草稿按 (task,user) 隔离，
// 让调用方自报 user 等于让任何人读写别人的草稿。
func (h *DraftHandler) scope(c *gin.Context) (uint, uint, bool) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return 0, 0, false
	}
	uid := c.GetUint("user_id")
	if uid == 0 {
		Error(c, http.StatusUnauthorized, "未识别的用户")
		return 0, 0, false
	}
	if _, err := h.tasks.Get(c.Request.Context(), taskID); err != nil {
		Error(c, http.StatusNotFound, "task not found")
		return 0, 0, false
	}
	return taskID, uid, true
}
