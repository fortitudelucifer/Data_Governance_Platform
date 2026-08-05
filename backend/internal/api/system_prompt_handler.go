package api

import (
	"net/http"
	"strconv"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// SystemPromptHandler handles HTTP endpoints for system prompt management.
type SystemPromptHandler struct {
	promptService *service.SystemPromptService
}

// NewSystemPromptHandler creates a SystemPromptHandler.
func NewSystemPromptHandler(promptService *service.SystemPromptService) *SystemPromptHandler {
	return &SystemPromptHandler{promptService: promptService}
}

// ListPrompts handles GET /system_prompts.
func (h *SystemPromptHandler) ListPrompts(c *gin.Context) {
	prompts, err := h.promptService.List(c.Request.Context())
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, prompts)
}

// GetPrompt handles GET /system_prompts/:case_type.
func (h *SystemPromptHandler) GetPrompt(c *gin.Context) {
	caseType := c.Param("case_type")
	prompt, err := h.promptService.GetByCaseType(c.Request.Context(), caseType)
	if err != nil {
		Error(c, http.StatusNotFound, err.Error())
		return
	}
	c.JSON(http.StatusOK, prompt)
}

type updatePromptRequest struct {
	Content string `json:"content"`
}

// UpdatePrompt handles PUT /system_prompts/:case_type.
func (h *SystemPromptHandler) UpdatePrompt(c *gin.Context) {
	caseType := c.Param("case_type")
	var req updatePromptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.promptService.Update(c.Request.Context(), caseType, req.Content); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	prompt, _ := h.promptService.GetByCaseType(c.Request.Context(), caseType)
	c.JSON(http.StatusOK, prompt)
}

type createPromptRequest struct {
	CaseType string `json:"case_type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
}

// CreatePrompt handles POST /system_prompts.
func (h *SystemPromptHandler) CreatePrompt(c *gin.Context) {
	var req createPromptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CaseType == "" {
		Error(c, http.StatusBadRequest, "case_type is required")
		return
	}
	prompt, err := h.promptService.Create(c.Request.Context(), req.CaseType, req.Name, req.Content)
	if err != nil {
		Error(c, http.StatusConflict, err.Error())
		return
	}
	c.JSON(http.StatusCreated, prompt)
}

type autoPromptTemplateRequest struct {
	Name               string `json:"name"`
	CaseType           string `json:"case_type"`
	TaskType           string `json:"task_type"`
	SystemPrompt       string `json:"system_prompt"`
	UserPromptTemplate string `json:"user_prompt_template"`
	OutputSchema       string `json:"output_schema"`
	Guide              string `json:"guide"`
	Enabled            *bool  `json:"enabled"`
}

// ListAutoPromptTemplates handles GET /auto_prompt_templates.
func (h *SystemPromptHandler) ListAutoPromptTemplates(c *gin.Context) {
	templates, err := h.promptService.ListAutoPromptTemplates(c.Request.Context(), c.Query("case_type"), true)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, templates)
}

// CreateAutoPromptTemplate handles POST /auto_prompt_templates.
func (h *SystemPromptHandler) CreateAutoPromptTemplate(c *gin.Context) {
	var req autoPromptTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tpl, err := h.promptService.CreateAutoPromptTemplate(c.Request.Context(), dbmodel.AutoPromptTemplate{
		Name:               req.Name,
		CaseType:           req.CaseType,
		TaskType:           req.TaskType,
		SystemPrompt:       req.SystemPrompt,
		UserPromptTemplate: req.UserPromptTemplate,
		OutputSchema:       req.OutputSchema,
		Guide:              req.Guide,
		Enabled:            enabled,
		CreatedBy:          currentUserID(c),
	})
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusCreated, tpl)
}

// UpdateAutoPromptTemplate handles PUT /auto_prompt_templates/:id.
func (h *SystemPromptHandler) UpdateAutoPromptTemplate(c *gin.Context) {
	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		Error(c, http.StatusBadRequest, "invalid template id")
		return
	}
	var req autoPromptTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tpl, err := h.promptService.UpdateAutoPromptTemplate(c.Request.Context(), uint(id64), dbmodel.AutoPromptTemplate{
		Name:               req.Name,
		CaseType:           req.CaseType,
		TaskType:           req.TaskType,
		SystemPrompt:       req.SystemPrompt,
		UserPromptTemplate: req.UserPromptTemplate,
		OutputSchema:       req.OutputSchema,
		Guide:              req.Guide,
		Enabled:            enabled,
	})
	if err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, tpl)
}
