package api

import (
	"net/http"
	"strconv"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// UserHandler handles user management HTTP endpoints
type UserHandler struct {
	userService *service.UserService
	authService *service.AuthService // PH-5：改状态/角色后失效鉴权缓存，撤权即时生效
}

// NewUserHandler creates a UserHandler with the given UserService + AuthService.
func NewUserHandler(userService *service.UserService, authService *service.AuthService) *UserHandler {
	return &UserHandler{userService: userService, authService: authService}
}

// createUserRequest represents the request body for creating a user
type createUserRequest struct {
	Username    string `json:"username" binding:"required,min=3,max=20"`
	Password    string `json:"password" binding:"required,min=6"`
	DisplayName string `json:"displayName" binding:"required"`
	Role        string `json:"role" binding:"required,oneof=admin annotator reviewer image_annotator image_reviewer audio_annotator audio_reviewer video_annotator video_reviewer"`
	EmployeeID  string `json:"employeeId,omitempty"`
	Email       string `json:"email,omitempty"`
}

// updateUserRequest represents the request body for updating a user
type updateUserRequest struct {
	DisplayName *string `json:"displayName,omitempty"`
	Email       *string `json:"email,omitempty"`
	EmployeeID  *string `json:"employeeId,omitempty"`
}

// resetPasswordRequest represents the request body for resetting password
type resetPasswordRequest struct {
	Password string `json:"password" binding:"required,min=6"`
}

// CreateUser handles POST /users
func (h *UserHandler) CreateUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	var emailPtr *string
	if req.Email != "" {
		email := req.Email
		emailPtr = &email
	}

	var employeeIDPtr *string
	if req.EmployeeID != "" {
		employeeID := req.EmployeeID
		employeeIDPtr = &employeeID
	}

	user, err := h.userService.CreateUser(c.Request.Context(), req.Username, req.Password, req.DisplayName, req.Role, emailPtr, employeeIDPtr)
	if err != nil {
		if err.Error() == "username already exists" {
			Error(c, http.StatusConflict, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "用户创建成功",
		"user":    user,
	})
}

// ListUsers handles GET /users
func (h *UserHandler) ListUsers(c *gin.Context) {
	page, pageSize := ParsePageParams(c)

	users, total, err := h.userService.ListUsers(c.Request.Context(), page, pageSize)
	if err != nil {
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"users":    users,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

// UpdateUser handles PUT /users/:id
func (h *UserHandler) UpdateUser(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid user ID")
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	user, err := h.userService.UpdateUser(c.Request.Context(), uint(id), req.DisplayName, req.Email, req.EmployeeID)
	if err != nil {
		if err.Error() == "user not found" {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "用户信息更新成功",
		"user":    user,
	})
}

// UpdateRole handles PUT /users/:id/role
func (h *UserHandler) UpdateRole(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid user ID")
		return
	}

	var req struct {
		Role string `json:"role" binding:"required,oneof=admin annotator reviewer image_annotator image_reviewer audio_annotator audio_reviewer video_annotator video_reviewer"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	err = h.userService.UpdateRole(c.Request.Context(), uint(id), req.Role)
	if err != nil {
		if err.Error() == "user not found" {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	h.authService.InvalidateAuth(uint(id)) // PH-5：新角色下次请求即时生效
	OK(c, "角色更新成功")
}

// UpdateStatus handles PUT /users/:id/status
func (h *UserHandler) UpdateStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid user ID")
		return
	}

	var req struct {
		Status string `json:"status" binding:"required,oneof=active disabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	err = h.userService.UpdateStatus(c.Request.Context(), uint(id), req.Status)
	if err != nil {
		if err.Error() == "user not found" {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	h.authService.InvalidateAuth(uint(id)) // PH-5：封停/启用下次请求即时生效
	OK(c, "状态更新成功")
}

// ResetPassword handles PUT /users/:id/password
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid user ID")
		return
	}

	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, err.Error())
		return
	}

	err = h.userService.ResetPassword(c.Request.Context(), uint(id), req.Password)
	if err != nil {
		if err.Error() == "user not found" {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	OK(c, "密码重置成功")
}

// DeleteUser handles DELETE /users/:id (admin)。禁止删除当前登录账号，避免误锁定。
func (h *UserHandler) DeleteUser(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		Error(c, http.StatusBadRequest, "invalid user ID")
		return
	}
	if uint(id) == c.GetUint("user_id") {
		Error(c, http.StatusBadRequest, "不能删除当前登录账号")
		return
	}
	if err := h.userService.DeleteUser(c.Request.Context(), uint(id)); err != nil {
		if err.Error() == "user not found" {
			Error(c, http.StatusNotFound, err.Error())
			return
		}
		Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	h.authService.InvalidateAuth(uint(id)) // PH-5：删除后其 token 立即失效
	OK(c, "用户已删除")
}
