package api

import (
	"errors"
	"net/http"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AnnotationHandler bundles human annotation, QA review, final retrieval and
// interactive segmentation endpoints — the per-task workbench surface.
type AnnotationHandler struct {
	task       *service.AnnotationTaskService
	asset      *service.AssetService
	human      *service.HumanAnnotationService
	qa         *service.QAService
	final      *service.FinalAnnotationService
	capability *service.CapabilityService
}

// NewAnnotationHandler wires the dependencies.
func NewAnnotationHandler(task *service.AnnotationTaskService, asset *service.AssetService,
	human *service.HumanAnnotationService, qa *service.QAService,
	final *service.FinalAnnotationService, capability *service.CapabilityService) *AnnotationHandler {
	return &AnnotationHandler{
		task: task, asset: asset, human: human, qa: qa, final: final, capability: capability,
	}
}

// GetHumanAnnotation handles GET /tasks/:id/human-annotation.
func (h *AnnotationHandler) GetHumanAnnotation(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	ha, err := h.human.GetActive(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, ha)
}

// PutHumanAnnotation handles PUT /tasks/:id/human-annotation.
func (h *AnnotationHandler) PutHumanAnnotation(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var draft service.HumanAnnotationDraft
	if err := c.BindJSON(&draft); err != nil {
		Error(c, http.StatusBadRequest, "invalid body")
		return
	}
	uc := middleware.GetUserContext(c)
	uid := uint(0)
	if uc != nil {
		uid = uc.UserID
	}
	ha, err := h.human.Save(c.Request.Context(), taskID, uid, draft)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, ha)
}

// SubmitTask handles POST /tasks/:id/submit.
func (h *AnnotationHandler) SubmitTask(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	uc := middleware.GetUserContext(c)
	uid := uint(0)
	if uc != nil {
		uid = uc.UserID
	}
	if err := h.qa.Submit(c.Request.Context(), taskID, uid); err != nil {
		if errors.Is(err, service.ErrNoActiveHumanAnnotation) {
			Error(c, http.StatusBadRequest, "save a draft before submitting")
			return
		}
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// QAPass handles POST /tasks/:id/qa/pass.
func (h *AnnotationHandler) QAPass(c *gin.Context) {
	h.handleQA(c, true)
}

// QAReject handles POST /tasks/:id/qa/reject.
func (h *AnnotationHandler) QAReject(c *gin.Context) {
	h.handleQA(c, false)
}

func (h *AnnotationHandler) handleQA(c *gin.Context, pass bool) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	uc := middleware.GetUserContext(c)
	uid := uint(0)
	isAdmin := false
	if uc != nil {
		uid = uc.UserID
		isAdmin = uc.Role == "admin"
	}
	var body struct {
		Note string `json:"note"`
	}
	_ = c.BindJSON(&body)
	var err2 error
	if pass {
		err2 = h.qa.Pass(c.Request.Context(), taskID, uid, body.Note, isAdmin)
	} else {
		err2 = h.qa.Reject(c.Request.Context(), taskID, uid, body.Note, isAdmin)
	}
	if err2 != nil {
		if errors.Is(err2, service.ErrSelfReview) { // 四眼规则
			Error(c, http.StatusForbidden, err2.Error())
			return
		}
		Error(c, http.StatusBadRequest, err2.Error()) // 含 ErrRejectedTracksRemain
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GetFinal handles GET /tasks/:id/final.
func (h *AnnotationHandler) GetFinal(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	fa, err := h.final.GetLatest(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, fa)
}

// SegmentInteractive handles POST /tasks/:id/segment.
// Runs MobileSAM on the task's asset with the provided point prompts and
// returns the best polygon + score + mask PNG for workbench preview.
// Does NOT persist an AI run or change task state.
func (h *AnnotationHandler) SegmentInteractive(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Points   [][]float64 `json:"points"`
		Box      []float64   `json:"box,omitempty"`
		ImageB64 string      `json:"image_b64,omitempty"` // video: current-frame pixels to segment
	}
	if err := c.BindJSON(&body); err != nil || len(body.Points) == 0 {
		Error(c, http.StatusBadRequest, "points required [[x,y,label],...]")
		return
	}
	if h.capability == nil || !h.capability.Has(service.CapabilitySegInteractive) {
		Error(c, http.StatusServiceUnavailable, "seg.interactive not configured (sam-server running?)")
		return
	}

	task, err := h.task.Get(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusNotFound, "task not found")
		return
	}
	asset, err := h.asset.GetAsset(c.Request.Context(), task.AssetID)
	if err != nil {
		Error(c, http.StatusInternalServerError, "load asset: " + err.Error())
		return
	}

	extras := map[string]interface{}{"points": body.Points}
	if len(body.Box) == 4 {
		extras["box"] = body.Box
	}
	if body.ImageB64 != "" {
		extras["image_b64"] = body.ImageB64
	}

	capResp, err := h.capability.Invoke(c.Request.Context(), service.CapabilityRequest{
		TaskID:         task.ID,
		AssetID:        task.AssetID,
		TraceID:        task.TraceID,
		CapabilityType: service.CapabilitySegInteractive,
		AssetURI:       asset.StorageURI,
		MIME:           asset.MIME,
		Width:          asset.Width,
		Height:         asset.Height,
		Extras:         extras,
	})
	if err != nil || capResp.Status != "success" {
		msg := capResp.Error
		if msg == "" && err != nil {
			msg = err.Error()
		}
		Error(c, http.StatusInternalServerError, msg)
		return
	}
	c.JSON(http.StatusOK, capResp.Raw)
}
