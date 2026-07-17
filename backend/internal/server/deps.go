// Package server centralises route registration so cmd/main.go (full-stack
// production deployment) and runner/server.go (Wails desktop embedded mode)
// share a single source of truth for the HTTP surface. The two entry points
// only differ in
// whether multi-modal handlers are wired in.
package server

import (
	"context"

	"text-annotation-platform/internal/api"
	"text-annotation-platform/internal/service"
)

// Deps bundles every handler RegisterRoutes can mount, plus the AuthService
// required by the JWT middleware.
//
// Multi-modal handlers are optional: leave them nil in a Deps built by the
// runner mode and the corresponding routes are simply
// not registered. The full cmd/main path sets every field.
type Deps struct {
	// AuthService — required (JWT middleware needs it).
	AuthService *service.AuthService

	// Configuration knobs that affect route wiring.
	AllowedOrigins []string

	// RedisProbe — optional; if set, health endpoints include a "redis" field.
	// Returns nil when Redis is healthy, non-nil error otherwise. Redis 为可选缓存，
	// 不通仅标记 degraded，不影响 /readyz 就绪。
	RedisProbe func(ctx context.Context) error

	// DBProbe — optional; if set, /readyz pings it as critical
	// dependencies（任一不通则 /readyz 返回 503，供 LB/K8s 摘流量）。
	DBProbe func(ctx context.Context) error

	// V1 handlers — always set by both entry points.
	AuthHandler            *api.AuthHandler
	CategoryHandler        *api.DatasetCategoryHandler
	TagHandler             *api.TagHandler
	DatasetHandler         *api.DatasetHandler
	DocumentHandler        *api.DocumentHandler
	LLMHandler             *api.LLMHandler
	ExportHandler          *api.ExportHandler
	SamplingHandler        *api.SamplingHandler
	AuditHandler           *api.AuditHandler
	SystemPromptHandler    *api.SystemPromptHandler
	AutoAnnotateHandler    *api.AutoAnnotateHandler
	TextCandidateHandler   *api.TextCandidateHandler
	RefinementHandler      *api.RefinementHandler
	DashboardHandler       *api.DashboardHandler
	ExtractionHandler      *api.ExtractionHandler
	DatasetFunctionHandler *api.DatasetFunctionHandler
	LLMRefinementHandler   *api.LLMRefinementHandler

	// Optional — only the cmd/main path wires these.
	UserHandler             *api.UserHandler             // user management (admin)
	CapabilityConfigHandler *api.CapabilityConfigHandler // capability provider CRUD (admin)
	AssetHandler            *api.AssetHandler            // multi-modal asset upload/list/detail
	TaskHandler             *api.TaskHandler             // multi-modal task lifecycle
	AnnotationHandler       *api.AnnotationHandler       // multi-modal human annotation + QA
	AIResultHandler         *api.AIResultHandler         // multi-modal AI results + invoke
	ImageExportHandler      *api.ImageExportHandler      // multi-modal dataset exports
	AudioExportHandler      *api.AudioExportHandler      // audio dataset exports (A3.3)
	VideoExportHandler      *api.VideoExportHandler      // video track exports (B3.3)
	TrackHandler            *api.TrackHandler            // video tracks (B1.0)
	ReviewCommentHandler    *api.ReviewCommentHandler    // frame/track-anchored review comments (B3.1)
	EditLockHandler         *api.EditLockHandler         // distributed task edit-lock (T0.4)
	MultipartHandler        *api.MultipartHandler        // resumable chunked upload (T0.2)
	BatchAnnotateHandler    *api.BatchAnnotateHandler    // list-level batch auto-annotation (item 4)
}

// hasMultiModal reports whether any multi-modal handler is wired. Used to
// guard the entire multi-modal route block in RegisterRoutes.
func (d Deps) hasMultiModal() bool {
	return d.AssetHandler != nil ||
		d.TaskHandler != nil ||
		d.AnnotationHandler != nil ||
		d.AIResultHandler != nil ||
		d.ImageExportHandler != nil
}
