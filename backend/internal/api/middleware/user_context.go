package middleware

import (
	"github.com/gin-gonic/gin"
)

// UserContext holds the authenticated user's identity and role information.
// Currently in single-user mode, Role defaults to "admin". This struct is
// designed to support future RBAC expansion with roles like "annotator" and
// "reviewer".
type UserContext struct {
	UserID uint
	Role   string // "admin", "annotator", "reviewer" (reserved for multi-user)
}

// userContextKey is the Gin context key used to store the UserContext.
const userContextKey = "user_context"

// UserContextMiddleware extracts the user_id and user_role set by JWTMiddleware and injects
// a UserContext into the Gin context. The role is read from the JWT token; if missing,
// defaults to "annotator" for security.
func UserContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetUint("user_id") // set by JWTMiddleware
		role := c.GetString("user_role") // set by JWTMiddleware
		
		// Security fallback: default to annotator rather than admin
		if role == "" {
			role = "annotator"
		}
		
		c.Set(userContextKey, &UserContext{
			UserID: userID,
			Role:   role,
		})
		c.Next()
	}
}

// GetUserContext retrieves the UserContext from the Gin context. Returns nil if
// the context does not contain a UserContext (e.g. the middleware was not applied).
func GetUserContext(c *gin.Context) *UserContext {
	val, exists := c.Get(userContextKey)
	if !exists {
		return nil
	}
	uc, ok := val.(*UserContext)
	if !ok {
		return nil
	}
	return uc
}

// RequireRole returns a middleware that restricts access to users with the specified roles.
// If the user's role is not in the allowedRoles list, it aborts with 403 Forbidden.
func RequireRole(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		uc := GetUserContext(c)
		if uc == nil {
			c.AbortWithStatusJSON(403, gin.H{"error": "无权访问"})
			return
		}
		
		for _, r := range allowedRoles {
			if uc.Role == r {
				c.Next()
				return
			}
		}
		
		c.AbortWithStatusJSON(403, gin.H{"error": "权限不足"})
	}
}
