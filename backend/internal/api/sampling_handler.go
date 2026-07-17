package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// SamplingHandler handles HTTP endpoints for sampling plan generation
// and sampling strategy listing.
type SamplingHandler struct {
	samplingService *service.SamplingService
}

// NewSamplingHandler creates a SamplingHandler with the given SamplingService.
func NewSamplingHandler(samplingService *service.SamplingService) *SamplingHandler {
	return &SamplingHandler{samplingService: samplingService}
}

// generateSamplingPlanRequest represents the JSON body for POST /datasets/:id/sampling.
type generateSamplingPlanRequest struct {
	Strategy string                 `json:"strategy"`
	Params   map[string]interface{} `json:"params"`
}

// GenerateSamplingPlan handles POST /datasets/:id/sampling.
// Parses dataset ID from URL, binds JSON {strategy, params}, retrieves user_id
// from UserContext. If strategy is empty or "full", calls GenerateFullPlan;
// otherwise calls GeneratePlan with the specified strategy and params.
// Returns JSON {segments: [...], total: int}.
func (h *SamplingHandler) GenerateSamplingPlan(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid dataset id")
		return
	}

	var req generateSamplingPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	var userID uint = 1
	if uc := middleware.GetUserContext(c); uc != nil {
		userID = uc.UserID
	}

	var segments []plugin.SegmentUnit
	if req.Strategy == "" || req.Strategy == "full" {
		result, err := h.samplingService.GenerateFullPlan(c.Request.Context(), uint(id), userID)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		segments = result
	} else {
		result, err := h.samplingService.GeneratePlan(c.Request.Context(), uint(id), req.Strategy, req.Params, userID)
		if err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		segments = result
	}

	c.JSON(http.StatusOK, gin.H{
		"segments": segments,
		"total":    len(segments),
	})
}

// ListStrategies handles GET /sampling/strategies.
// Returns all registered sampling strategies as a JSON array.
func (h *SamplingHandler) ListStrategies(c *gin.Context) {
	strategies := h.samplingService.ListStrategies()
	c.JSON(http.StatusOK, strategies)
}
