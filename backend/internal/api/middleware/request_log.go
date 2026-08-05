package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger 统一的 JSON 请求日志（复用 request_id），取代 gin.Default 的文本
// 日志，与 worker 的 slog JSON 单轨对齐（PH-6 日志双轨修复）。
// 健康 / 指标端点不记，避免噪音淹没真实请求。
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		switch path {
		case "/metrics", "/livez", "/readyz", "/health":
			c.Next()
			return
		}
		start := time.Now()
		c.Next()
		slog.Info("http_request",
			"method", c.Request.Method,
			"path", path,
			"route", c.FullPath(),
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
			"request_id", c.GetString(RequestIDKey),
		)
	}
}
