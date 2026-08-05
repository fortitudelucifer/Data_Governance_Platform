package middleware

import (
	"crypto/rand"
	"encoding/hex"

	"text-annotation-platform/internal/logger"

	"github.com/gin-gonic/gin"
)

const RequestIDKey = "request_id"

// RequestIDMiddleware generates a random hex request ID for every inbound
// request, injects it into:
//   - the Gin context under the key "request_id"
//   - the request context via logger.ContextWithRequestID (for service-layer use)
//   - the response header X-Request-Id
//
// Downstream handlers retrieve the ID with c.GetString(RequestIDKey) and
// get a pre-decorated slog logger with logger.FromContext(c.Request.Context()).
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-Id")
		if rid == "" {
			rid = generateRequestID()
		}
		c.Set(RequestIDKey, rid)
		c.Header("X-Request-Id", rid)
		c.Request = c.Request.WithContext(
			logger.ContextWithRequestID(c.Request.Context(), rid),
		)
		c.Next()
	}
}

func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "fallback-no-entropy"
	}
	return hex.EncodeToString(b)
}
