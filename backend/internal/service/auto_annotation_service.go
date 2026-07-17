package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// cancelTTL is how long a Redis cancel signal lives; long enough to survive any
// in-flight LLM call but short enough to not pollute a restarted run.
const cancelTTL = 5 * time.Minute

// localCancelKeys is the fallback when Redis is unavailable (dev / no REDIS_URL).
var localCancelKeys sync.Map

// Annotation stage constants
const (
	StageNotAnnotated   = "not_annotated"
	StageAutoAnnotating = "auto_annotating"
	StageAutoAnnotated  = "auto_annotated"
	StageAutoFailed     = "auto_failed"
	StageRefining       = "refining"
	StageRefined        = "refined"
)

// validTransitions defines the allowed state transitions.
var validTransitions = map[string][]string{
	StageNotAnnotated:   {StageAutoAnnotating, StageRefined, StageRefining},
	StageAutoAnnotating: {StageAutoAnnotated, StageAutoFailed},
	StageAutoFailed:     {StageAutoAnnotating, StageRefined, StageRefining},
	StageAutoAnnotated:  {StageRefining, StageAutoAnnotating, StageRefined},
	StageRefining:       {StageRefined},
	StageRefined:        {StageRefining, StageAutoAnnotating},
}

// AllStages lists every known annotation stage.
var AllStages = []string{
	StageNotAnnotated,
	StageAutoAnnotating,
	StageAutoAnnotated,
	StageAutoFailed,
	StageRefining,
	StageRefined,
}

// ValidateStageTransition checks whether a transition from current to target is allowed.
func ValidateStageTransition(current, target string) error {
	allowed, ok := validTransitions[current]
	if !ok {
		return fmt.Errorf("未知的标注阶�? %s", current)
	}
	for _, a := range allowed {
		if a == target {
			return nil
		}
	}
	return fmt.Errorf("不允许从 '%s' 转换�?'%s'", current, target)
}

// AutoAnnotationService orchestrates the first-layer auto-annotation workflow.
type AutoAnnotationService struct {
	capSvc        *CapabilityService
	promptService *SystemPromptService
	docRepo     repository.DocumentDB
	dbRepo        *repository.DB
	cache         *cache.Cache // nil = fallback to localCancelKeys (dev/no-Redis)
}

// NewAutoAnnotationService creates a new AutoAnnotationService.
func NewAutoAnnotationService(
	capSvc *CapabilityService,
	promptService *SystemPromptService,
	docRepo repository.DocumentDB,
	dbRepo *repository.DB,
) *AutoAnnotationService {
	return &AutoAnnotationService{
		capSvc:        capSvc,
		promptService: promptService,
		docRepo:     docRepo,
		dbRepo:        dbRepo,
	}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *AutoAnnotationService) WithCache(c *cache.Cache) *AutoAnnotationService {
	s.cache = c
	return s
}

// cancelKey returns the Redis key for a cancel signal.
func cancelKey(docKey string) string { return "cancel:auto:" + docKey }

// storeCancelSignal writes a cancel signal for docKey (Redis or local fallback).
func (s *AutoAnnotationService) storeCancelSignal(ctx context.Context, docKey string) {
	if s.cache != nil {
		s.cache.SetJSON(ctx, cancelKey(docKey), true, cancelTTL)
	} else {
		localCancelKeys.Store(docKey, true)
	}
}

// isCancelled reports whether a cancel signal exists for docKey and removes it.
func (s *AutoAnnotationService) isCancelled(ctx context.Context, docKey string) bool {
	if s.cache != nil {
		if !s.cache.Exists(ctx, cancelKey(docKey)) {
			return false
		}
		s.cache.Delete(ctx, cancelKey(docKey))
		return true
	}
	if _, ok := localCancelKeys.LoadAndDelete(docKey); ok {
		return true
	}
	return false
}

// AutoAnnotateRequest describes a batch auto-annotation request.
type AutoAnnotateRequest struct {
	DatasetID  uint     `json:"dataset_id"`
	DocKeys    []string `json:"doc_keys"`
	ProviderID uint     `json:"provider_id"`
}

// AutoAnnotateResult holds the outcome for a single document.
type AutoAnnotateResult struct {
	DocKey string `json:"doc_key"`
	Stage  string `json:"stage"`
	Error  string `json:"error,omitempty"`
}

// AutoAnnotateStatus aggregates batch results.
type AutoAnnotateStatus struct {
	Completed int                  `json:"completed"`
	Failed    int                  `json:"failed"`
	Total     int                  `json:"total"`
	Results   []AutoAnnotateResult `json:"results"`
}

// CheckProviderConnectivity returns a warning message if the provider has not
// passed a connectivity test. Returns empty string when the provider is OK.
func (s *AutoAnnotationService) CheckProviderConnectivity(ctx context.Context, providerID uint) string {
	var provider dbmodel.LLMProvider
	if err := s.dbRepo.DB.First(&provider, providerID).Error; err != nil {
		return "所�?LLM 提供商不存在"
	}
	if provider.LastTestSuccess == nil || !*provider.LastTestSuccess {
		return "��ѡ LLM �ṩ��δͨ����ͨ�Բ��ԣ������Ȳ�������"
	}
	return ""
}

// AutoAnnotateDocuments processes each doc_key sequentially, collecting results.
// Partial failures do not stop the batch.
func (s *AutoAnnotationService) AutoAnnotateDocuments(ctx context.Context, req AutoAnnotateRequest) *AutoAnnotateStatus {
	slog.Debug("auto_annotate: batch starting", "doc_count", len(req.DocKeys))
	status := &AutoAnnotateStatus{
		Total:   len(req.DocKeys),
		Results: make([]AutoAnnotateResult, 0, len(req.DocKeys)),
	}

	for _, docKey := range req.DocKeys {
		slog.Debug("auto_annotate: processing doc", "doc_key", docKey)
		if s.isCancelled(ctx, docKey) {
			slog.Debug("auto_annotate: cancel intercepted at loop header", "doc_key", docKey)
			status.Failed++
			status.Results = append(status.Results, AutoAnnotateResult{
				DocKey: docKey,
				Stage:  StageAutoFailed,
				Error:  "�����û���ֹ / Terminated by user",
			})
			continue
		}

		err := s.autoAnnotateSingle(ctx, req.DatasetID, docKey, req.ProviderID)
		if err != nil {
			status.Failed++
			status.Results = append(status.Results, AutoAnnotateResult{
				DocKey: docKey,
				Stage:  StageAutoFailed,
				Error:  err.Error(),
			})
		} else {
			status.Completed++
			status.Results = append(status.Results, AutoAnnotateResult{
				DocKey: docKey,
				Stage:  StageAutoAnnotated,
			})
		}
	}
	// Recompute dataset counters once for the whole batch. Background context
	// ensures the sync completes even if the HTTP client disconnected.
	if err := s.dbRepo.SyncDatasetCounters(context.Background(), s.docRepo, req.DatasetID); err != nil {
		slog.Error("auto_annotate: sync dataset counters failed", "dataset_id", req.DatasetID, "error", err)
	}
	return status
}

// RangeAutoAnnotateDocuments fetches the documents within the given index range [startIdx, endIdx]
// and triggers auto-annotation on them sequentially.
func (s *AutoAnnotationService) RangeAutoAnnotateDocuments(
	ctx context.Context,
	datasetID uint,
	startIdx, endIdx int64,
	providerID uint,
) (*AutoAnnotateStatus, error) {
	if startIdx < 0 || endIdx < startIdx {
		return nil, fmt.Errorf("��Ч�ķ�Χ���� / invalid range parameters")
	}

	limit := endIdx - startIdx + 1
	docKeys, err := s.docRepo.FindDocKeysByRange(ctx, datasetID, startIdx, limit)
	if err != nil {
		return nil, err
	}
	if len(docKeys) == 0 {
		return &AutoAnnotateStatus{}, nil
	}

	req := AutoAnnotateRequest{
		DatasetID:  datasetID,
		DocKeys:    docKeys,
		ProviderID: providerID,
	}
	return s.AutoAnnotateDocuments(ctx, req), nil
}

// autoAnnotateSingle performs the full auto-annotation pipeline for one document:
// 1. Load document and validate stage
// 2. Transition to auto_annotating
// 3. Load system prompt by dataset case_type
// 4. Call LLM
// 5. Parse QA pairs from JSON response
// 6. Quality filter
// 7. Write qa_pairs to document data
// 8. Transition to auto_annotated (or auto_failed on error)
func (s *AutoAnnotationService) autoAnnotateSingle(ctx context.Context, datasetID uint, docKey string, providerID uint) error {
	// 1. Load document
	doc, err := s.docRepo.FindActiveDocument(ctx, &datasetID, docKey, 0)
	if err != nil {
		return fmt.Errorf("�����ĵ�ʧ��: %w", err)
	}
	if doc == nil {
		return fmt.Errorf("�ĵ�������: %s", docKey)
	}

	// Validate stage transition
	currentStage := doc.AnnotationStage
	if currentStage == "" {
		currentStage = StageNotAnnotated
	}
	if err := ValidateStageTransition(currentStage, StageAutoAnnotating); err != nil {
		return err
	}

	// 2. Set auto_annotating.
	// fire-and-forget: use Background ctx so the in-progress marker persists
	// even if the originating HTTP client disconnected — otherwise the doc
	// would appear stuck in not_annotated despite LLM work being in flight.
	if err := s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoAnnotating); err != nil {
		return fmt.Errorf("update doc stage failed: %w", err)
	}

	// 3. Load system prompt
	dataset, err := s.dbRepo.FindDatasetByID(ctx, datasetID)
	if err != nil {
		// fire-and-forget: failure-path stage marker, see top comment.
		s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoFailed)
		return fmt.Errorf("load dataset failed: %w", err)
	}
	prompt, err := s.promptService.GetOrDefault(ctx, dataset.CaseType)
	if err != nil {
		// fire-and-forget: failure-path stage marker, see top comment.
		s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoFailed)
		return fmt.Errorf("load system prompt failed: %w", err)
	}

	// Extract raw text from document data
	rawText := extractRawText(doc.Data)
	if rawText == "" {
		// fire-and-forget: failure-path stage marker, see top comment.
		s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoFailed)
		return fmt.Errorf("document content is empty")
	}

	// 4. Call LLM via CapabilityService (text.chat) for unified trace logging.
	capResp, err := s.capSvc.Invoke(ctx, CapabilityRequest{
		CapabilityType: CapabilityTextChat,
		Prompt:         rawText,
		Extras: map[string]interface{}{
			"system_prompt": prompt.Content,
			"provider_id":   providerID,
			"temperature":   0.0,
		},
	})
	if err != nil {
		// fire-and-forget: failure-path stage marker, see top comment.
		s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoFailed)
		return fmt.Errorf("LLM call failed: %w", err)
	}

	// 5. Parse QA pairs (includes quality filter)
	qaPairs := paymodel.ParseQAPairs(capResp.Text)
	if len(qaPairs) == 0 {
		// fire-and-forget: failure-path stage marker, see top comment.
		s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoFailed)
		return fmt.Errorf("LLM returned no valid QA pairs (parse or quality filter rejected all)")
	}

	// Mark all as LLM-generated
	for i := range qaPairs {
		qaPairs[i].Source = "llm"
		qaPairs[i].Confirmed = false
	}

	// Retrieve existing QA pairs to prevent overwriting
	var existingQAs []paymodel.QAPair
	if raw, ok := doc.Data["qa_pairs"]; ok {
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &existingQAs)
		}
	}

	seenQA := make(map[string]bool)
	var finalQAPairs []paymodel.QAPair
	for _, p := range existingQAs {
		seenQA[p.Question] = true
		finalQAPairs = append(finalQAPairs, p)
	}
	for _, p := range qaPairs {
		if !seenQA[p.Question] {
			seenQA[p.Question] = true
			finalQAPairs = append(finalQAPairs, p)
		}
	}

	if s.isCancelled(ctx, docKey) {
		slog.Debug("auto_annotate: cancel intercepted before DB commit", "doc_key", docKey)
		return fmt.Errorf("任务被用户终止 / Terminated by user")
	}

	slog.Debug("auto_annotate: finalizing DB save", "doc_key", docKey)
	// 7. Write qa_pairs to document.
	// fire-and-forget: use Background context so the DB commit completes even if
	// the originating HTTP client disconnected mid-batch — abandoning a doc that
	// already finished LLM processing wastes the cost and leaves the stage stuck.
	err = s.docRepo.UpdateDocumentQAPairsAndStage(context.Background(), &datasetID, docKey, finalQAPairs, StageAutoAnnotated)
	if err != nil {
		// fire-and-forget: same reason as above — record the failure stage even
		// if the upstream ctx was cancelled.
		s.docRepo.UpdateDocStage(context.Background(), &datasetID, docKey, StageAutoFailed)
		return fmt.Errorf("update document stage failed: %w", err)
	}

	return nil
}

// CancelAutoAnnotation aborts auto-annotation for the specified documents.
func (s *AutoAnnotationService) CancelAutoAnnotation(ctx context.Context, datasetID uint, docKeys []string) error {
	slog.Debug("auto_annotate: cancel invoked", "doc_count", len(docKeys))
	for _, k := range docKeys {
		s.storeCancelSignal(ctx, k)
		slog.Debug("auto_annotate: stored cancel signal", "doc_key", k)

		err := s.docRepo.UpdateDocStage(ctx, &datasetID, k, StageNotAnnotated)
		if err != nil {
			slog.Error("auto_annotate: force-downgrade to not_annotated failed", "doc_key", k, "error", err)
		} else {
			slog.Debug("auto_annotate: force-downgraded to not_annotated", "doc_key", k)
		}
	}
	return nil
}

// extractRawText pulls the text content from document data.
// Checks full_text, raw_text, and fact_text in order of preference.
func extractRawText(data map[string]interface{}) string {
	if data == nil {
		return ""
	}
	for _, key := range []string{"full_text", "raw_text", "fact_text", "text"} {
		if raw, ok := data[key]; ok {
			if s, ok := raw.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
