package api

import (
	"net/http"
	"os"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// authCookieName 是承载 JWT 的 HttpOnly cookie 名（PH-9）。中间件按同名读取。
const authCookieName = "auth_token"

// AuthHandler handles authentication-related HTTP endpoints.
type AuthHandler struct {
	authService *service.AuthService
}

// NewAuthHandler creates an AuthHandler with the given AuthService dependency.
func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{authService: authService}
}

// setAuthCookie 写入 HttpOnly 鉴权 cookie（PH-9）：JS 读不到，规避 XSS 窃取 token。
// SameSite=Lax 提供 CSRF 防护（跨站不带上状态变更请求）；prod 下 Secure（HTTPS）。
func setAuthCookie(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authCookieName, token, 24*60*60, "/", "", cookieSecure(), true)
}

func clearAuthCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(authCookieName, "", -1, "/", "", cookieSecure(), true)
}

// cookieSecure returns whether the auth cookie should have the Secure flag.
// Default: true only when APP_ENV=prod. Can be overridden with COOKIE_SECURE=false
// for internal HTTP deployments.
func cookieSecure() bool {
	if v := os.Getenv("COOKIE_SECURE"); v != "" {
		return v != "false"
	}
	return os.Getenv("APP_ENV") == "prod"
}

// loginRequest represents the expected JSON body for POST /auth/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// registerRequest represents the expected JSON body for POST /auth/register.
type registerRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code"`
}

// Login handles POST /auth/login.
// It validates the request body, authenticates via AuthService, and returns
// a JWT token along with user info on success.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		Error(c, http.StatusBadRequest, "username and password are required")
		return
	}

	token, user, err := h.authService.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		Error(c, http.StatusUnauthorized, err.Error())
		return
	}

	setAuthCookie(c, token) // PH-9：鉴权走 HttpOnly cookie；body 仍回 token 兼容脚本/API 客户端

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user": gin.H{
			"id":           user.ID,
			"username":     user.Username,
			"displayName":  user.DisplayName,
			"role":         user.Role,
			"status":       user.Status,
			"employeeId":   user.EmployeeID,
			"email":        user.Email,
			"lastLoginAt":  user.LastLoginAt,
			"createdAt":    user.CreatedAt,
			"updatedAt":    user.UpdatedAt,
		},
	})
}

// Logout handles POST /auth/logout：清除鉴权 cookie（PH-9）。公开端点，幂等。
func (h *AuthHandler) Logout(c *gin.Context) {
	clearAuthCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

// Register handles POST /auth/register.
func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Error(c, http.StatusBadRequest, "无效的提交参数 / invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		Error(c, http.StatusBadRequest, "用户名和密码不能为空 / username and password are required")
		return
	}

	if err := h.authService.Register(c.Request.Context(), req.Username, req.Password, req.InviteCode); err != nil {
		Error(c, http.StatusConflict, err.Error())
		return
	}

	OK(c, "注册成功 / registration successful")
}
