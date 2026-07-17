package api

import (
	"errors"
	"net/http"

	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// AIResultHandler bundles read-only AI artefact endpoints (routing, runs,
// results, trace) plus the capability discovery & ad-hoc invoke endpoints.
type AIResultHandler struct {
	payload      *repository.DB
	trace      *service.TraceService
	capability *service.CapabilityService
	adhoc      *service.AdHocInvocationService
}

// NewAIResultHandler wires the dependencies.
func NewAIResultHandler(payload *repository.DB, trace *service.TraceService,
	capability *service.CapabilityService, adhoc *service.AdHocInvocationService) *AIResultHandler {
	return &AIResultHandler{payload: payload, trace: trace, capability: capability, adhoc: adhoc}
}

// GetRouting handles GET /tasks/:id/routing.
func (h *AIResultHandler) GetRouting(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	rr, err := h.payload.FindLatestRoutingResult(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, rr)
}

// GetAIRuns handles GET /tasks/:id/ai-runs.
func (h *AIResultHandler) GetAIRuns(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	runs, err := h.payload.FindAIRunsByTask(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, runs)
}

// GetAIResults handles GET /tasks/:id/ai-results.
func (h *AIResultHandler) GetAIResults(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	out := gin.H{}
	if ocr, err := h.payload.FindLatestOCRResult(c.Request.Context(), taskID); err == nil {
		out["ocr"] = ocr
	}
	if vlm, err := h.payload.FindLatestVLMResult(c.Request.Context(), taskID); err == nil {
		out["vlm"] = vlm
	}
	if seg, err := h.payload.FindLatestSegResult(c.Request.Context(), taskID); err == nil {
		out["seg"] = seg
	}
	if asr, err := h.payload.FindLatestASRResult(c.Request.Context(), taskID); err == nil {
		out["asr"] = asr
	}
	c.JSON(http.StatusOK, out)
}

// GetTrace handles GET /tasks/:id/trace.
func (h *AIResultHandler) GetTrace(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	t, err := h.trace.GetByTask(c.Request.Context(), taskID)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, t)
}

// ListCapabilities handles GET /capabilities. Returns the capability_types
// currently wired in the running backend.
func (h *AIResultHandler) ListCapabilities(c *gin.Context) {
	if h.capability == nil {
		c.JSON(http.StatusOK, gin.H{"capabilities": []string{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"capabilities": h.capability.ListCapabilities()})
}

// GetGPUQueue handles GET /capabilities/gpu-queue — the backlog of every
// GPU-gated capability, keyed by capability name (B2.8 成本闸门). The workbench
// polls this to show "队列 2/4" and grey out the trigger before the user clicks
// into a 429.
func (h *AIResultHandler) GetGPUQueue(c *gin.Context) {
	if h.capability == nil {
		c.JSON(http.StatusOK, gin.H{"queues": map[string]service.GPUQueueStats{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"queues": h.capability.QueueStats()})
}

// InvokeCapabilityOnTask handles POST /tasks/:id/invoke?capability=<cap>.
// Runs a capability on the task's asset on demand, writes a new mm_ai_run +
// result, but does NOT change task state or route_strategy.
func (h *AIResultHandler) InvokeCapabilityOnTask(c *gin.Context) {
	taskID, err := taskIDParam(c)
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	capability := c.Query("capability")
	model := c.Query("model")
	if capability == "" || model == "" {
		var body struct {
			Capability string `json:"capability"`
			Model      string `json:"model"`
		}
		_ = c.ShouldBindJSON(&body)
		if capability == "" {
			capability = body.Capability
		}
		if model == "" {
			model = body.Model
		}
	}
	if capability == "" {
		Error(c, http.StatusBadRequest, "capability is required (?capability=<type> or {\"capability\":\"...\"})")
		return
	}
	if h.adhoc == nil {
		Error(c, http.StatusServiceUnavailable, "ad-hoc invocation service not configured")
		return
	}
	result, err := h.adhoc.InvokeForTask(c.Request.Context(), taskID, capability, model)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCapabilityNotRegistered):
			Error(c, http.StatusBadRequest, err.Error())
		case errors.Is(err, service.ErrTaskNotInvocable):
			Error(c, http.StatusConflict, err.Error())
		default:
			Error(c, http.StatusInternalServerError, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, result)
}
