package middleware

import (
	"net/http/httptest"
	"testing"
	"testing/quick"

	"github.com/gin-gonic/gin"
)

// --- Property 16: 用户上下文中间件注入
// Feature: text-annotation-platform, Property 16: 用户上下文中间件注入
// Validates: Requirements 21.3
//
// For any user_id value set in the Gin context (by JWT middleware),
// UserContextMiddleware correctly creates a UserContext with that user_id
// and Role="annotator" when role is missing. If user_role is present,
// it should be preserved. GetUserContext returns the correct UserContext
// after middleware runs. GetUserContext returns nil when middleware hasn't run.

func init() {
	gin.SetMode(gin.TestMode)
}

func TestProperty16_UserContextMiddlewareInjection(t *testing.T) {
	t.Run("MiddlewareInjectsCorrectUserContext", func(t *testing.T) {
		f := func(userID uint) bool {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			// Simulate JWT middleware setting user_id
			c.Set("user_id", userID)

			// Run UserContextMiddleware
			handler := UserContextMiddleware()
			handler(c)

			// Retrieve the UserContext
			uc := GetUserContext(c)
			if uc == nil {
				return false
			}

			// Verify user_id matches
			if uc.UserID != userID {
				return false
			}

			// Verify Role is downgraded to "annotator" when user_role is missing
			if uc.Role != "annotator" {
				return false
			}

			return true
		}

		if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
			t.Errorf("Property 16 (MiddlewareInjectsCorrectUserContext) failed: %v", err)
		}
	})

	t.Run("MiddlewarePreservesRoleFromJWT", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("user_id", uint(7))
		c.Set("user_role", "admin")

		handler := UserContextMiddleware()
		handler(c)

		uc := GetUserContext(c)
		if uc == nil {
			t.Fatalf("expected user context, got nil")
		}
		if uc.Role != "admin" {
			t.Fatalf("expected role admin, got %s", uc.Role)
		}
	})

	t.Run("GetUserContextReturnsNilWithoutMiddleware", func(t *testing.T) {
		// This is a deterministic check: without middleware, GetUserContext
		// must return nil for any context.
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)

		uc := GetUserContext(c)
		if uc != nil {
			t.Errorf("Expected nil UserContext when middleware has not run, got %+v", uc)
		}
	})
}
