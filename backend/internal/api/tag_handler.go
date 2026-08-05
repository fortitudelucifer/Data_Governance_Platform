package api

import (
	"net/http"
	"strconv"
	"strings"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// TagHandler handles HTTP endpoints for tag CRUD operations.
type TagHandler struct {
	datasetService *service.DatasetService
}

// NewTagHandler creates a TagHandler with the given DatasetService.
func NewTagHandler(datasetService *service.DatasetService) *TagHandler {
	return &TagHandler{datasetService: datasetService}
}

// createTagRequest represents the JSON body for creating/updating a tag.
type createTagRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Type  string `json:"type"`
}

// ListTags handles GET /tags.
// Returns all tags as a JSON array.
func (h *TagHandler) ListTags(c *gin.Context) {
	tagType := c.DefaultQuery("type", "dataset")
	tags, err := h.datasetService.ListTags(c.Request.Context(), tagType)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, tags)
}

// CreateTag handles POST /tags.
// Binds JSON {name, color}, validates name is non-empty, creates and returns 201.
// Returns 409 Conflict if the tag name already exists.
func (h *TagHandler) CreateTag(c *gin.Context) {
	var req createTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		Error(c, http.StatusBadRequest, "name is required")
		return
	}

	tag := &dbmodel.Tag{
		Name:  req.Name,
		Color: req.Color,
		Type:  req.Type,
	}

	if err := h.datasetService.CreateTag(c.Request.Context(), tag); err != nil {
		if strings.Contains(err.Error(), "tag name already exists") {
			Error(c, http.StatusConflict, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":    tag.ID,
		"name":  tag.Name,
		"color": tag.Color,
	})
}

// UpdateTag handles PUT /tags/:id.
// Parses id from URL, binds JSON {name, color}, and updates the tag.
func (h *TagHandler) UpdateTag(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid tag id")
		return
	}

	var req createTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Color != "" {
		updates["color"] = req.Color
	}
	if req.Type != "" {
		updates["type"] = req.Type
	}

	if err := h.datasetService.UpdateTag(c.Request.Context(), uint(id), updates); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":    id,
		"name":  req.Name,
		"color": req.Color,
		"type":  req.Type,
	})
}

// DeleteTag handles DELETE /tags/:id.
// Parses id from URL and deletes the tag.
func (h *TagHandler) DeleteTag(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid tag id")
		return
	}

	if err := h.datasetService.DeleteTag(c.Request.Context(), uint(id)); err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}
