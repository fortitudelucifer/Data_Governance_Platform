package logger

import (
	"context"
	"log/slog"
	"os"
)

// contextKey is the type for context keys used by this package.
type contextKey string

const requestIDKey contextKey = "request_id"

// New creates a JSON slog.Logger writing to w. If w is nil, os.Stdout is used.
// JSON format is chosen so log aggregators (Loki, CloudWatch, etc.) can parse
// fields without extra grok rules.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// Default is the package-level logger used by package-level helpers below.
// Callers that want a request-scoped logger with request_id should use
// FromContext instead.
var Default = New()

// WithRequestID returns a child logger that always includes the given
// request_id field. This is the canonical way to get a per-request logger
// from a gin handler.
func WithRequestID(base *slog.Logger, requestID string) *slog.Logger {
	return base.With(slog.String("request_id", requestID))
}

// ContextWithRequestID stores request_id in ctx so it can be retrieved by
// downstream service code without threading the logger explicitly.
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestIDFromContext retrieves the request_id stored by ContextWithRequestID.
// Returns "" when no request_id is present.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// FromContext returns a logger decorated with the request_id found in ctx,
// or Default when no request_id is present.
func FromContext(ctx context.Context) *slog.Logger {
	if rid := RequestIDFromContext(ctx); rid != "" {
		return WithRequestID(Default, rid)
	}
	return Default
}
