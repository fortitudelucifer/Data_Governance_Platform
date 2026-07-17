package main

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"text-annotation-platform/config"
	"text-annotation-platform/internal/api"
	"text-annotation-platform/internal/api/middleware"
	"text-annotation-platform/internal/cache"
	"text-annotation-platform/internal/logger"
	"text-annotation-platform/internal/server"
	"text-annotation-platform/internal/util"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/plugin/builtin"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/service"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	if err := util.SetAppLocation(cfg.Timezone); err != nil {
		log.Printf("warn: failed to load timezone %q: %v (falling back to default)", cfg.Timezone, err)
	}

	dbRepo, err := repository.NewDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	// schema 全部由版本化迁移负责（internal/repository/migrations）。这里曾经有
	// 一段「启动时 ALTER TABLE 并吞掉错误」的补丁（M4）——那不是迁移，是碰运气；
	// Postgres 单真源后没有 legacy 库需要修补，整段删除。

	// 文本文档线(DocumentDB 接口)已切 Postgres 实现(07·接线批 A):
	// documents 表由 goose 建出,含 pg_trgm 检索索引。
	docDB := repository.NewRelationalDocRepo(dbRepo.DB)


	// Redis cache — optional; empty REDIS_URL disables caching (dev fallback to DB).
	var redisCache *cache.Cache
	var redisClient *goredis.Client
	if cfg.Redis.URL != "" {
		opt, err := goredis.ParseURL(cfg.Redis.URL)
		if err != nil {
			log.Fatalf("invalid REDIS_URL: %v", err)
		}
		// 只有显式设了 REDIS_DB 才覆盖；否则尊重 REDIS_URL 路径里的 db
		// （此前是无条件覆盖，redis://…/1 会被静默降成 db 0）。
		if cfg.Redis.DBSet {
			opt.DB = cfg.Redis.DB
		}
		if cfg.Redis.Password != "" {
			opt.Password = cfg.Redis.Password
		}
		redisClient = goredis.NewClient(opt)
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			log.Printf("[redis] ping failed, cache disabled: %v", err)
			redisClient = nil
		} else {
			redisCache = cache.New(redisClient)
			log.Printf("[redis] connected (url=%s db=%d)", cfg.Redis.URL, cfg.Redis.DB)
		}
	} else {
		log.Printf("[redis] REDIS_URL not set, cache disabled (all reads fall through to DB)")
	}
	// Multi-modal: object store. "local" (default) or "minio".
	var assetStore service.ObjectStore
	switch cfg.MultiModal.ObjectStoreDriver {
	case "local", "":
		localStore, err := service.NewLocalObjectStore(cfg.MultiModal.ObjectStoreRoot)
		if err != nil {
			log.Fatalf("Failed to init local object store: %v", err)
		}
		assetStore = localStore
		log.Printf("[object-store] driver=local root=%s", cfg.MultiModal.ObjectStoreRoot)
	case "minio":
		minioStore, err := service.NewMinIOObjectStore(context.Background(), service.MinIOConfig{
			Endpoint:  cfg.MultiModal.MinIOEndpoint,
			AccessKey: cfg.MultiModal.MinIOAccessKey,
			SecretKey: cfg.MultiModal.MinIOSecretKey,
			Bucket:    cfg.MultiModal.MinIOBucket,
			UseSSL:    cfg.MultiModal.MinIOUseSSL,
		})
		if err != nil {
			log.Fatalf("Failed to init MinIO object store: %v", err)
		}
		assetStore = minioStore
		log.Printf("[object-store] driver=minio endpoint=%s bucket=%s", cfg.MultiModal.MinIOEndpoint, cfg.MultiModal.MinIOBucket)
	default:
		log.Fatalf("Unsupported object store driver %q (supported: local, minio)", cfg.MultiModal.ObjectStoreDriver)
	}
	assetService := service.NewAssetService(dbRepo, assetStore, service.NewQCService(service.QCConfig{})).WithCache(redisCache)

	// T0.2: resumable chunked upload. Only the MinIO driver supports multipart;
	// for local the store assertion yields nil and the service reports
	// unsupported (frontend falls back to simple upload).
	mpStore, _ := assetStore.(service.MultipartObjectStore)
	multipartService := service.NewMultipartUploadService(dbRepo, mpStore, assetService, service.DefaultMultipartUploadConfig())

	// AssetReader closure: required by VLM / OCR adapters to fetch image bytes.
	assetReader := func(ctx context.Context, storageURI string) (io.ReadCloser, error) {
		return assetStore.Get(ctx, storageURI)
	}

	// CapabilityService + adapters. P0 wires LiteLLM (VLM) and PaddleOCR
	// (OCR) when their endpoints are configured. Missing configuration
	// disables the related capability and the worker falls back to
	// HUMAN_PENDING per ADR-09.
	capabilityService := service.NewCapabilityService(dbRepo)
	if cfg.MultiModal.LiteLLMEndpoint != "" {
		liteClient := service.NewLiteLLMClient(cfg.MultiModal.LiteLLMEndpoint, cfg.MultiModal.LiteLLMAPIKey, cfg.MultiModal.AITimeout)
		// Default VLM: structured extract for VLM_FIRST routes.
		modelName := os.Getenv("MM_VLM_MODEL")
		if modelName == "" {
			modelName = "qwen-vl"
		}
		capabilityService.Register(service.NewVLMAdapter(service.VLMAdapterConfig{
			Capability:   service.CapabilityVLMStructured,
			Model:        modelName,
			ProviderName: "litellm",
			Client:       liteClient,
			Reader:       assetReader,
		}))
		capabilityService.Register(service.NewVLMAdapter(service.VLMAdapterConfig{
			Capability:   service.CapabilityVLMCaption,
			Model:        modelName,
			ProviderName: "litellm",
			Client:       liteClient,
			Reader:       assetReader,
		}))
		log.Printf("[capability] registered LiteLLM VLM adapters (model=%s)", modelName)
	}
	if cfg.MultiModal.OCREndpoint != "" {
		ocrAdapter := service.NewOCRHTTPAdapter(service.OCRAdapterConfig{
			Capability:   service.CapabilityOCRStructure,
			Endpoint:     cfg.MultiModal.OCREndpoint,
			APIKey:       cfg.MultiModal.OCRAPIKey,
			ProviderName: "paddleocr",
			Reader:       assetReader,
			Timeout:      cfg.MultiModal.AITimeout,
		})
		capabilityService.Register(ocrAdapter)
		log.Printf("[capability] registered PaddleOCR adapter (endpoint=%s)", cfg.MultiModal.OCREndpoint)
	}
	if cfg.MultiModal.SegEndpoint != "" {
		segAdapter := service.NewSegmentationHTTPAdapter(service.SegAdapterConfig{
			Capability:   service.CapabilitySegInstance,
			Endpoint:     cfg.MultiModal.SegEndpoint,
			APIKey:       cfg.MultiModal.SegAPIKey,
			ProviderName: "yolov8-seg",
			Reader:       assetReader,
			Timeout:      cfg.MultiModal.AITimeout,
		})
		capabilityService.Register(segAdapter)
		log.Printf("[capability] registered YOLOv8-seg adapter (endpoint=%s)", cfg.MultiModal.SegEndpoint)
	}
	if cfg.MultiModal.SAMEndpoint != "" {
		samAdapter := service.NewSAMInteractiveAdapter(service.SAMAdapterConfig{
			Endpoint: cfg.MultiModal.SAMEndpoint,
			APIKey:   cfg.MultiModal.SAMAPIKey,
			Timeout:  30 * time.Second,
			Reader:   assetReader,
		})
		capabilityService.Register(samAdapter)
		log.Printf("[capability] registered MobileSAM adapter (endpoint=%s)", cfg.MultiModal.SAMEndpoint)
	}
	if cfg.MultiModal.ASREndpoint != "" {
		asrAdapter := service.NewASRHTTPAdapter(service.ASRAdapterConfig{
			Endpoint:     cfg.MultiModal.ASREndpoint,
			APIKey:       cfg.MultiModal.ASRAPIKey,
			ProviderName: "funasr",
			Timeout:      cfg.MultiModal.AITimeout,
			Reader:       assetReader,
		})
		capabilityService.Register(asrAdapter)
		log.Printf("[capability] registered FunASR ASR adapter (endpoint=%s)", cfg.MultiModal.ASREndpoint)
	}
	if cfg.MultiModal.DetEndpoint != "" {
		detAdapter := service.NewDetTrackAdapter(service.DetTrackAdapterConfig{
			Endpoint:     cfg.MultiModal.DetEndpoint,
			APIKey:       cfg.MultiModal.DetAPIKey,
			ProviderName: "det-server",
			MaxQueue:     cfg.MultiModal.GPUMaxQueue,
			Timeout:      cfg.MultiModal.AITimeout,
			Reader:       assetReader,
			Tools:        service.NewMediaTools(cfg.MultiModal.FFmpegPath, cfg.MultiModal.FFprobePath),
			DB:        dbRepo,
			Payload:      dbRepo,
		})
		capabilityService.Register(detAdapter)
		log.Printf("[capability] registered det-server detect_track adapter (endpoint=%s)", cfg.MultiModal.DetEndpoint)
	}
	if cfg.MultiModal.SAM2Endpoint != "" {
		sam2Adapter := service.NewSAM2PropagateAdapter(service.SAM2PropagateAdapterConfig{
			Endpoint:     cfg.MultiModal.SAM2Endpoint,
			APIKey:       cfg.MultiModal.SAM2APIKey,
			ProviderName: "sam2-video",
			MaxQueue:     cfg.MultiModal.GPUMaxQueue,
			Timeout:      cfg.MultiModal.AITimeout,
			Reader:       assetReader,
			Tools:        service.NewMediaTools(cfg.MultiModal.FFmpegPath, cfg.MultiModal.FFprobePath),
			DB:        dbRepo,
			Payload:      dbRepo,
		})
		capabilityService.Register(sam2Adapter)
		log.Printf("[capability] registered sam2-video propagate adapter (endpoint=%s)", cfg.MultiModal.SAM2Endpoint)
	}
	if cfg.MultiModal.AudioEndpoint != "" {
		capabilityService.Register(service.NewQwenAudioAdapter(service.QwenAudioAdapterConfig{
			Endpoint:     cfg.MultiModal.AudioEndpoint,
			APIKey:       cfg.MultiModal.AudioAPIKey,
			ProviderName: "qwen2.5-omni",
			Timeout:      cfg.MultiModal.AITimeout,
			Reader:       assetReader,
			DB:        dbRepo,
		}))
		log.Printf("[capability] registered Qwen2.5-Omni audio adapter (endpoint=%s)", cfg.MultiModal.AudioEndpoint)
	}

	// RouterService + AnnotationTaskService + downstream services.
	// Phase 1.5: when MM_OCR_ENDPOINT is configured, wire the cheap OCR
	// detection probe so the L1 router populates box_count and
	// text_area_ratio from real OCR boxes instead of zero (plan_v1/06
	// §11.0 known gap). Probe failure is non-fatal — featuresFromAsset
	// logs and continues with metadata-only features.
	routerService := service.NewRouterService(dbRepo, capabilityService, service.DefaultRoutingDefaults()).WithCache(redisCache)
	if cfg.MultiModal.OCREndpoint != "" {
		if probe := service.NewHTTPOCRDetProbe(service.OCRDetProbeConfig{
			Endpoint: cfg.MultiModal.OCREndpoint,
			APIKey:   cfg.MultiModal.OCRAPIKey,
			Timeout:  cfg.MultiModal.AITimeout,
			Reader:   assetReader,
		}); probe != nil {
			routerService.WithOCRDetProbe(probe)
			log.Printf("[router] L1 OCR det probe enabled (endpoint=%s)", cfg.MultiModal.OCREndpoint)
		}
	}
	if cfg.MultiModal.SigLIPEndpoint != "" {
		if probe := service.NewHTTPSigLIPProbe(service.SigLIPProbeConfig{
			Endpoint: cfg.MultiModal.SigLIPEndpoint,
			APIKey:   cfg.MultiModal.SigLIPAPIKey,
			Timeout:  cfg.MultiModal.AITimeout,
			Reader:   assetReader,
		}); probe != nil {
			routerService.WithSigLIPProbe(probe)
			log.Printf("[router] L2 SigLIP2 semantic probe enabled (endpoint=%s)", cfg.MultiModal.SigLIPEndpoint)
		}
	}
	annotationTaskService := service.NewAnnotationTaskService(dbRepo).WithCache(redisCache)
	assetService.BindTaskService(annotationTaskService)
	humanAnnotationService := service.NewHumanAnnotationService(dbRepo, dbRepo)
	finalAnnotationService := service.NewFinalAnnotationService(dbRepo, dbRepo).WithCache(redisCache)
	qaService := service.NewQAService(dbRepo, dbRepo, finalAnnotationService)
	traceService := service.NewTraceService(dbRepo).WithCache(redisCache)
	adhocInvocationService := service.NewAdHocInvocationService(dbRepo, dbRepo, capabilityService, cfg.MultiModal.AITimeout).WithCache(redisCache)
	imageExportService := service.NewImageExportService(dbRepo, dbRepo)

	// AIWorker. Started below after all services are wired.
	aiWorker := service.NewAIWorker(service.AIWorkerConfig{
		Interval:        cfg.MultiModal.WorkerInterval,
		BatchSize:       cfg.MultiModal.WorkerBatch,
		Concurrency:     cfg.MultiModal.WorkerConcurrency,
		LeaseTTL:        cfg.MultiModal.WorkerLeaseTTL,
		MaxRetries:      cfg.MultiModal.AIRetryMax,
		InvokeTimeout:   cfg.MultiModal.AITimeout,
		RetryMaxBackoff: cfg.MultiModal.AIWorkerRetryMaxBackoff,
	}, dbRepo, dbRepo, routerService, capabilityService).WithCache(redisCache)

	// T0.3: derived-asset pipeline (waveform peaks / frame index / thumbnails).
	mediaCfg := service.DefaultMediaWorkerConfig()
	mediaCfg.Enabled = cfg.MultiModal.MediaWorkerEnabled
	mediaWorker := service.NewMediaWorker(mediaCfg, dbRepo, assetStore,
		service.NewMediaTools(cfg.MultiModal.FFmpegPath, cfg.MultiModal.FFprobePath))

	// Initialize plugin registries and register built-in plugins
	plugin.InitRegistries()
	registerBuiltinPlugins()

	// --- Services ---
	authService := service.NewAuthService(dbRepo, cfg.JWTSecret)
	auditService := service.NewAuditService(dbRepo)
	// WithAssetService：删数据集时逐资产清 blob/派生物/载荷表标注行（M7）；
	// 剩余关系行由外键级联。runner（纯文本）模式没有资产栈，不注入。
	compensationHandler := service.NewCompensationHandler(dbRepo, docDB, auditService).WithAssetService(assetService)
	datasetService := service.NewDatasetService(dbRepo, docDB).WithCache(redisCache)
	// B2.8 成本闸门：数据集若开启 trigger=auto，视频任务在建任务时就排给 AI worker。
	// 加载失败时回落到默认（manual）——宁可不自动跑，也不要因为一次读库失败去打 GPU。
	annotationTaskService.WithVideoAIConfig(func(ctx context.Context, datasetID uint) service.VideoAIConfig {
		vcfg, err := datasetService.GetVideoAIConfig(ctx, datasetID)
		if err != nil {
			return service.DefaultVideoAIConfig()
		}
		return vcfg
	})
	dashboardService := service.NewDashboardService(dbRepo, docDB, cfg.DemoMode, cfg.DashboardCacheTTL).
		WithCache(redisCache)
	documentService := service.NewDocumentService(dbRepo, docDB, plugin.ImportRegistry, dashboardService).WithCache(redisCache)
	llmService := service.NewLLMService(dbRepo, plugin.TaskRegistry, capabilityService, service.LLMServiceConfig{
		MinProviderTimeout: cfg.LLM.MinProviderTimeout,
		RetryMaxBackoff:    cfg.LLM.MaxBackoff,
	})
	samplingService := service.NewSamplingService(docDB, plugin.SamplingRegistry)
	exportService := service.NewExportService(docDB, dbRepo, plugin.ExportRegistry)
	systemPromptService := service.NewSystemPromptService(dbRepo).WithCache(redisCache)
	autoAnnotationService := service.NewAutoAnnotationService(capabilityService, systemPromptService, docDB, dbRepo).
		WithCache(redisCache)
	refinementService := service.NewRefinementService(docDB, dbRepo, dashboardService).WithCache(redisCache)
	textCandidateService := service.NewTextCandidateService(capabilityService, systemPromptService, dbRepo, docDB, dbRepo, dashboardService)
	extractionService := service.NewExtractionService(plugin.ExtractionRegistry, docDB, dbRepo)
	datasetFunctionService := service.NewDatasetFunctionService(dbRepo)
	llmRefinementService := service.NewLLMRefinementService(docDB, dbRepo, capabilityService)
	userService := service.NewUserService(dbRepo).WithCache(redisCache)

	// Reload LLM adapters from DB (text.chat providers).
	if err := llmService.ReloadAdapters(context.Background()); err != nil {
		log.Printf("Warning: failed to reload LLM adapters: %v", err)
	}

	// Register TextLLMAdapter so text.chat calls route through CapabilityService
	// for unified trace logging. The adapter delegates to llmService, so future
	// ReloadAdapters() calls automatically pick up provider changes.
	capabilityService.Register(service.NewTextLLMAdapter(llmService, dbRepo))
	log.Printf("[capability] registered TextLLMAdapter (text.chat)")

	// Phase 2: CapabilityConfigService manages vlm/ocr/seg providers via DB.
	// On startup it loads any providers persisted via the UI and registers
	// them with CapabilityService, overwriting env-based adapters of the same
	// capability_type (DB wins over env when both are present).
	capabilityConfigService := service.NewCapabilityConfigService(
		dbRepo,
		capabilityService,
		llmService,
		assetReader,
		cfg.MultiModal.AITimeout,
	)
	if err := capabilityConfigService.ReloadAdapters(context.Background()); err != nil {
		log.Printf("[capability-config] startup reload: %v", err)
	}

	// Record env-sourced adapters so the management center can display them.
	if cfg.MultiModal.LiteLLMEndpoint != "" {
		modelName := os.Getenv("MM_VLM_MODEL")
		if modelName == "" {
			modelName = "qwen-vl"
		}
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityVLMStructured,
			ProviderName:   "LiteLLM (env)",
			ProviderKind:   "litellm",
			Endpoint:       cfg.MultiModal.LiteLLMEndpoint,
			Model:          modelName,
		})
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityVLMCaption,
			ProviderName:   "LiteLLM (env)",
			ProviderKind:   "litellm",
			Endpoint:       cfg.MultiModal.LiteLLMEndpoint,
			Model:          modelName,
		})
	}
	if cfg.MultiModal.OCREndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityOCRStructure,
			ProviderName:   "PaddleOCR (env)",
			ProviderKind:   "paddlex",
			Endpoint:       cfg.MultiModal.OCREndpoint,
		})
	}
	if cfg.MultiModal.SegEndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilitySegInstance,
			ProviderName:   "YOLOv8-seg (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.SegEndpoint,
		})
	}
	if cfg.MultiModal.SAMEndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilitySegInteractive,
			ProviderName:   "MobileSAM (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.SAMEndpoint,
		})
	}
	if cfg.MultiModal.SigLIPEndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilitySemanticRouter,
			ProviderName:   "SigLIP2 (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.SigLIPEndpoint,
		})
	}
	if cfg.MultiModal.ASREndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityASRTranscribe,
			ProviderName:   "FunASR (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.ASREndpoint,
			Model:          "paraformer-zh",
		})
	}
	if cfg.MultiModal.DetEndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityVideoDetectTrack,
			ProviderName:   "det-server (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.DetEndpoint,
			Model:          "YOLO26x + ByteTrack/BoT-SORT",
		})
	}
	if cfg.MultiModal.SAM2Endpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityVideoSAM2Propagate,
			ProviderName:   "sam2-video (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.SAM2Endpoint,
			Model:          "sam2.1_hiera_base_plus",
		})
	}
	if cfg.MultiModal.AudioEndpoint != "" {
		capabilityConfigService.RegisterEnvAdapter(service.EnvAdapterSnapshot{
			CapabilityType: service.CapabilityAudioTranscribe,
			ProviderName:   "qwen-audio (env)",
			ProviderKind:   "http",
			Endpoint:       cfg.MultiModal.AudioEndpoint,
			Model:          "Qwen2.5-Omni-7B",
		})
	}

	// --- V1 Handlers ---
	authHandler := api.NewAuthHandler(authService)
	categoryHandler := api.NewDatasetCategoryHandler(datasetService)
	tagHandler := api.NewTagHandler(datasetService)
	datasetHandler := api.NewDatasetHandler(datasetService, compensationHandler)
	documentHandler := api.NewDocumentHandler(documentService, compensationHandler, plugin.ImportRegistry)
	llmHandler := api.NewLLMHandler(llmService)
	exportHandler := api.NewExportHandler(exportService)
	samplingHandler := api.NewSamplingHandler(samplingService)
	auditHandler := api.NewAuditHandler(auditService)

	// --- Annotation-workflow handlers (prompts, refinement, dashboard, extraction) ---
	systemPromptHandler := api.NewSystemPromptHandler(systemPromptService)
	autoAnnotateHandler := api.NewAutoAnnotateHandler(autoAnnotationService)
	textCandidateHandler := api.NewTextCandidateHandler(textCandidateService)
	refinementHandler := api.NewRefinementHandler(refinementService)
	dashboardHandler := api.NewDashboardHandler(dashboardService)
	extractionHandler := api.NewExtractionHandler(extractionService, plugin.ExtractionRegistry)
	datasetFunctionHandler := api.NewDatasetFunctionHandler(datasetFunctionService)
	llmRefinementHandler := api.NewLLMRefinementHandler(llmRefinementService)
	userHandler := api.NewUserHandler(userService, authService) // Add user handler

	// --- Phase 2: Capability Config Handler ---
	capabilityConfigHandler := api.NewCapabilityConfigHandler(capabilityConfigService, cfg.MultiModal.LiteLLMConfigPath, cfg.MultiModal.DefaultProviderTimeoutSeconds)

	// --- Multi-modal P0 Handlers (TD-05: split from MultiModalHandlers god object) ---
	assetHandler := api.NewAssetHandler(assetService, annotationTaskService)
	taskHandler := api.NewTaskHandler(assetService, annotationTaskService)
	annotationHandler := api.NewAnnotationHandler(
		annotationTaskService, assetService, humanAnnotationService,
		qaService, finalAnnotationService, capabilityService,
	)
	aiResultHandler := api.NewAIResultHandler(dbRepo, traceService, capabilityService, adhocInvocationService)
	imageExportHandler := api.NewImageExportHandler(dbRepo, imageExportService)
	audioExportHandler := api.NewAudioExportHandler(service.NewAudioExportService(dbRepo, dbRepo)) // A3.3
	videoExportHandler := api.NewVideoExportHandler(service.NewVideoExportService(dbRepo, dbRepo)) // B3.3
	trackHandler := api.NewTrackHandler(service.NewTrackService(dbRepo, dbRepo).WithCapability(capabilityService)) // B1.0 tracks + B2 detect_track trigger
	reviewCommentHandler := api.NewReviewCommentHandler(service.NewReviewCommentService(dbRepo, dbRepo))          // B3.1 anchored review comments
	editLockHandler := api.NewEditLockHandler(service.NewEditLockService(redisCache))                    // T0.4
	multipartHandler := api.NewMultipartHandler(multipartService)                                        // T0.2
	batchAnnotateSvc := service.NewBatchAnnotateService(adhocInvocationService, dbRepo)               // item 4 (DB-backed, item 3)
	batchAnnotateSvc.ReconcileOnStartup(context.Background())                                            // orphaned running → interrupted
	batchAnnotateHandler := api.NewBatchAnnotateHandler(batchAnnotateSvc)
	_ = routerService // wired by AIWorker; retained as dependency root

	// Start the AIWorker (P0 ADR-08). Disabled when explicitly turned off.
	// AIWorker 启动；优雅停机统一在 main 末尾处理（HTTP 先停、再排空 worker）。
	if cfg.MultiModal.WorkerEnabled {
		aiWorker.Start(context.Background())
	} else {
		log.Printf("[ai_worker] disabled by configuration (MM_WORKER_ENABLED=false)")
		_ = aiWorker
	}
	// T0.3: start the media-worker (derived-asset pipeline).
	if cfg.MultiModal.MediaWorkerEnabled {
		mediaWorker.Start(context.Background())
	} else {
		log.Printf("[media_worker] disabled by configuration (MM_MEDIA_WORKER_ENABLED=false)")
		_ = mediaWorker
	}

	// Set up Gin router and register the full route surface via the shared
	// internal/server package (see TD-16). The Deps struct bundles every
	// handler; multi-modal fields stay non-nil here so the full stack is
	// registered.
	// PH-6：统一日志为 JSON（与 worker slog 单轨）；gin.New 去掉内置文本 Logger，
	// 改用自定义 JSON 请求日志 + Recovery + Prometheus 指标 + /metrics 端点。
	slog.SetDefault(logger.New())
	if os.Getenv("APP_ENV") == "prod" {
		gin.SetMode(gin.ReleaseMode) // 静默 [GIN-debug] 启动噪音，单轨 JSON 日志
	}
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.MetricsMiddleware())
	r.Use(middleware.RequestLogger())
	r.GET("/metrics", middleware.MetricsHandler())
	var redisProbe func(ctx context.Context) error
	if redisCache != nil {
		redisProbe = redisCache.Ping
	}
	// PH-4：关键依赖探针（/readyz 用）。
	dbProbe := func(ctx context.Context) error {
		sqlDB, err := dbRepo.DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
	server.RegisterRoutes(r, server.Deps{
		AuthService:             authService,
		AllowedOrigins:          cfg.AllowedOrigins,
		RedisProbe:              redisProbe,
		DBProbe:              dbProbe,
		AuthHandler:             authHandler,
		CategoryHandler:         categoryHandler,
		TagHandler:              tagHandler,
		DatasetHandler:          datasetHandler,
		DocumentHandler:         documentHandler,
		LLMHandler:              llmHandler,
		ExportHandler:           exportHandler,
		SamplingHandler:         samplingHandler,
		AuditHandler:            auditHandler,
		SystemPromptHandler:     systemPromptHandler,
		AutoAnnotateHandler:     autoAnnotateHandler,
		TextCandidateHandler:    textCandidateHandler,
		RefinementHandler:       refinementHandler,
		DashboardHandler:        dashboardHandler,
		ExtractionHandler:       extractionHandler,
		DatasetFunctionHandler:  datasetFunctionHandler,
		LLMRefinementHandler:    llmRefinementHandler,
		UserHandler:             userHandler,
		CapabilityConfigHandler: capabilityConfigHandler,
		AssetHandler:            assetHandler,
		TaskHandler:             taskHandler,
		AnnotationHandler:       annotationHandler,
		AIResultHandler:         aiResultHandler,
		ImageExportHandler:      imageExportHandler,
		AudioExportHandler:      audioExportHandler,
		VideoExportHandler:      videoExportHandler,
		TrackHandler:            trackHandler,
		ReviewCommentHandler:    reviewCommentHandler,
		EditLockHandler:         editLockHandler,
		MultipartHandler:        multipartHandler,
		BatchAnnotateHandler:    batchAnnotateHandler,
	})

	// Create default admin user if not exists
	createDefaultUser(dbRepo)
	seedDefaultData(dbRepo)

	// Wire Redis into the rate-limit middleware (distributed sliding window).
	middleware.InitRedisLimiter(redisClient)

	// Cache warmup: pre-populate high-TTL keys so the first real requests hit Redis.
	if redisCache != nil {
		wctx := context.Background()
		if _, err := datasetService.ListCategories(wctx); err == nil {
			log.Printf("[cache] warmed up categories:all")
		}
		if _, err := datasetService.ListTags(wctx, "dataset"); err == nil {
			log.Printf("[cache] warmed up tags:dataset")
		}
	}

	// PH-3：带超时的 http.Server + 统一优雅停机。
	// 仅设 ReadHeaderTimeout（防 Slowloris）+ IdleTimeout（清理 keep-alive）。
	// ReadTimeout/WriteTimeout 故意留空：大文件上传（图片，未来音视频）、长耗时同步
	// AI 调用、流式下载会被它们截断；待 PH-1 把媒体字节移出应用进程后再按路由细化。
	srv := &http.Server{
		Addr:              cfg.ServerPort,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("Starting server on %s", cfg.ServerPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// 收到 SIGINT/SIGTERM：先停 HTTP 新连接并排空在途请求，再排空 AIWorker（LLM 调用
	// 可达数十秒），最后关 Redis。整体预算 40s（覆盖 worker 的 30s drain）。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("shutdown signal received; stopping HTTP then draining worker…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[http] graceful shutdown deadline exceeded: %v", err)
	} else {
		log.Printf("[http] stopped accepting; in-flight requests drained")
	}
	if cfg.MultiModal.WorkerEnabled {
		if err := aiWorker.Stop(shutdownCtx); err != nil {
			log.Printf("[ai_worker] graceful stop deadline exceeded: %v", err)
		} else {
			log.Printf("[ai_worker] drained cleanly")
		}
	}
	if cfg.MultiModal.MediaWorkerEnabled {
		if err := mediaWorker.Stop(shutdownCtx); err != nil {
			log.Printf("[media_worker] graceful stop deadline exceeded: %v", err)
		} else {
			log.Printf("[media_worker] drained cleanly")
		}
	}
	if redisClient != nil {
		_ = redisClient.Close()
		log.Printf("[redis] connection closed")
	}
	log.Printf("shutdown complete")
}

func createDefaultUser(dbRepo *repository.DB) {
	_, err := dbRepo.FindUserByUsername(context.Background(), "admin")
	if err == nil {
		return
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("Failed to hash default password: %v", err)
	}
	user := dbmodel.User{
		Username:     "admin",
		PasswordHash: string(hashedPassword),
		Role:         "admin",
		DisplayName:  "系统管理员",
		Status:       "active",
	}
	if err := dbRepo.DB.Create(&user).Error; err != nil {
		log.Fatalf("Failed to create default admin user: %v", err)
	}
	log.Printf("Created default admin user: admin/admin123")
}

func registerBuiltinPlugins() {
	must(plugin.ImportRegistry.Register("json", plugin.ImportPlugin(&builtin.JSONImportPlugin{})))
	must(plugin.ImportRegistry.Register("csv", plugin.ImportPlugin(&builtin.CSVImportPlugin{})))
	must(plugin.ExportRegistry.Register("json", plugin.ExportPlugin(&builtin.JSONExportPlugin{})))
	must(plugin.ExportRegistry.Register("jsonl", plugin.ExportPlugin(&builtin.JSONLExportPlugin{})))
	must(plugin.ExportRegistry.Register("csv", plugin.ExportPlugin(&builtin.CSVExportPlugin{})))
	must(plugin.TaskRegistry.Register("qa_generation", plugin.TaskPlugin(&builtin.QAGenerationTaskPlugin{})))
	must(plugin.SamplingRegistry.Register("random", plugin.SamplingStrategy(&builtin.RandomSamplingStrategy{})))
	must(plugin.SamplingRegistry.Register("rule", plugin.SamplingStrategy(&builtin.RuleSamplingStrategy{})))
	// Extraction filters
	must(plugin.ExtractionRegistry.Register("ratio", plugin.ExtractionFilter(&builtin.RatioFilter{})))
	must(plugin.ExtractionRegistry.Register("import_time", plugin.ExtractionFilter(&builtin.ImportTimeFilter{})))
	must(plugin.ExtractionRegistry.Register("case_occurrence_time", plugin.ExtractionFilter(&builtin.CaseTimeFilter{})))
	must(plugin.ExtractionRegistry.Register("judgment_time", plugin.ExtractionFilter(&builtin.JudgmentTimeFilter{})))
	must(plugin.ExtractionRegistry.Register("keyword", plugin.ExtractionFilter(&builtin.KeywordFilter{})))
}

func must(err error) {
	if err != nil {
		log.Fatalf("failed to register plugin: %v", err)
	}
}

func seedDefaultData(dbRepo *repository.DB) {
	var catCount int64
	dbRepo.DB.Model(&dbmodel.DatasetCategory{}).Count(&catCount)
	if catCount == 0 {
		for _, cat := range []dbmodel.DatasetCategory{
			{Name: "刑事案件", Description: "刑事案件相关文本"},
			{Name: "民事案件", Description: "民事案件相关文本"},
			{Name: "行政案件", Description: "行政案件相关文本"},
		} {
			dbRepo.DB.Create(&cat)
		}
		log.Println("Default categories created")
	}

	var tagCount int64
	dbRepo.DB.Model(&dbmodel.Tag{}).Count(&tagCount)
	if tagCount == 0 {
		for _, tag := range []dbmodel.Tag{
			{Name: "已审核", Color: "#000000"},
			{Name: "待审核", Color: "#000000"},
			{Name: "重要", Color: "#000000"},
		} {
			dbRepo.DB.Create(&tag)
		}
		log.Println("Default tags created")
	}

	var promptCount int64
	dbRepo.DB.Model(&dbmodel.SystemPrompt{}).Count(&promptCount)
	if promptCount == 0 {
		for _, p := range []dbmodel.SystemPrompt{
			{CaseType: "criminal", Name: "刑事案件", Content: "你是一个专业的刑事案件法律文书标注助手。请根据以下刑事案件文书内容，生成高质量的问答对。重点关注以下法律要素：犯罪事实（时间、地点、手段、后果）、被告人信息、罪名认定、量刑情节（自首、立功、累犯等）、证据采信、法律适用和判决结果。\n\n请以JSON数组格式输出问答对，每个问答对包含 question 和 answer 字段。问题应具体明确，答案应准确引用原文内容。"},
			{CaseType: "civil", Name: "民事案件", Content: "你是一个专业的民事案件法律文书标注助手。请根据以下民事案件文书内容，生成高质量的问答对。重点关注以下法律要素：当事人信息及法律关系、诉讼请求、事实与理由、争议焦点、证据认定、法律适用（合同法、侵权责任法等）、判决主文和诉讼费用。\n\n请以JSON数组格式输出问答对，每个问答对包含 question 和 answer 字段。问题应具体明确，答案应准确引用原文内容。"},
			{CaseType: "administrative", Name: "行政案件", Content: "你是一个专业的行政案件法律文书标注助手。请根据以下行政案件文书内容，生成高质量的问答对。重点关注以下法律要素：行政机关及行政行为、行政相对人、行政行为的事实依据、法律依据、行政程序合法性、行政行为合理性、司法审查标准和判决结果。\n\n请以JSON数组格式输出问答对，每个问答对包含 question 和 answer 字段。问题应具体明确，答案应准确引用原文内容。"},
		} {
			dbRepo.DB.Create(&p)
		}
		log.Println("Default system prompts created")
	}

	var autoPromptCount int64
	dbRepo.DB.Model(&dbmodel.AutoPromptTemplate{}).Count(&autoPromptCount)
	if autoPromptCount == 0 {
		var prompts []dbmodel.SystemPrompt
		dbRepo.DB.Order("case_type").Find(&prompts)
		for _, p := range prompts {
			dbRepo.DB.Create(&dbmodel.AutoPromptTemplate{
				Name:         p.Name + "自动标注 QA",
				CaseType:     p.CaseType,
				TaskType:     service.AutoPromptTaskTextQA,
				SystemPrompt: p.Content,
				UserPromptTemplate: `请基于以下正文生成高质量问答对，仅返回 JSON 数组。

要求：
1. 每个对象必须包含 question_key、category、question、answer、evidence、span_text、confidence、reason 字段。
2. question_key 必须从以下固定枚举中选择；同一个 question_key 的 question 必须严格使用对应中文问题，不得改写。
3. 正文中没有依据的 question_key 可以省略，不要编造。
4. evidence / span_text 必须来自原文，可用于人工复核。

固定问题：
- parties: 当事人及其身份信息是什么？
- claims: 主要诉讼请求、指控或处理请求是什么？
- facts: 案件基本事实是什么？
- issues: 争议焦点、审查重点或待证明问题是什么？
- evidence: 关键证据及采信情况是什么？
- law: 适用的法律依据是什么？
- judgment: 裁判结果、处理结果或结论是什么？

正文：
{{text}}`,
				OutputSchema: `{"type":"array","items":{"type":"object","properties":{"question_key":{"type":"string"},"category":{"type":"string"},"question":{"type":"string"},"answer":{"type":"string"},"evidence":{"type":"string"},"span_text":{"type":"string"},"confidence":{"type":"number"},"reason":{"type":"string"}},"required":["question_key","category","question","answer","evidence"]}}`,
				Guide:        "必须包含 {{text}}；建议使用固定 question_key 枚举，并要求 question_key 对应的 question 原样输出。evidence/span_text 用于系统回原文定位与人工复核。",
				Enabled:      true,
				Version:      1,
			})
		}
		log.Println("Default auto prompt templates created")
	}
	if err := service.NewSystemPromptService(dbRepo).EnsureDefaultJudgePromptTemplates(context.Background()); err != nil {
		log.Printf("Warning: failed to ensure Judge Agent prompt templates: %v", err)
	} else {
		log.Println("Default Judge Agent prompt templates ensured")
	}

	var funcCount int64
	dbRepo.DB.Model(&dbmodel.DatasetFunction{}).Count(&funcCount)
	if funcCount == 0 {
		for _, f := range []dbmodel.DatasetFunction{
			{Name: "预训练标注", Description: "支持两层标注工作流（自动标注 + 人工精标），右侧显示完整的问答对列表", WorkflowConfig: `{"layers": ["auto", "manual"], "right_panel": "qa_list"}`, SortOrder: 1},
			{Name: "定向任务微调", Description: "仅支持人工标注模式，右侧显示任务特定的标注表单", WorkflowConfig: `{"layers": ["manual"], "right_panel": "task_form"}`, SortOrder: 2},
		} {
			dbRepo.DB.Create(&f)
		}
		log.Println("Default dataset functions created")
	}
}
