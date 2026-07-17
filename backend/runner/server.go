package runner

import (
	"context"
	"log"

	"text-annotation-platform/config"
	"text-annotation-platform/internal/api"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/plugin/builtin"
	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/server"
	"text-annotation-platform/internal/service"
	"text-annotation-platform/internal/util"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// RunServer initializes and starts the backend API server.
// It is extracted from cmd/main.go so that it can be embedded in the Wails app.
func RunServer(ctx context.Context) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Printf("configuration error: %v\n", err)
		return err
	}
	if err := util.SetAppLocation(cfg.Timezone); err != nil {
		log.Printf("warn: failed to load timezone %q: %v (falling back to default)", cfg.Timezone, err)
	}

	dbRepo, err := repository.NewDB(cfg.DatabaseURL)
	if err != nil {
		log.Printf("Failed to connect to the database: %v\n", err)
		return err
	}

	// Documents live in the relational documents table (方案-07 起主部署亦然).
	// The RelationalDocRepo reuses the same GORM connection.
	docDB := repository.NewRelationalDocRepo(dbRepo.DB)
	// EnsureIndexes is a no-op (schema 由 goose 迁移建出)，保留调用以兑现接口契约。
	if err := docDB.EnsureIndexes(context.Background()); err != nil {
		log.Printf("Failed to auto-migrate document table: %v\n", err)
		return err
	}
	log.Println("Using the relational DB as document store (standalone mode)")

	// Initialize plugin registries and register built-in plugins
	plugin.InitRegistries()
	registerBuiltinPlugins()

	// --- Services ---
	capabilityService := service.NewCapabilityService(nil)
	authService := service.NewAuthService(dbRepo, cfg.JWTSecret)
	auditService := service.NewAuditService(dbRepo)
	compensationHandler := service.NewCompensationHandler(dbRepo, docDB, auditService)
	datasetService := service.NewDatasetService(dbRepo, docDB)
	dashboardService := service.NewDashboardService(dbRepo, docDB, cfg.DemoMode, cfg.DashboardCacheTTL)
	documentService := service.NewDocumentService(dbRepo, docDB, plugin.ImportRegistry, dashboardService)
	llmService := service.NewLLMService(dbRepo, plugin.TaskRegistry, capabilityService, service.LLMServiceConfig{
		MinProviderTimeout: cfg.LLM.MinProviderTimeout,
		RetryMaxBackoff:    cfg.LLM.MaxBackoff,
	})
	samplingService := service.NewSamplingService(docDB, plugin.SamplingRegistry)
	exportService := service.NewExportService(docDB, dbRepo, plugin.ExportRegistry)
	systemPromptService := service.NewSystemPromptService(dbRepo)
	autoAnnotationService := service.NewAutoAnnotationService(capabilityService, systemPromptService, docDB, dbRepo)
	refinementService := service.NewRefinementService(docDB, dbRepo, dashboardService)
	extractionService := service.NewExtractionService(plugin.ExtractionRegistry, docDB, dbRepo)
	datasetFunctionService := service.NewDatasetFunctionService(dbRepo)
	llmRefinementService := service.NewLLMRefinementService(docDB, dbRepo, capabilityService)

	// Reload LLM adapters from DB and wire TextLLMAdapter for unified trace logging.
	if err := llmService.ReloadAdapters(context.Background()); err != nil {
		log.Printf("Warning: failed to reload LLM adapters: %v", err)
	}
	capabilityService.Register(service.NewTextLLMAdapter(llmService, dbRepo))

	// --- Handlers ---
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
	refinementHandler := api.NewRefinementHandler(refinementService)
	dashboardHandler := api.NewDashboardHandler(dashboardService)
	extractionHandler := api.NewExtractionHandler(extractionService, plugin.ExtractionRegistry)
	datasetFunctionHandler := api.NewDatasetFunctionHandler(datasetFunctionService)
	llmRefinementHandler := api.NewLLMRefinementHandler(llmRefinementService)

	// Set up Gin router and register the V1 route surface via the shared
	// internal/server package (see TD-16). Multi-modal handler fields are
	// intentionally left nil — runner mode is the text-only embedded
	// build and does not wire AIWorker, AssetService, etc.
	r := gin.Default()
	server.RegisterRoutes(r, server.Deps{
		AuthService:            authService,
		AllowedOrigins:         cfg.AllowedOrigins,
		AuthHandler:            authHandler,
		CategoryHandler:        categoryHandler,
		TagHandler:             tagHandler,
		DatasetHandler:         datasetHandler,
		DocumentHandler:        documentHandler,
		LLMHandler:             llmHandler,
		ExportHandler:          exportHandler,
		SamplingHandler:        samplingHandler,
		AuditHandler:           auditHandler,
		SystemPromptHandler:    systemPromptHandler,
		AutoAnnotateHandler:    autoAnnotateHandler,
		RefinementHandler:      refinementHandler,
		DashboardHandler:       dashboardHandler,
		ExtractionHandler:      extractionHandler,
		DatasetFunctionHandler: datasetFunctionHandler,
		LLMRefinementHandler:   llmRefinementHandler,
		// Multi-modal + user mgmt + capability config left nil — runner mode
		// only exposes the V1 text pipeline.
	})

	// Create default admin user if not exists
	createDefaultUser(dbRepo)
	seedDefaultData(dbRepo)

	log.Printf("Starting embedded backend server on %s", cfg.ServerPort)

	serverErrChan := make(chan error, 1)
	go func() {
		serverErrChan <- r.Run(cfg.ServerPort)
	}()

	select {
	case <-ctx.Done():
		log.Println("Context cancelled, shutting down Server HTTP API gracefully...")
		return nil
	case err := <-serverErrChan:
		return err
	}
}

func createDefaultUser(dbRepo *repository.DB) {
	_, err := dbRepo.FindUserByUsername(context.Background(), "admin")
	if err == nil {
		return
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("Failed to hash default password: %v", err)
		return
	}
	user := dbmodel.User{Username: "admin", PasswordHash: string(hashedPassword), Role: "admin"}
	if err := dbRepo.DB.Create(&user).Error; err != nil {
		log.Printf("Failed to create default admin user: %v", err)
	}
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
	must(plugin.ExtractionRegistry.Register("ratio", plugin.ExtractionFilter(&builtin.RatioFilter{})))
	must(plugin.ExtractionRegistry.Register("import_time", plugin.ExtractionFilter(&builtin.ImportTimeFilter{})))
	must(plugin.ExtractionRegistry.Register("case_occurrence_time", plugin.ExtractionFilter(&builtin.CaseTimeFilter{})))
	must(plugin.ExtractionRegistry.Register("judgment_time", plugin.ExtractionFilter(&builtin.JudgmentTimeFilter{})))
	must(plugin.ExtractionRegistry.Register("keyword", plugin.ExtractionFilter(&builtin.KeywordFilter{})))
}

func must(err error) {
	if err != nil {
		log.Printf("failed to register plugin: %v", err)
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
	}

	var tagCount int64
	dbRepo.DB.Model(&dbmodel.Tag{}).Count(&tagCount)
	if tagCount == 0 {
		for _, tag := range []dbmodel.Tag{
			{Name: "高优", Color: "#ff0000"},
			{Name: "待标", Color: "#00ff00"},
			{Name: "疑难", Color: "#0000ff"},
		} {
			dbRepo.DB.Create(&tag)
		}
	}
}
