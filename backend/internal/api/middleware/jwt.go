package middleware

import (
	"net/http"
	"strings"

	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// JWTMiddleware returns a Gin middleware that validates JWT tokens from the
// Authorization header (Bearer scheme). On success it stores the user_id and user_role in
// the Gin context for downstream handlers. On failure it aborts with 401.
func JWTMiddleware(authService *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// PH-9：优先从 HttpOnly cookie 取 token；回退到 Authorization: Bearer
		// （脚本/API 客户端仍可用 header）。
		var tokenStr string
		if ck, err := c.Cookie("auth_token"); err == nil && ck != "" {
			tokenStr = ck
		} else {
			authHeader := c.GetHeader("Authorization")
			if authHeader == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "missing credentials (cookie or bearer)",
				})
				return
			}
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"error": "invalid authorization header format, expected 'Bearer <token>'",
				})
				return
			}
			tokenStr = parts[1]
		}
		userID, _, err := authService.ValidateToken(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or expired token",
			})
			return
		}

		// PH-5：实时校验账号状态/角色（带 30s 缓存）——封停/降级 ≤30s 生效，不再等
		// token 自然过期；授权用 DB 当前角色而非 token 内固化的角色。
		role, active, err := authService.CheckActive(c.Request.Context(), userID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "account not found"})
			return
		}
		if !active {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "account disabled"})
			return
		}

		c.Set("user_id", userID)
		c.Set("user_role", role)
		c.Next()
	}
}
