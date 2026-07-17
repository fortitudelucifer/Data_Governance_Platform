package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"gorm.io/gorm"
)

const textCandidateMaxProviders = 8
const textCandidateMaxRuns = 12
const textCandidateTemperature = 0.0

// TextCandidateService generates and adopts multi-model text QA candidates.
type TextCandidateService struct {
	capSvc         *CapabilityService
	promptService  *SystemPromptService
	payloadRepo    *repository.DB        // 候选/Judge 载荷
	docDB          repository.DocumentDB // 文本文档(读正文/写回 QA)
	dbRepo         *repository.DB
	dashboardCache DashboardCacheInvalidator
}

// NewTextCandidateService creates a TextCandidateService.
func NewTextCandidateService(
	capSvc *CapabilityService,
	promptService *SystemPromptService,
	payloadRepo *repository.DB,
	docDB repository.DocumentDB,
	dbRepo *repository.DB,
	dashboardCache DashboardCacheInvalidator,
) *TextCandidateService {
	return &TextCandidateService{
		capSvc:         capSvc,
		promptService:  promptService,
		payloadRepo:    payloadRepo,
		docDB:          docDB,
		dbRepo:         dbRepo,
		dashboardCache: dashboardCache,
	}
}

type TextCandidateCompareRequest struct {
	DatasetID         uint
	DocKey            string
	ProviderIDs       []uint
	PromptTemplateIDs []uint
	TextField         string
	UserID            uint
}

type TextCandidateCompareResult struct {
	DatasetID  uint                         `json:"dataset_id"`
	DocKey     string                       `json:"doc_key"`
	TextField  string                       `json:"text_field"`
	Candidates []paymodel.TextAICandidate `json:"candidates"`
}

type TextProviderOption struct {
	ID                uint       `json:"id"`
	Name              string     `json:"name"`
	Type              string     `json:"type"`
	CapabilityType    string     `json:"capability_type"`
	ProviderKind      string     `json:"provider_kind"`
	Model             string     `json:"model"`
	Enabled           bool       `json:"enabled"`
	Priority          int        `json:"priority"`
	LastTestSuccess   *bool      `json:"last_test_success"`
	LastTestAt        *time.Time `json:"last_test_at"`
	LastTestLatencyMs *int       `json:"last_test_latency_ms"`
}

type TextCandidateAdoptRequest struct {
	DatasetID uint
	DocKey    string
	RunID     string
	Indexes   []int
	ETag      string
	UserID    uint
}

type TextCandidateDeleteRequest struct {
	DatasetID uint
	DocKey    string
	RunID     string
}

type TextCandidateJudgeRequest struct {
	DatasetID        uint
	DocKey           string
	CandidateRunIDs  []string
	ProviderID       uint
	PromptTemplateID uint
	TextField        string
	UserID           uint
}

type TextCandidateJudgeAdoptRequest struct {
	DatasetID uint
	DocKey    string
	RunID     string
	Indexes   []int
	ETag      string
	UserID    uint
}

type textCandidatePrompt struct {
	ID                 uint
	Name               string
	Version            int
	SystemPrompt       string
	UserPromptTemplate string
	UserPrompt         string
}

type textQAAdoptParams struct {
	DatasetID         uint
	DocKey            string
	ETag              string
	UserID            uint
	Selected          []paymodel.QAPair
	EmptySelectionErr string
	DuplicateErr      string
	Decorate          func(*paymodel.QAPair)
	AfterCommit       func(adoptedCount int)
}

// Compare generates candidates for one document using the selected providers.
func (s *TextCandidateService) Compare(ctx context.Context, req TextCandidateCompareRequest) (*TextCandidateCompareResult, error) {
	if req.DatasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if req.DocKey == "" {
		return nil, fmt.Errorf("doc_key is required")
	}
	providerIDs := dedupeUintIDs(req.ProviderIDs)
	if len(providerIDs) == 0 {
		return nil, fmt.Errorf("provider_ids is required")
	}
	if len(providerIDs) > textCandidateMaxProviders {
		return nil, fmt.Errorf("provider_ids exceeds maximum %d", textCandidateMaxProviders)
	}

	doc, err := s.docDB.FindActiveDocument(ctx, &req.DatasetID, req.DocKey, 0)
	if err != nil {
		return nil, fmt.Errorf("load document failed: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: %s", req.DocKey)
	}

	textField, rawText := resolveTextForCandidate(doc.Data, req.TextField)
	if strings.TrimSpace(rawText) == "" {
		return nil, fmt.Errorf("document content is empty")
	}

	dataset, err := s.dbRepo.FindDatasetByID(ctx, req.DatasetID)
	if err != nil {
		return nil, fmt.Errorf("load dataset failed: %w", err)
	}
	prompts, err := s.loadCandidatePrompts(ctx, dataset.CaseType, req.PromptTemplateIDs, textField, rawText, req.DocKey)
	if err != nil {
		return nil, err
	}
	runCount := len(providerIDs) * len(prompts)
	if runCount > textCandidateMaxRuns {
		return nil, fmt.Errorf("selected combinations exceed maximum %d", textCandidateMaxRuns)
	}

	providers := make(map[uint]dbmodel.LLMProvider, len(providerIDs))
	for _, id := range providerIDs {
		p, err := s.loadTextProvider(ctx, id)
		if err != nil {
			return nil, err
		}
		providers[id] = *p
	}

	resultCh := make(chan paymodel.TextAICandidate, runCount)
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, providerID := range providerIDs {
		provider := providers[providerID]
		for _, prompt := range prompts {
			prompt := prompt
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				resultCh <- s.generateCandidate(ctx, req, textField, prompt, provider)
			}()
		}
	}
	wg.Wait()
	close(resultCh)

	candidates := make([]paymodel.TextAICandidate, 0, runCount)
	for cand := range resultCh {
		if err := s.payloadRepo.UpsertTextAICandidate(context.Background(), &cand); err != nil {
			return nil, fmt.Errorf("save candidate failed: %w", err)
		}
		candidates = append(candidates, cand)
	}
	providerOrder := make(map[uint]int, len(providerIDs))
	for i, id := range providerIDs {
		providerOrder[id] = i
	}
	promptOrder := make(map[uint]int, len(prompts))
	for i, p := range prompts {
		promptOrder[p.ID] = i
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		pi := providerOrder[candidates[i].Provider.ProviderID]
		pj := providerOrder[candidates[j].Provider.ProviderID]
		if pi != pj {
			return pi < pj
		}
		return promptOrder[candidates[i].PromptTemplateID] < promptOrder[candidates[j].PromptTemplateID]
	})

	return &TextCandidateCompareResult{
		DatasetID:  req.DatasetID,
		DocKey:     req.DocKey,
		TextField:  textField,
		Candidates: candidates,
	}, nil
}

// ListProviderOptions returns a sanitized provider list for annotator-facing
// model selection.
func (s *TextCandidateService) ListProviderOptions(ctx context.Context) ([]TextProviderOption, error) {
	var providers []dbmodel.LLMProvider
	if err := s.dbRepo.DB.WithContext(ctx).
		Scopes(repository.EnabledTextChatProvidersScope).
		Order("priority DESC, id ASC").
		Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("list text providers failed: %w", err)
	}
	out := make([]TextProviderOption, len(providers))
	for i, p := range providers {
		out[i] = TextProviderOption{
			ID:                p.ID,
			Name:              p.Name,
			Type:              p.Type,
			CapabilityType:    CapabilityTextChat,
			ProviderKind:      p.ProviderKind,
			Model:             p.Model,
			Enabled:           p.Enabled,
			Priority:          p.Priority,
			LastTestSuccess:   p.LastTestSuccess,
			LastTestAt:        p.LastTestAt,
			LastTestLatencyMs: p.LastTestLatencyMs,
		}
		if p.CapabilityType != "" {
			out[i].CapabilityType = p.CapabilityType
		}
	}
	return out, nil
}

// ListPromptTemplatesForDataset returns enabled QA auto-annotation prompt
// templates. Prompts are intentionally not filtered by dataset case_type so
// annotators can run cross-type prompt comparisons when needed.
func (s *TextCandidateService) ListPromptTemplatesForDataset(ctx context.Context, datasetID uint) ([]dbmodel.AutoPromptTemplate, error) {
	if datasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if _, err := s.dbRepo.FindDatasetByID(ctx, datasetID); err != nil {
		return nil, fmt.Errorf("load dataset failed: %w", err)
	}
	templates, err := s.promptService.ListAutoPromptTemplates(ctx, "", false)
	if err != nil {
		return nil, err
	}
	out := make([]dbmodel.AutoPromptTemplate, 0, len(templates))
	for _, tpl := range templates {
		if tpl.TaskType == "" || tpl.TaskType == AutoPromptTaskTextQA {
			out = append(out, tpl)
		}
	}
	return out, nil
}

// ListJudgePromptTemplatesForDataset returns enabled Judge Agent prompt
// templates. The list is intentionally not case_type-filtered so admins can
// compare reusable judge rubrics across datasets.
func (s *TextCandidateService) ListJudgePromptTemplatesForDataset(ctx context.Context, datasetID uint) ([]dbmodel.AutoPromptTemplate, error) {
	if datasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if _, err := s.dbRepo.FindDatasetByID(ctx, datasetID); err != nil {
		return nil, fmt.Errorf("load dataset failed: %w", err)
	}
	templates, err := s.promptService.ListAutoPromptTemplates(ctx, "", false)
	if err != nil {
		return nil, err
	}
	out := make([]dbmodel.AutoPromptTemplate, 0, len(templates))
	for _, tpl := range templates {
		if tpl.TaskType == AutoPromptTaskTextJudge {
			out = append(out, tpl)
		}
	}
	return out, nil
}

// List returns candidate history for a document.
func (s *TextCandidateService) List(ctx context.Context, datasetID uint, docKey string) ([]paymodel.TextAICandidate, error) {
	if datasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if docKey == "" {
		return nil, fmt.Errorf("doc_key is required")
	}
	return s.payloadRepo.FindTextAICandidates(ctx, datasetID, docKey)
}

// ListJudgeRuns returns Judge Agent history for a document.
func (s *TextCandidateService) ListJudgeRuns(ctx context.Context, datasetID uint, docKey string) ([]paymodel.TextAIJudgeRun, error) {
	if datasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if docKey == "" {
		return nil, fmt.Errorf("doc_key is required")
	}
	return s.payloadRepo.FindTextAIJudgeRuns(ctx, datasetID, docKey)
}

// Delete removes an unadopted candidate run. Adopted candidates are retained
// so official QA provenance remains inspectable.
func (s *TextCandidateService) Delete(ctx context.Context, req TextCandidateDeleteRequest) error {
	if req.DatasetID == 0 {
		return fmt.Errorf("dataset_id is required")
	}
	if req.DocKey == "" {
		return fmt.Errorf("doc_key is required")
	}
	if req.RunID == "" {
		return fmt.Errorf("run_id is required")
	}
	cand, err := s.payloadRepo.FindTextAICandidateByRunID(ctx, req.RunID)
	if err != nil {
		return fmt.Errorf("load candidate failed: %w", err)
	}
	if cand == nil || cand.DatasetID != req.DatasetID || cand.DocKey != req.DocKey {
		return fmt.Errorf("candidate not found")
	}
	if cand.AdoptedCount > 0 {
		return fmt.Errorf("candidate has been adopted and is retained for audit")
	}
	deleted, err := s.payloadRepo.DeleteTextAICandidate(ctx, req.DatasetID, req.DocKey, req.RunID)
	if err != nil {
		return fmt.Errorf("delete candidate failed: %w", err)
	}
	if !deleted {
		return fmt.Errorf("candidate not found")
	}
	return nil
}

// Judge runs a Judge Agent over selected candidate runs and stores the
// advisory result. It does not modify official qa_pairs.
func (s *TextCandidateService) Judge(ctx context.Context, req TextCandidateJudgeRequest) (*paymodel.TextAIJudgeRun, error) {
	if req.DatasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if req.DocKey == "" {
		return nil, fmt.Errorf("doc_key is required")
	}
	candidateRunIDs := dedupeStringIDs(req.CandidateRunIDs)
	if len(candidateRunIDs) == 0 {
		return nil, fmt.Errorf("candidate_run_ids is required")
	}
	if len(candidateRunIDs) > textCandidateMaxRuns {
		return nil, fmt.Errorf("candidate_run_ids exceeds maximum %d", textCandidateMaxRuns)
	}
	if req.ProviderID == 0 {
		return nil, fmt.Errorf("judge provider_id is required")
	}
	if req.PromptTemplateID == 0 {
		return nil, fmt.Errorf("judge prompt_template_id is required")
	}

	doc, err := s.docDB.FindActiveDocument(ctx, &req.DatasetID, req.DocKey, 0)
	if err != nil {
		return nil, fmt.Errorf("load document failed: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: %s", req.DocKey)
	}
	textField, rawText := resolveTextForCandidate(doc.Data, req.TextField)
	if strings.TrimSpace(rawText) == "" {
		return nil, fmt.Errorf("document content is empty")
	}

	dataset, err := s.dbRepo.FindDatasetByID(ctx, req.DatasetID)
	if err != nil {
		return nil, fmt.Errorf("load dataset failed: %w", err)
	}
	candidates, err := s.loadJudgeCandidates(ctx, req.DatasetID, req.DocKey, candidateRunIDs)
	if err != nil {
		return nil, err
	}
	candidatesJSON, err := renderJudgeCandidates(candidates)
	if err != nil {
		return nil, err
	}
	provider, err := s.loadTextProvider(ctx, req.ProviderID)
	if err != nil {
		return nil, err
	}
	prompt, err := s.loadJudgePrompt(ctx, req.PromptTemplateID, dataset.CaseType, textField, rawText, req.DocKey, candidatesJSON)
	if err != nil {
		return nil, err
	}

	traceID := fmt.Sprintf("text-judge-%d-%s", req.DatasetID, req.DocKey)
	runID := fmt.Sprintf("%s-%d-%d-%s", traceID, provider.ID, prompt.ID, randomToken(4))
	temperature := textCandidateTemperature
	generationParams := mergeGenerationParams(generationParamsFromExtraConfig(provider.ExtraConfig), GenerationParams{
		Temperature: &temperature,
	})
	judgeRun := &paymodel.TextAIJudgeRun{
		RunID:                runID,
		TraceID:              traceID,
		DatasetID:            req.DatasetID,
		DocKey:               req.DocKey,
		TextField:            textField,
		CandidateRunIDs:      candidateRunIDs,
		PromptTemplateID:     prompt.ID,
		PromptTemplateName:   prompt.Name,
		PromptVersion:        prompt.Version,
		SystemPromptSnapshot: prompt.SystemPrompt,
		UserPromptSnapshot:   prompt.UserPrompt,
		GenerationParams:     generationParams.toMap(),
		Provider: paymodel.ModelProviderRef{
			ProviderID:     provider.ID,
			ProviderName:   provider.Name,
			ModelID:        provider.Model,
			CapabilityType: CapabilityTextChat,
			EndpointMode:   provider.Type,
		},
		Status:    "failed",
		CreatedBy: req.UserID,
		CreatedAt: time.Now(),
	}

	resp, callErr := s.capSvc.Invoke(ctx, CapabilityRequest{
		TraceID:        traceID,
		RunID:          runID,
		CapabilityType: CapabilityTextChat,
		Prompt:         prompt.UserPrompt,
		Extras: map[string]interface{}{
			"system_prompt": prompt.SystemPrompt,
			"provider_id":   provider.ID,
			"temperature":   textCandidateTemperature,
		},
	})
	if resp.Provider.ProviderID != 0 {
		judgeRun.Provider = resp.Provider
	}
	judgeRun.LatencyMs = resp.LatencyMs
	judgeRun.Cost = resp.Cost
	judgeRun.EstimatedCost = resp.EstimatedCost
	judgeRun.RawResponse = resp.Raw
	if resp.GenerationParams != nil {
		judgeRun.GenerationParams = resp.GenerationParams
	}
	if judgeRun.RawResponse == nil && resp.Text != "" {
		judgeRun.RawResponse = resp.Text
	}
	if callErr != nil {
		judgeRun.Error = callErr.Error()
		_ = s.payloadRepo.UpsertTextAIJudgeRun(context.Background(), judgeRun)
		return judgeRun, callErr
	}

	parsed, parseErr := parseJudgeAgentResult(resp.Text, runID, candidateRunIDs)
	if parseErr != nil {
		judgeRun.Error = parseErr.Error()
		_ = s.payloadRepo.UpsertTextAIJudgeRun(context.Background(), judgeRun)
		return judgeRun, parseErr
	}
	judgeRun.Status = "success"
	judgeRun.Error = resp.Error
	judgeRun.OverallScore = parsed.OverallScore
	judgeRun.Decision = parsed.Decision
	judgeRun.Summary = parsed.Summary
	judgeRun.ReviewReasons = parsed.ReviewReasons
	judgeRun.CandidateScores = parsed.CandidateScores
	judgeRun.MergedQAPairs = parsed.MergedQAPairs
	if err := s.payloadRepo.UpsertTextAIJudgeRun(context.Background(), judgeRun); err != nil {
		return nil, fmt.Errorf("save judge run failed: %w", err)
	}
	return judgeRun, nil
}

// Adopt appends selected candidate QA pairs into the official refinement draft.
func (s *TextCandidateService) Adopt(ctx context.Context, req TextCandidateAdoptRequest) (*paymodel.Document, error) {
	if req.DatasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if req.DocKey == "" {
		return nil, fmt.Errorf("doc_key is required")
	}
	if req.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if req.ETag == "" {
		return nil, fmt.Errorf("etag is required")
	}

	cand, err := s.payloadRepo.FindTextAICandidateByRunID(ctx, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("load candidate failed: %w", err)
	}
	if cand == nil || cand.DatasetID != req.DatasetID || cand.DocKey != req.DocKey {
		return nil, fmt.Errorf("candidate not found")
	}
	if cand.Status != "success" {
		return nil, fmt.Errorf("candidate is not successful: %s", cand.Status)
	}

	selected, err := selectCandidatePairs(cand, req.Indexes)
	if err != nil {
		return nil, err
	}

	return s.adoptSelectedQAPairs(ctx, textQAAdoptParams{
		DatasetID:         req.DatasetID,
		DocKey:            req.DocKey,
		ETag:              req.ETag,
		UserID:            req.UserID,
		Selected:          selected,
		EmptySelectionErr: "no QA pairs selected",
		DuplicateErr:      "selected QA pairs already exist",
		Decorate: func(p *paymodel.QAPair) {
			p.Source = "llm_candidate"
			p.Confirmed = false
			p.CandidateRunID = cand.RunID
			p.TextField = cand.TextField
			p.ProviderID = cand.Provider.ProviderID
			p.ProviderName = cand.Provider.ProviderName
			p.Model = cand.Provider.ModelID
			p.PromptTemplateID = cand.PromptTemplateID
			p.PromptTemplateName = cand.PromptTemplateName
			p.PromptVersion = cand.PromptVersion
		},
		AfterCommit: func(adoptedCount int) {
			_ = s.payloadRepo.MarkTextAICandidateAdopted(context.Background(), cand.RunID, adoptedCount, req.UserID)
		},
	})
}

// AdoptJudge appends selected Judge merged QA pairs into the official
// refinement draft. The Judge run remains advisory until this method is called.
func (s *TextCandidateService) AdoptJudge(ctx context.Context, req TextCandidateJudgeAdoptRequest) (*paymodel.Document, error) {
	if req.DatasetID == 0 {
		return nil, fmt.Errorf("dataset_id is required")
	}
	if req.DocKey == "" {
		return nil, fmt.Errorf("doc_key is required")
	}
	if req.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if req.ETag == "" {
		return nil, fmt.Errorf("etag is required")
	}

	judgeRun, err := s.payloadRepo.FindTextAIJudgeRunByRunID(ctx, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("load judge run failed: %w", err)
	}
	if judgeRun == nil || judgeRun.DatasetID != req.DatasetID || judgeRun.DocKey != req.DocKey {
		return nil, fmt.Errorf("judge run not found")
	}
	if judgeRun.Status != "success" {
		return nil, fmt.Errorf("judge run is not successful: %s", judgeRun.Status)
	}

	selected, err := selectJudgePairs(judgeRun, req.Indexes)
	if err != nil {
		return nil, err
	}

	return s.adoptSelectedQAPairs(ctx, textQAAdoptParams{
		DatasetID:         req.DatasetID,
		DocKey:            req.DocKey,
		ETag:              req.ETag,
		UserID:            req.UserID,
		Selected:          selected,
		EmptySelectionErr: "no judge QA pairs selected",
		DuplicateErr:      "selected judge QA pairs already exist",
		Decorate: func(p *paymodel.QAPair) {
			p.Source = "llm_judge"
			p.Confirmed = false
			p.JudgeRunID = judgeRun.RunID
			p.TextField = judgeRun.TextField
			p.ProviderID = judgeRun.Provider.ProviderID
			p.ProviderName = judgeRun.Provider.ProviderName
			p.Model = judgeRun.Provider.ModelID
			p.PromptTemplateID = judgeRun.PromptTemplateID
			p.PromptTemplateName = judgeRun.PromptTemplateName
			p.PromptVersion = judgeRun.PromptVersion
			if len(p.SourceCandidateRunIDs) == 0 {
				p.SourceCandidateRunIDs = append([]string(nil), judgeRun.CandidateRunIDs...)
			}
			if p.Meta == nil {
				p.Meta = map[string]interface{}{}
			}
			p.Meta["judge_decision"] = judgeRun.Decision
			p.Meta["judge_score"] = judgeRun.OverallScore
		},
		AfterCommit: func(adoptedCount int) {
			_ = s.payloadRepo.MarkTextAIJudgeRunAdopted(context.Background(), judgeRun.RunID, adoptedCount, req.UserID)
		},
	})
}

func (s *TextCandidateService) adoptSelectedQAPairs(ctx context.Context, params textQAAdoptParams) (*paymodel.Document, error) {
	doc, err := s.docDB.FindActiveDocument(ctx, &params.DatasetID, params.DocKey, 0)
	if err != nil {
		return nil, fmt.Errorf("load document failed: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document not found: %s", params.DocKey)
	}
	if doc.AnnotationStage != StageRefining {
		return nil, fmt.Errorf("document not in refining state, current: %s", doc.AnnotationStage)
	}
	if doc.ETag != params.ETag {
		return nil, fmt.Errorf("document has been modified, please refresh")
	}
	if len(params.Selected) == 0 {
		if params.EmptySelectionErr != "" {
			return nil, fmt.Errorf("%s", params.EmptySelectionErr)
		}
		return nil, fmt.Errorf("no QA pairs selected")
	}

	existing := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	seen := make(map[string]struct{}, len(existing))
	for _, p := range existing {
		seen[textQAMergeKey(p)] = struct{}{}
	}
	adopted := make([]paymodel.QAPair, 0, len(params.Selected))
	for _, p := range params.Selected {
		key := textQAMergeKey(p)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if params.Decorate != nil {
			params.Decorate(&p)
		}
		adopted = append(adopted, p)
	}
	if len(adopted) == 0 {
		if params.DuplicateErr != "" {
			return nil, fmt.Errorf("%s", params.DuplicateErr)
		}
		return nil, fmt.Errorf("selected QA pairs already exist")
	}

	next := append(existing, adopted...)
	cursor := doc.RefinementCursor
	if len(next) > 0 && cursor >= len(next) {
		cursor = len(next) - 1
	}
	newETag, err := randomETag()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	annotatorName := s.getAnnotatorName(ctx, params.UserID)
	if err := s.docDB.UpdateDocumentQAPairsAndCursor(ctx, &params.DatasetID, params.DocKey, params.ETag, next, cursor, newETag, &params.UserID, annotatorName); err != nil {
		return nil, fmt.Errorf("update document failed: %w", err)
	}
	if params.AfterCommit != nil {
		params.AfterCommit(len(adopted))
	}
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docDB, params.DatasetID)

	doc.Data["qa_pairs"] = next
	doc.RefinementCursor = cursor
	doc.ETag = newETag
	doc.UpdatedAt = paymodel.NewJSONTime(now)
	doc.AnnotatorUserID = fmt.Sprintf("%d", params.UserID)
	doc.AnnotatorName = annotatorName
	doc.AnnotatorActionTime = paymodel.JSONTimePtr(&now)
	if s.dashboardCache != nil {
		s.dashboardCache.InvalidateDataset(params.DatasetID)
	}
	return doc, nil
}

func (s *TextCandidateService) generateCandidate(
	ctx context.Context,
	req TextCandidateCompareRequest,
	textField string,
	prompt textCandidatePrompt,
	provider dbmodel.LLMProvider,
) paymodel.TextAICandidate {
	traceID := fmt.Sprintf("text-candidate-%d-%s", req.DatasetID, req.DocKey)
	runID := fmt.Sprintf("%s-%d-%d-%s", traceID, provider.ID, prompt.ID, randomToken(4))
	temperature := textCandidateTemperature
	generationParams := mergeGenerationParams(generationParamsFromExtraConfig(provider.ExtraConfig), GenerationParams{
		Temperature: &temperature,
	})
	cand := paymodel.TextAICandidate{
		RunID:                runID,
		TraceID:              traceID,
		DatasetID:            req.DatasetID,
		DocKey:               req.DocKey,
		TextField:            textField,
		PromptTemplateID:     prompt.ID,
		PromptTemplateName:   prompt.Name,
		PromptVersion:        prompt.Version,
		SystemPromptSnapshot: prompt.SystemPrompt,
		UserPromptSnapshot:   prompt.UserPrompt,
		GenerationParams:     generationParams.toMap(),
		Provider: paymodel.ModelProviderRef{
			ProviderID:     provider.ID,
			ProviderName:   provider.Name,
			ModelID:        provider.Model,
			CapabilityType: CapabilityTextChat,
			EndpointMode:   provider.Type,
		},
		Status:    "failed",
		QAPairs:   []paymodel.QAPair{},
		CreatedBy: req.UserID,
		CreatedAt: time.Now(),
	}

	resp, err := s.capSvc.Invoke(ctx, CapabilityRequest{
		TraceID:        traceID,
		RunID:          runID,
		CapabilityType: CapabilityTextChat,
		Prompt:         prompt.UserPrompt,
		Extras: map[string]interface{}{
			"system_prompt": prompt.SystemPrompt,
			"provider_id":   provider.ID,
			"temperature":   textCandidateTemperature,
		},
	})
	if resp.Provider.ProviderID != 0 {
		cand.Provider = resp.Provider
	}
	cand.LatencyMs = resp.LatencyMs
	cand.Cost = resp.Cost
	cand.EstimatedCost = resp.EstimatedCost
	cand.RawResponse = resp.Raw
	if resp.GenerationParams != nil {
		cand.GenerationParams = resp.GenerationParams
	}
	if cand.RawResponse == nil && resp.Text != "" {
		cand.RawResponse = resp.Text
	}
	if err != nil {
		cand.Error = err.Error()
		return cand
	}
	cand.Status = resp.Status
	if cand.Status == "" {
		cand.Status = "success"
	}
	cand.Error = resp.Error
	qaPairs := paymodel.ParseQAPairs(resp.Text)
	if len(qaPairs) == 0 {
		cand.Status = "failed"
		cand.Error = "LLM returned no valid QA pairs"
		return cand
	}
	for i := range qaPairs {
		qaPairs[i].Source = "llm_candidate"
		qaPairs[i].Confirmed = false
		qaPairs[i].CandidateRunID = cand.RunID
		qaPairs[i].TextField = textField
		qaPairs[i].ProviderID = cand.Provider.ProviderID
		qaPairs[i].ProviderName = cand.Provider.ProviderName
		qaPairs[i].Model = cand.Provider.ModelID
		qaPairs[i].PromptTemplateID = cand.PromptTemplateID
		qaPairs[i].PromptTemplateName = cand.PromptTemplateName
		qaPairs[i].PromptVersion = cand.PromptVersion
	}
	cand.QAPairs = qaPairs
	return cand
}

func (s *TextCandidateService) loadTextProvider(ctx context.Context, id uint) (*dbmodel.LLMProvider, error) {
	var p dbmodel.LLMProvider
	err := s.dbRepo.DB.WithContext(ctx).First(&p, id).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("provider not found: %d", id)
		}
		return nil, fmt.Errorf("load provider failed: %w", err)
	}
	if !p.Enabled {
		return nil, fmt.Errorf("provider disabled: %s", p.Name)
	}
	if p.CapabilityType != "" && p.CapabilityType != CapabilityTextChat {
		return nil, fmt.Errorf("provider %s is not text.chat", p.Name)
	}
	if p.LastTestSuccess == nil || !*p.LastTestSuccess {
		return nil, fmt.Errorf("provider %s has not passed connectivity test", p.Name)
	}
	return &p, nil
}

func (s *TextCandidateService) loadCandidatePrompts(
	ctx context.Context,
	caseType string,
	ids []uint,
	textField string,
	rawText string,
	docKey string,
) ([]textCandidatePrompt, error) {
	if len(ids) == 0 {
		prompt, err := s.promptService.GetOrDefault(ctx, caseType)
		if err != nil {
			return nil, fmt.Errorf("load system prompt failed: %w", err)
		}
		return []textCandidatePrompt{{
			ID:                 0,
			Name:               prompt.Name,
			Version:            1,
			SystemPrompt:       prompt.Content,
			UserPromptTemplate: "{{text}}",
			UserPrompt:         rawText,
		}}, nil
	}

	ids = dedupeUintIDs(ids)
	out := make([]textCandidatePrompt, 0, len(ids))
	for _, id := range ids {
		tpl, err := s.promptService.GetAutoPromptTemplate(ctx, id)
		if err != nil {
			return nil, err
		}
		if !tpl.Enabled {
			return nil, fmt.Errorf("prompt template disabled: %s", tpl.Name)
		}
		if tpl.TaskType != "" && tpl.TaskType != AutoPromptTaskTextQA {
			return nil, fmt.Errorf("prompt template %s is not %s", tpl.Name, AutoPromptTaskTextQA)
		}
		rendered := renderAutoPromptTemplate(tpl.UserPromptTemplate, map[string]string{
			"text":       rawText,
			"text_field": textField,
			"doc_key":    docKey,
			"case_type":  caseType,
		})
		out = append(out, textCandidatePrompt{
			ID:                 tpl.ID,
			Name:               tpl.Name,
			Version:            tpl.Version,
			SystemPrompt:       tpl.SystemPrompt,
			UserPromptTemplate: tpl.UserPromptTemplate,
			UserPrompt:         rendered,
		})
	}
	return out, nil
}

func (s *TextCandidateService) loadJudgePrompt(
	ctx context.Context,
	id uint,
	caseType string,
	textField string,
	rawText string,
	docKey string,
	candidatesJSON string,
) (textCandidatePrompt, error) {
	tpl, err := s.promptService.GetAutoPromptTemplate(ctx, id)
	if err != nil {
		return textCandidatePrompt{}, err
	}
	if !tpl.Enabled {
		return textCandidatePrompt{}, fmt.Errorf("judge prompt template disabled: %s", tpl.Name)
	}
	if tpl.TaskType != AutoPromptTaskTextJudge {
		return textCandidatePrompt{}, fmt.Errorf("prompt template %s is not %s", tpl.Name, AutoPromptTaskTextJudge)
	}
	rendered := renderAutoPromptTemplate(tpl.UserPromptTemplate, map[string]string{
		"text":       rawText,
		"text_field": textField,
		"doc_key":    docKey,
		"case_type":  caseType,
		"candidates": candidatesJSON,
	})
	return textCandidatePrompt{
		ID:                 tpl.ID,
		Name:               tpl.Name,
		Version:            tpl.Version,
		SystemPrompt:       tpl.SystemPrompt,
		UserPromptTemplate: tpl.UserPromptTemplate,
		UserPrompt:         rendered,
	}, nil
}

func (s *TextCandidateService) loadJudgeCandidates(ctx context.Context, datasetID uint, docKey string, runIDs []string) ([]paymodel.TextAICandidate, error) {
	out := make([]paymodel.TextAICandidate, 0, len(runIDs))
	for _, runID := range runIDs {
		cand, err := s.payloadRepo.FindTextAICandidateByRunID(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("load candidate %s failed: %w", runID, err)
		}
		if cand == nil || cand.DatasetID != datasetID || cand.DocKey != docKey {
			return nil, fmt.Errorf("candidate not found: %s", runID)
		}
		out = append(out, *cand)
	}
	return out, nil
}

func renderAutoPromptTemplate(tpl string, vars map[string]string) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

type judgeCandidatePayload struct {
	RunID              string                 `json:"run_id"`
	Status             string                 `json:"status"`
	Error              string                 `json:"error,omitempty"`
	ProviderName       string                 `json:"provider_name"`
	Model              string                 `json:"model"`
	PromptTemplateID   uint                   `json:"prompt_template_id,omitempty"`
	PromptTemplateName string                 `json:"prompt_template_name,omitempty"`
	PromptVersion      int                    `json:"prompt_version,omitempty"`
	GenerationParams   map[string]interface{} `json:"generation_params,omitempty"`
	QAPairs            []paymodel.QAPair    `json:"qa_pairs"`
}

func renderJudgeCandidates(candidates []paymodel.TextAICandidate) (string, error) {
	payload := make([]judgeCandidatePayload, 0, len(candidates))
	for _, cand := range candidates {
		payload = append(payload, judgeCandidatePayload{
			RunID:              cand.RunID,
			Status:             cand.Status,
			Error:              cand.Error,
			ProviderName:       cand.Provider.ProviderName,
			Model:              cand.Provider.ModelID,
			PromptTemplateID:   cand.PromptTemplateID,
			PromptTemplateName: cand.PromptTemplateName,
			PromptVersion:      cand.PromptVersion,
			GenerationParams:   cand.GenerationParams,
			QAPairs:            cand.QAPairs,
		})
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal judge candidates failed: %w", err)
	}
	return string(b), nil
}

type parsedJudgeAgentResult struct {
	OverallScore    *float64
	Decision        string
	Summary         string
	ReviewReasons   []string
	CandidateScores []paymodel.TextAIJudgeCandidateScore
	MergedQAPairs   []paymodel.QAPair
}

func parseJudgeAgentResult(response string, judgeRunID string, fallbackRunIDs []string) (parsedJudgeAgentResult, error) {
	clean := stripCodeFence(response)
	start := strings.Index(clean, "{")
	end := strings.LastIndex(clean, "}")
	if start == -1 || end == -1 || end <= start {
		return parsedJudgeAgentResult{}, fmt.Errorf("Judge Agent returned no JSON object")
	}
	clean = clean[start : end+1]
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(clean), &raw); err != nil {
		return parsedJudgeAgentResult{}, fmt.Errorf("parse Judge Agent JSON failed: %w", err)
	}
	result := parsedJudgeAgentResult{
		OverallScore:  float64PtrFromAny(raw["overall_score"]),
		Decision:      normalizeJudgeDecision(stringFromAny(raw["decision"])),
		Summary:       stringFromAny(raw["summary"]),
		ReviewReasons: stringSliceFromAny(raw["review_reasons"]),
	}
	result.CandidateScores = parseJudgeCandidateScores(raw["candidate_scores"])
	if result.Decision == "" {
		result.Decision = "needs_review"
	}
	if mergedRaw, ok := raw["merged_qa_pairs"]; ok {
		normalized := normalizeJudgeMergedRaw(mergedRaw, judgeRunID, fallbackRunIDs)
		result.MergedQAPairs = paymodel.ParseQAPairs(normalized)
		for i := range result.MergedQAPairs {
			result.MergedQAPairs[i].JudgeRunID = judgeRunID
			result.MergedQAPairs[i].Source = "llm_judge_suggestion"
			result.MergedQAPairs[i].Confirmed = false
			if len(result.MergedQAPairs[i].SourceCandidateRunIDs) == 0 {
				result.MergedQAPairs[i].SourceCandidateRunIDs = append([]string(nil), fallbackRunIDs...)
			}
		}
	}
	return result, nil
}

func normalizeJudgeDecision(decision string) string {
	switch strings.TrimSpace(decision) {
	case "pass", "merge", "needs_review":
		return strings.TrimSpace(decision)
	default:
		return ""
	}
}

func parseJudgeCandidateScores(v interface{}) []paymodel.TextAIJudgeCandidateScore {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]paymodel.TextAIJudgeCandidateScore, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		runID := stringFromAny(m["run_id"])
		if runID == "" {
			runID = stringFromAny(m["candidate_run_id"])
		}
		score := paymodel.TextAIJudgeCandidateScore{
			RunID:    runID,
			Score:    float64PtrFromAny(m["score"]),
			Decision: stringFromAny(m["decision"]),
			Summary:  stringFromAny(m["summary"]),
			Risks:    stringSliceFromAny(m["risks"]),
			QACount:  intFromAny(m["qa_count"]),
		}
		if score.RunID != "" {
			out = append(out, score)
		}
	}
	return out
}

func normalizeJudgeMergedRaw(v interface{}, judgeRunID string, fallbackRunIDs []string) interface{} {
	arr, ok := v.([]interface{})
	if !ok {
		return v
	}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		m["judge_run_id"] = judgeRunID
		if _, ok := m["source_candidate_run_ids"]; !ok {
			if sourceRunIDs, ok := m["source_run_ids"]; ok {
				m["source_candidate_run_ids"] = sourceRunIDs
			} else {
				m["source_candidate_run_ids"] = fallbackRunIDs
			}
		}
	}
	return arr
}

func stringFromAny(v interface{}) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func stringSliceFromAny(v interface{}) []string {
	switch arr := v.(type) {
	case []string:
		return arr
	case []interface{}:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func float64PtrFromAny(v interface{}) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case float32:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func intFromAny(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

func textQAMergeKey(p paymodel.QAPair) string {
	if key := strings.TrimSpace(p.QuestionKey); key != "" {
		return "key:" + strings.ToLower(key)
	}
	return "question:" + strings.TrimSpace(p.Question)
}

func (s *TextCandidateService) getAnnotatorName(ctx context.Context, userID uint) string {
	user, err := s.dbRepo.FindUserByID(ctx, userID)
	if err != nil || user == nil {
		return "Unknown"
	}
	if user.DisplayName != "" {
		return user.DisplayName
	}
	return user.Username
}

func resolveTextForCandidate(data map[string]interface{}, requested string) (string, string) {
	if data == nil {
		return "", ""
	}
	if requested != "" {
		if s, ok := data[requested].(string); ok {
			return requested, s
		}
		return requested, ""
	}
	for _, key := range []string{"full_text", "raw_text", "fact_text", "text", "content"} {
		if s, ok := data[key].(string); ok && strings.TrimSpace(s) != "" {
			return key, s
		}
	}
	return "", ""
}

func dedupeUintIDs(ids []uint) []uint {
	seen := map[uint]struct{}{}
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func dedupeStringIDs(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func selectCandidatePairs(cand *paymodel.TextAICandidate, indexes []int) ([]paymodel.QAPair, error) {
	if len(indexes) == 0 {
		return append([]paymodel.QAPair(nil), cand.QAPairs...), nil
	}
	out := make([]paymodel.QAPair, 0, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(cand.QAPairs) {
			return nil, fmt.Errorf("candidate index out of bounds: %d", idx)
		}
		out = append(out, cand.QAPairs[idx])
	}
	return out, nil
}

func selectJudgePairs(run *paymodel.TextAIJudgeRun, indexes []int) ([]paymodel.QAPair, error) {
	if len(indexes) == 0 {
		return append([]paymodel.QAPair(nil), run.MergedQAPairs...), nil
	}
	out := make([]paymodel.QAPair, 0, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= len(run.MergedQAPairs) {
			return nil, fmt.Errorf("judge QA index out of bounds: %d", idx)
		}
		out = append(out, run.MergedQAPairs[idx])
	}
	return out, nil
}

func randomETag() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate etag failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func randomToken(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
