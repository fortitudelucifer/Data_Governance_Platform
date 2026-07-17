package server

import (
	"context"
	"net/http"
	"time"

	"text-annotation-platform/internal/api/middleware"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RegisterRoutes wires the entire HTTP surface onto r. Routes whose
// corresponding handler is nil in deps are silently skipped — this is the
// hook that lets the runner mode skip the multi-modal stack.
//
// The route list is intentionally exhaustive in one place so any drift
// between the two entry points has a single source of truth.
func RegisterRoutes(r *gin.Engine, deps Deps) {
	r.Use(middleware.CORSMiddleware(deps.AllowedOrigins))
	r.Use(middleware.RequestIDMiddleware())

	// Health checks (public, no auth). PH-4：拆 liveness / readiness。
	// collect 探测各依赖；ok=false 表示有"关键依赖"(Postgres)不通。Redis 仅 degraded。
	collect := func(ctx context.Context) (gin.H, bool) {
		resp := gin.H{"status": "ok"}
		ok := true
		if deps.DBProbe != nil {
			if err := deps.DBProbe(ctx); err != nil {
				resp["db"] = "down: " + err.Error()
				ok = false
			} else {
				resp["db"] = "ok"
			}
		}
		if deps.RedisProbe != nil {
			if err := deps.RedisProbe(ctx); err != nil {
				resp["redis"] = "degraded: " + err.Error()
			} else {
				resp["redis"] = "ok"
			}
		}
		if !ok {
			resp["status"] = "not ready"
		}
		return resp, ok
	}
	// /livez：进程存活即 200（不探依赖），供 K8s livenessProbe。
	r.GET("/livez", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	// /readyz：关键依赖任一不通 → 503，LB/K8s 据此摘流量。
	r.GET("/readyz", func(c *gin.Context) {
		resp, ok := collect(c.Request.Context())
		if ok {
			c.JSON(http.StatusOK, resp)
		} else {
			c.JSON(http.StatusServiceUnavailable, resp)
		}
	})
	// /health：保留为 200 调试视图（展示各依赖状态，向后兼容历史调用）。
	r.GET("/health", func(c *gin.Context) {
		resp, _ := collect(c.Request.Context())
		c.JSON(http.StatusOK, resp)
	})

	// Public routes
	r.POST("/auth/login",
		middleware.IPRateLimit(rate.Every(6*time.Second), 10),
		deps.AuthHandler.Login)
	r.POST("/auth/logout", deps.AuthHandler.Logout) // PH-9：清除鉴权 cookie

	// JWT-protected routes
	protected := r.Group("/")
	protected.Use(middleware.JWTMiddleware(deps.AuthService))
	protected.Use(middleware.UserContextMiddleware())

	registerCategoryRoutes(protected, deps)
	registerTagRoutes(protected, deps)
	registerDatasetRoutes(protected, deps)
	registerDocumentRoutes(protected, deps)
	registerLLMRoutes(protected, deps)
	registerExportRoutes(protected, deps)
	registerSamplingRoutes(protected, deps)
	registerAuditRoutes(protected, deps)
	registerSystemPromptReadRoutes(protected, deps)
	registerAutoAnnotateRoutes(protected, deps)
	registerTextCandidateRoutes(protected, deps)
	registerRefinementRoutes(protected, deps)
	registerDashboardRoutes(protected, deps)
	registerExtractionRoutes(protected, deps)
	registerDatasetFunctionReadRoutes(protected, deps)

	if deps.hasMultiModal() {
		registerMultiModalRoutes(protected, deps)
	}

	registerAdminRoutes(protected, deps)
}

// ---------------------------------------------------------------------------
// V1 routes
// ---------------------------------------------------------------------------

func registerCategoryRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/dataset_categories", deps.CategoryHandler.ListCategories)
	g.POST("/dataset_categories", deps.CategoryHandler.CreateCategory)
	g.PUT("/dataset_categories/:id", deps.CategoryHandler.UpdateCategory)
	g.DELETE("/dataset_categories/:id", deps.CategoryHandler.DeleteCategory)
}

func registerTagRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/tags", deps.TagHandler.ListTags)
	g.POST("/tags", deps.TagHandler.CreateTag)
	g.PUT("/tags/:id", deps.TagHandler.UpdateTag)
	g.DELETE("/tags/:id", deps.TagHandler.DeleteTag)
}

func registerDatasetRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/datasets", deps.DatasetHandler.ListDatasets)
	g.GET("/datasets/options", deps.DatasetHandler.ListDatasetOptions)
	g.GET("/datasets/:id", deps.DatasetHandler.GetDataset)
	g.POST("/datasets", deps.DatasetHandler.CreateDataset)
	g.PUT("/datasets/:id", deps.DatasetHandler.UpdateDataset)
	g.PUT("/datasets/:id/export-meta", deps.DatasetHandler.UpdateExportMeta)
	// B2.8 成本闸门：detect_track 的数据集级配置。读开放（工作台要显示会用什么
	// 参数、是否被关闭），写仅 admin（天花板属于数据集所有者）。
	g.GET("/datasets/:id/video-ai-config", deps.DatasetHandler.GetVideoAIConfig)
	g.PUT("/datasets/:id/video-ai-config",
		middleware.RequireRole("admin"),
		deps.DatasetHandler.UpdateVideoAIConfig)
	g.DELETE("/datasets/:id", deps.DatasetHandler.DeleteDataset)
	g.GET("/datasets/:id/label-config", deps.DatasetHandler.GetLabelConfig)
	g.PUT("/datasets/:id/label-config", deps.DatasetHandler.PutLabelConfig)
	// A/V label ontology (T0.6). Read open to annotators (workspace renders
	// from it); writes admin-only.
	g.GET("/datasets/:id/ontology", deps.DatasetHandler.GetLabelOntology)
	g.PUT("/datasets/:id/ontology",
		middleware.RequireRole("admin"),
		deps.DatasetHandler.PutLabelOntology)
}

func registerDocumentRoutes(g *gin.RouterGroup, deps Deps) {
	rolesReview := []string{"admin", "reviewer", "image_reviewer"}
	g.POST("/datasets/:id/documents/import", deps.DocumentHandler.ImportDocuments)
	g.GET("/datasets/:id/documents", deps.DocumentHandler.ListDocuments)
	g.DELETE("/datasets/:id/documents/:key", middleware.RequireRole(rolesReview...), deps.DocumentHandler.DeleteDocument)
	g.POST("/datasets/:id/documents/batch_delete", middleware.RequireRole(rolesReview...), deps.DocumentHandler.BatchDeleteDocuments)
	g.POST("/datasets/:id/documents/range_delete", middleware.RequireRole(rolesReview...), deps.DocumentHandler.RangeDeleteDocuments)
	g.PUT("/datasets/:id/documents/deadline", deps.DocumentHandler.SetDocumentsDeadline)
	g.PUT("/datasets/:id/documents/assign", deps.DocumentHandler.AssignDocuments)
	g.GET("/documents/:key", deps.DocumentHandler.GetDocument)
	g.POST("/documents/:key/direct_complete", deps.DocumentHandler.DirectCompleteDocument)
	g.POST("/documents/:key/reannotate", deps.DocumentHandler.ReAnnotateDocument)
	g.GET("/documents/:key/versions", deps.DocumentHandler.GetVersionHistory)
	g.POST("/documents/:key/update", deps.DocumentHandler.UpdateDocument)
	g.GET("/import/formats", deps.DocumentHandler.ListImportFormats)
}

func registerLLMRoutes(g *gin.RouterGroup, deps Deps) {
	llmRateLimited := g.Group("/")
	llmRateLimited.Use(middleware.UserRateLimit(rate.Every(3*time.Second), 20))
	llmRateLimited.POST("/datasets/:id/qa/llm_suggest", deps.LLMHandler.GenerateQACandidates)
	llmRateLimited.GET("/llm/task_types", deps.LLMHandler.ListTaskTypes)
}

func registerExportRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/datasets/:id/export", deps.ExportHandler.ExportDataset)
	g.GET("/export/formats", deps.ExportHandler.ListExportFormats)
}

func registerSamplingRoutes(g *gin.RouterGroup, deps Deps) {
	g.POST("/datasets/:id/sampling", deps.SamplingHandler.GenerateSamplingPlan)
	g.GET("/sampling/strategies", deps.SamplingHandler.ListStrategies)
}

func registerAuditRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/audit_logs", deps.AuditHandler.QueryAuditLogs)
}

func registerSystemPromptReadRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/system_prompts", deps.SystemPromptHandler.ListPrompts)
	g.GET("/system_prompts/:case_type", deps.SystemPromptHandler.GetPrompt)
}

func registerAutoAnnotateRoutes(g *gin.RouterGroup, deps Deps) {
	g.POST("/datasets/:id/auto_annotate", deps.AutoAnnotateHandler.TriggerAutoAnnotate)
	g.POST("/datasets/:id/auto_annotate/range", deps.AutoAnnotateHandler.RangeAutoAnnotate)
	g.GET("/datasets/:id/auto_annotate/status", deps.AutoAnnotateHandler.GetAutoAnnotateStatus)
	g.POST("/datasets/:id/auto_annotate/cancel", deps.AutoAnnotateHandler.CancelAutoAnnotate)
}

func registerTextCandidateRoutes(g *gin.RouterGroup, deps Deps) {
	if deps.TextCandidateHandler == nil {
		return
	}
	textCandidateRateLimited := g.Group("/")
	textCandidateRateLimited.Use(middleware.UserRateLimit(rate.Every(3*time.Second), 20))
	textCandidateRateLimited.GET("/datasets/:id/auto_annotate/providers", deps.TextCandidateHandler.ListProviders)
	textCandidateRateLimited.GET("/datasets/:id/auto_annotate/prompts", deps.TextCandidateHandler.ListPromptTemplates)
	textCandidateRateLimited.GET("/datasets/:id/auto_annotate/judge_prompts", deps.TextCandidateHandler.ListJudgePromptTemplates)
	textCandidateRateLimited.POST("/datasets/:id/auto_annotate/compare", deps.TextCandidateHandler.Compare)
	textCandidateRateLimited.POST("/datasets/:id/auto_annotate/judge", deps.TextCandidateHandler.Judge)
	textCandidateRateLimited.GET("/documents/:key/auto_annotate/candidates", deps.TextCandidateHandler.List)
	textCandidateRateLimited.DELETE("/documents/:key/auto_annotate/candidates/:run_id", deps.TextCandidateHandler.Delete)
	textCandidateRateLimited.GET("/documents/:key/auto_annotate/judges", deps.TextCandidateHandler.ListJudges)
	textCandidateRateLimited.POST("/documents/:key/qa_pairs/adopt", deps.TextCandidateHandler.Adopt)
	textCandidateRateLimited.POST("/documents/:key/qa_pairs/adopt_judge", deps.TextCandidateHandler.AdoptJudge)
}

func registerRefinementRoutes(g *gin.RouterGroup, deps Deps) {
	refinementRateLimited := g.Group("/")
	refinementRateLimited.Use(middleware.UserRateLimit(rate.Every(3*time.Second), 20))
	refinementRateLimited.POST("/documents/:key/start_refinement", deps.RefinementHandler.StartRefinement)
	refinementRateLimited.PUT("/documents/:key/refinement_cursor", deps.RefinementHandler.NavigateCursor)
	refinementRateLimited.PUT("/documents/:key/qa_pairs/:index", deps.RefinementHandler.EditQAPair)
	refinementRateLimited.DELETE("/documents/:key/qa_pairs/:index", deps.RefinementHandler.DeleteQAPair)
	refinementRateLimited.POST("/documents/:key/qa_pairs", deps.RefinementHandler.AddQAPair)
	refinementRateLimited.PUT("/documents/:key/qa_pairs_bulk", deps.RefinementHandler.BulkUpdateQAPairs)
	refinementRateLimited.POST("/documents/:key/complete_refinement", deps.RefinementHandler.CompleteRefinement)

	// LLM Refinement
	refinementRateLimited.POST("/documents/:key/llm-refine", deps.LLMRefinementHandler.TriggerRefinement)
	refinementRateLimited.DELETE("/documents/:key/llm-refine", deps.LLMRefinementHandler.RollbackRefinement)
}

func registerDashboardRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/dashboard/stats", deps.DashboardHandler.GetStats)
	g.GET("/dashboard/trend", deps.DashboardHandler.GetTrend)
	g.GET("/dashboard/annotators", deps.DashboardHandler.GetAnnotatorStats)
	if deps.hasMultiModal() {
		g.GET("/dashboard/image-annotators", deps.DashboardHandler.GetImageAnnotatorStats)
	}
}

func registerExtractionRoutes(g *gin.RouterGroup, deps Deps) {
	g.POST("/datasets/:id/extraction", deps.ExtractionHandler.Execute)
	g.GET("/datasets/:id/extractions", deps.ExtractionHandler.ListResults)
	g.GET("/extractions/:id/documents", deps.ExtractionHandler.GetResultDocuments)
	g.GET("/extraction/filters", deps.ExtractionHandler.ListFilters)
}

func registerDatasetFunctionReadRoutes(g *gin.RouterGroup, deps Deps) {
	g.GET("/dataset_functions", deps.DatasetFunctionHandler.List)
}

// ---------------------------------------------------------------------------
// Multi-modal routes (only when hasMultiModal())
// ---------------------------------------------------------------------------

// Asset-track RBAC. T0.6 generalizes the image-only roles to modality-agnostic
// annotator/reviewer while keeping modality-specific aliases, so the same asset
// endpoints serve image + audio + video. See plan_v2 执行方案-00 T0.6.
var (
	rolesAnnotate         = []string{"admin", "annotator", "image_annotator", "audio_annotator", "video_annotator"}
	rolesReview           = []string{"admin", "reviewer", "image_reviewer", "audio_reviewer", "video_reviewer"}
	rolesAnnotateOrReview = []string{"admin", "annotator", "reviewer", "image_annotator", "image_reviewer", "audio_annotator", "audio_reviewer", "video_annotator", "video_reviewer"}
)

func registerMultiModalRoutes(g *gin.RouterGroup, deps Deps) {
	// Asset endpoints (image/audio/video) — upload restricted to annotators.
	if deps.AssetHandler != nil {
		g.POST("/datasets/:id/assets",
			middleware.RequireRole(rolesAnnotate...),
			deps.AssetHandler.Upload)
		g.GET("/datasets/:id/assets", deps.AssetHandler.List)
		g.GET("/assets/:id", deps.AssetHandler.Detail)
		g.GET("/assets/:id/body", deps.AssetHandler.Body)
		g.GET("/assets/:id/derivative/:kind", deps.AssetHandler.Derivative) // T0.3
		// Hard-delete a sample (blob + tasks + annotations). Curation action.
		g.DELETE("/assets/:id",
			middleware.RequireRole(rolesReview...),
			deps.AssetHandler.Delete)
	}

	// Resumable chunked upload (T0.2). Separate /uploads prefix to avoid gin's
	// static-vs-param conflict with /assets/:id. Annotators only.
	if deps.MultipartHandler != nil {
		g.POST("/uploads/init",
			middleware.RequireRole(rolesAnnotate...),
			deps.MultipartHandler.Init)
		g.GET("/uploads/:session_id",
			middleware.RequireRole(rolesAnnotate...),
			deps.MultipartHandler.Status)
		g.POST("/uploads/complete",
			middleware.RequireRole(rolesAnnotate...),
			deps.MultipartHandler.Complete)
		g.POST("/uploads/abort",
			middleware.RequireRole(rolesAnnotate...),
			deps.MultipartHandler.Abort)
	}

	// Annotation task lifecycle
	if deps.TaskHandler != nil {
		g.POST("/assets/:id/tasks",
			middleware.RequireRole(rolesAnnotate...),
			deps.TaskHandler.CreateTask)
		g.GET("/tasks", deps.TaskHandler.ListTasks)
		g.GET("/tasks/:id", deps.TaskHandler.GetTask)
		g.PUT("/tasks/:id/assign",
			middleware.RequireRole("admin"),
			deps.TaskHandler.AssignTask)
		g.POST("/tasks/batch-assign",
			middleware.RequireRole("admin"),
			deps.TaskHandler.BatchAssignTasks)
		g.POST("/tasks/:id/reprocess",
			middleware.RequireRole(rolesReview...),
			deps.TaskHandler.Reprocess)
		g.GET("/tasks/:id/adjacent", deps.TaskHandler.GetAdjacentTasks)
	}

	// Distributed edit-lock (T0.4) — annotators/reviewers acquire on entering
	// a workspace, refresh on a heartbeat, release on leave.
	if deps.EditLockHandler != nil {
		g.POST("/tasks/:id/lock",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.EditLockHandler.Acquire)
		g.POST("/tasks/:id/lock/refresh",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.EditLockHandler.Refresh)
		g.DELETE("/tasks/:id/lock",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.EditLockHandler.Release)
	}

	// Video tracks (B1.0) — per-track upsert (optimistic lock/409), adopt, delete.
	if deps.TrackHandler != nil {
		g.GET("/tasks/:id/tracks",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.TrackHandler.ListTracks)
		g.PUT("/tasks/:id/tracks",
			middleware.RequireRole(rolesAnnotate...),
			deps.TrackHandler.PutTrack)
		g.DELETE("/tasks/:id/tracks/:tid",
			middleware.RequireRole(rolesAnnotate...),
			deps.TrackHandler.DeleteTrack)
		g.POST("/tasks/:id/tracks/:tid/adopt",
			middleware.RequireRole(rolesAnnotate...),
			deps.TrackHandler.AdoptTrack)
		// Per-track review verdict (B3.1) — reviewer's per-object checklist.
		g.POST("/tasks/:id/tracks/:tid/review",
			middleware.RequireRole(rolesReview...),
			deps.TrackHandler.ReviewTrack)
		// Rework diff (B3.1): what changed between two submission rounds, so a
		// reviewer re-checking a rework only looks at what actually moved.
		g.GET("/tasks/:id/rounds",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.TrackHandler.ListRounds)
		g.GET("/tasks/:id/diff",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.TrackHandler.DiffRounds)
		// Manual AI detect+track trigger (video.detect_track → mm_tracks source:ai).
		g.POST("/tasks/:id/detect-track",
			middleware.RequireRole(rolesAnnotate...),
			deps.TrackHandler.DetectTrack)
		// SAM2 cross-frame propagation (点选→整条 mask track).
		g.POST("/tasks/:id/propagate",
			middleware.RequireRole(rolesAnnotate...),
			deps.TrackHandler.Propagate)
		// Batch adopt AI tracks (all / by label / by score threshold).
		// Sibling of detect-track (not under /tracks/:tid) to avoid a router
		// static-vs-param conflict with /tasks/:id/tracks/:tid.
		g.POST("/tasks/:id/adopt-tracks",
			middleware.RequireRole(rolesAnnotate...),
			deps.TrackHandler.AdoptBatch)
	}

	// Review comments anchored to frame+track (B3.1). Reviewers file them,
	// annotators resolve them; a rejected task cannot be re-submitted while any
	// remain open.
	if deps.ReviewCommentHandler != nil {
		g.GET("/tasks/:id/review-comments",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.ReviewCommentHandler.ListComments)
		g.POST("/tasks/:id/review-comments",
			middleware.RequireRole(rolesReview...),
			deps.ReviewCommentHandler.CreateComment)
		// Resolving is the annotator's job; a reviewer may reopen a comment.
		g.PATCH("/tasks/:id/review-comments/:cid",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.ReviewCommentHandler.ResolveComment)
		// Only a reviewer/admin may retract a comment (service also checks author).
		g.DELETE("/tasks/:id/review-comments/:cid",
			middleware.RequireRole(rolesReview...),
			deps.ReviewCommentHandler.DeleteComment)
	}

	// AI results / routing / capabilities / invoke
	if deps.AIResultHandler != nil {
		g.GET("/tasks/:id/routing", deps.AIResultHandler.GetRouting)
		g.GET("/tasks/:id/ai-runs", deps.AIResultHandler.GetAIRuns)
		g.GET("/tasks/:id/ai-results", deps.AIResultHandler.GetAIResults)
		g.GET("/tasks/:id/trace", deps.AIResultHandler.GetTrace)
		g.GET("/capabilities", deps.AIResultHandler.ListCapabilities)
		// GPU 队列积压（B2.8）：工作台轮询，满了就先把按钮置灰。
		g.GET("/capabilities/gpu-queue", deps.AIResultHandler.GetGPUQueue)
		// 能力→模型清单（env 适配器 + 已启用 DB provider），供工作台模型下拉。
		// 只读、不含 endpoint/key，故放在标注员可访问的组（非 admin）。
		if deps.CapabilityConfigHandler != nil {
			g.GET("/capabilities/models", deps.CapabilityConfigHandler.ListModels)
		}
		g.POST("/tasks/:id/invoke",
			middleware.UserRateLimit(rate.Every(3*time.Second), 20),
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.AIResultHandler.InvokeCapabilityOnTask)
	}

	// 数据资产列表级批量自动标注（item 4）：勾选任务 → 选能力+模型 → 有界并发跑 +
	// 进度轮询 + 取消。复用 ad-hoc invoke（模型可选）。
	if deps.BatchAnnotateHandler != nil {
		g.POST("/datasets/:id/assets/auto_annotate",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.BatchAnnotateHandler.Start)
		g.GET("/datasets/:id/assets/auto_annotate/status", deps.BatchAnnotateHandler.Status)
		g.POST("/datasets/:id/assets/auto_annotate/cancel",
			middleware.RequireRole(rolesAnnotateOrReview...),
			deps.BatchAnnotateHandler.Cancel)
	}

	// Human annotation + QA + final + interactive segmentation
	if deps.AnnotationHandler != nil {
		g.GET("/tasks/:id/human-annotation", deps.AnnotationHandler.GetHumanAnnotation)
		g.PUT("/tasks/:id/human-annotation",
			middleware.RequireRole(rolesAnnotate...),
			deps.AnnotationHandler.PutHumanAnnotation)
		g.POST("/tasks/:id/submit",
			middleware.RequireRole(rolesAnnotate...),
			deps.AnnotationHandler.SubmitTask)
		g.POST("/tasks/:id/qa/pass",
			middleware.RequireRole(rolesReview...),
			deps.AnnotationHandler.QAPass)
		g.POST("/tasks/:id/qa/reject",
			middleware.RequireRole(rolesReview...),
			deps.AnnotationHandler.QAReject)
		g.GET("/tasks/:id/final", deps.AnnotationHandler.GetFinal)
		g.POST("/tasks/:id/segment",
			middleware.UserRateLimit(rate.Every(3*time.Second), 20),
			middleware.RequireRole(rolesAnnotate...),
			deps.AnnotationHandler.SegmentInteractive)
	}

	// Image dataset exports
	if deps.ImageExportHandler != nil {
		g.GET("/datasets/:id/final-annotations.jsonl",
			middleware.RequireRole(rolesReview...),
			deps.ImageExportHandler.ExportDatasetFinalAnnotations)
		g.GET("/datasets/:id/export.coco.json",
			middleware.RequireRole(rolesReview...),
			deps.ImageExportHandler.ExportCOCO)
		g.GET("/datasets/:id/export.yolo-seg.zip",
			middleware.RequireRole(rolesReview...),
			deps.ImageExportHandler.ExportYOLOSeg)
		g.GET("/datasets/:id/export.jsonld",
			middleware.RequireRole(rolesReview...),
			deps.ImageExportHandler.ExportJSONLD)
	}

	// Audio dataset exports (WebVTT / SRT / RTTM / CSV / JSONL)
	if deps.AudioExportHandler != nil {
		g.GET("/datasets/:id/export.audio",
			middleware.RequireRole(rolesReview...),
			deps.AudioExportHandler.ExportAudio)
	}

	// Video dataset exports (CVAT-XML / MOT / JSONL)
	if deps.VideoExportHandler != nil {
		g.GET("/datasets/:id/export.video",
			middleware.RequireRole(rolesReview...),
			deps.VideoExportHandler.ExportVideo)
	}
}

// ---------------------------------------------------------------------------
// Admin-only routes
// ---------------------------------------------------------------------------

func registerAdminRoutes(g *gin.RouterGroup, deps Deps) {
	admin := g.Group("")
	admin.Use(middleware.RequireRole("admin"))

	// Capability config management — only present when the capability stack
	// is wired (i.e. multi-modal mode).
	if deps.CapabilityConfigHandler != nil {
		admin.GET("/capabilities/types", deps.CapabilityConfigHandler.ListTypes)
		admin.GET("/capabilities/providers", deps.CapabilityConfigHandler.ListProviders)
		admin.GET("/capabilities/providers/env", deps.CapabilityConfigHandler.ListEnvAdapters)
		admin.POST("/capabilities/providers/probe", deps.CapabilityConfigHandler.ProbeProvider)
		admin.GET("/capabilities/providers/:id", deps.CapabilityConfigHandler.GetProvider)
		admin.POST("/capabilities/providers", deps.CapabilityConfigHandler.CreateProvider)
		admin.PUT("/capabilities/providers/:id", deps.CapabilityConfigHandler.UpdateProvider)
		admin.DELETE("/capabilities/providers/:id", deps.CapabilityConfigHandler.DeleteProvider)
		admin.POST("/capabilities/providers/:id/test", deps.CapabilityConfigHandler.TestProvider)
		admin.GET("/capabilities/litellm/config", deps.CapabilityConfigHandler.GetLiteLLMConfig)
		admin.PUT("/capabilities/litellm/config", deps.CapabilityConfigHandler.UpdateLiteLLMConfig)
	}

	// System Prompts management
	admin.POST("/system_prompts", deps.SystemPromptHandler.CreatePrompt)
	admin.PUT("/system_prompts/:case_type", deps.SystemPromptHandler.UpdatePrompt)
	admin.GET("/auto_prompt_templates", deps.SystemPromptHandler.ListAutoPromptTemplates)
	admin.POST("/auto_prompt_templates", deps.SystemPromptHandler.CreateAutoPromptTemplate)
	admin.PUT("/auto_prompt_templates/:id", deps.SystemPromptHandler.UpdateAutoPromptTemplate)

	// Dataset Functions management
	admin.POST("/dataset_functions", deps.DatasetFunctionHandler.Create)
	admin.PUT("/dataset_functions/:id", deps.DatasetFunctionHandler.Update)
	admin.DELETE("/dataset_functions/:id", deps.DatasetFunctionHandler.Delete)

	// Dataset counter backfill / repair.
	if deps.DashboardHandler != nil {
		admin.POST("/rebuild_counters", deps.DashboardHandler.RebuildCounters)
	}

	// User management — only present in full deployments.
	if deps.UserHandler != nil {
		admin.GET("/users", deps.UserHandler.ListUsers)
		admin.POST("/users", deps.UserHandler.CreateUser)
		admin.PUT("/users/:id", deps.UserHandler.UpdateUser)
		admin.PUT("/users/:id/role", deps.UserHandler.UpdateRole)
		admin.PUT("/users/:id/status", deps.UserHandler.UpdateStatus)
		admin.PUT("/users/:id/password", deps.UserHandler.ResetPassword)
		admin.DELETE("/users/:id", deps.UserHandler.DeleteUser)
	}
}
