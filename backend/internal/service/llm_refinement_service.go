package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

type LLMRefinementService struct {
	docRepo repository.DocumentDB
	dbRepo *repository.DB
	capSvc    *CapabilityService
}

func NewLLMRefinementService(docRepo repository.DocumentDB, dbRepo *repository.DB, capSvc *CapabilityService) *LLMRefinementService {
	return &LLMRefinementService{
		docRepo: docRepo,
		dbRepo: dbRepo,
		capSvc:    capSvc,
	}
}

type LLMRefinementResponse struct {
	Score   int `json:"score"`
	Details struct {
		Accuracy     int `json:"accuracy"`
		Completeness int `json:"completeness"`
		Consistency  int `json:"consistency"`
	} `json:"details"`
	Reasoning string `json:"reasoning"`
	Pass      bool   `json:"pass"`
	DocStatus string `json:"doc_status"`
}

type llmEvaluationResponse struct {
	Accuracy     int    `json:"accuracy"`
	Completeness int    `json:"completeness"`
	Consistency  int    `json:"consistency"`
	Reasoning    string `json:"reasoning"`
}

func (s *LLMRefinementService) TriggerRefinement(ctx context.Context, docKey string, model string, providerID uint, userID uint) (*LLMRefinementResponse, error) {
	return s.triggerRefinement(ctx, nil, docKey, model, providerID, userID)
}

func (s *LLMRefinementService) TriggerRefinementInDataset(ctx context.Context, datasetID uint, docKey string, model string, providerID uint, userID uint) (*LLMRefinementResponse, error) {
	return s.triggerRefinement(ctx, &datasetID, docKey, model, providerID, userID)
}

func (s *LLMRefinementService) triggerRefinement(ctx context.Context, datasetID *uint, docKey string, model string, providerID uint, userID uint) (*LLMRefinementResponse, error) {
	// 1. Get document
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, 0)
	if err != nil {
		return nil, fmt.Errorf("查找文档失败: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("文档未找到: %s", docKey)
	}

	if doc.AnnotationStage != StageRefining {
		return nil, fmt.Errorf("文档状态不是精标中，当前状态: %s", doc.AnnotationStage)
	}

	// 2. Prepare for LLM call
	qaPairs := paymodel.ParseQAPairs(doc.Data["qa_pairs"])
	qaBytes, _ := json.Marshal(qaPairs)
	rawText := extractRawText(doc.Data)

	prompt := `你是严格的文本标注质量审核员。请只依据原文评估已生成的问答对，不要引入原文之外的信息。

请从三个维度评分，每个维度 0-100 分：
1. accuracy：答案是否被原文直接支持，是否存在事实错误。
2. completeness：问答对是否覆盖原文中的关键事实、主体、时间、行为和结果。
3. consistency：问题、答案、证据字段是否结构稳定、互不矛盾、可复核。

请直接返回合法 JSON 对象，不要输出 markdown、解释性前后缀或代码块。
要求：
- 先输出三个分数字段，再输出 reasoning。
- reasoning 不超过 120 个中文字符。
- 不要复述原文，不要展开长篇说明。

格式必须严格如下：
{
  "accuracy": 90,
  "completeness": 85,
  "consistency": 95,
  "reasoning": "简要说明评分原因和主要风险"
}

原文：
%s

问答对 JSON：
%s
`
	systemPrompt := fmt.Sprintf(prompt, rawText, string(qaBytes))

	// 3. Call LLM via CapabilityService (text.chat) for unified trace logging.
	// Provider selection (by model name or first available) is handled inside
	// TextLLMAdapter so we only pass the model hint here.
	temperature := 0.0
	maxTokens := 2048
	extras := map[string]interface{}{
		"system_prompt":   systemPrompt,
		"temperature":     temperature,
		"max_tokens":      maxTokens,
		"response_format": "json_object",
	}
	if providerID > 0 {
		extras["provider_id"] = providerID
	}
	capResp, err := s.capSvc.Invoke(ctx, CapabilityRequest{
		CapabilityType: CapabilityTextChat,
		Model:          model,
		Prompt:         "请根据系统提示中的原文和问答对输出质量评分 JSON。",
		Extras:         extras,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM 调用失败: %w", err)
	}

	// 4. Parse response
	cleaned := strings.TrimSpace(capResp.Text)
	if cleaned == "" {
		return nil, fmt.Errorf("LLM 未返回评分内容")
	}
	if idx := strings.Index(cleaned, "{"); idx != -1 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx != -1 {
		cleaned = cleaned[:idx+1]
	}

	eval, err := parseLLMRefinementEvaluation(cleaned)
	if err != nil {
		return nil, fmt.Errorf("解析 LLM 评估结果失败: %w；原始返回: %s", err, truncateForError(capResp.Text, 240))
	}
	eval.Accuracy = clampPercent(eval.Accuracy)
	eval.Completeness = clampPercent(eval.Completeness)
	eval.Consistency = clampPercent(eval.Consistency)
	if strings.TrimSpace(eval.Reasoning) == "" {
		eval.Reasoning = "模型未返回评分说明"
	}

	// 5. Calculate weighted score
	score := float64(eval.Accuracy)*0.4 + float64(eval.Completeness)*0.3 + float64(eval.Consistency)*0.3
	finalScore := int(score)

	// 6. Workflow / Sampling decision
	pass := false
	newStatus := StageRefining

	if finalScore >= 95 {
		// 10% sampling check
		if rand.Float64() < 0.10 { // Sampled -> Requires Human QA Check
			newStatus = StageRefining
			// We keep it in refinement but flag it (using score reasoning and pass=false)
			pass = false
		} else {
			// Auto pass -> Refined
			newStatus = StageRefined
			pass = true
		}
	}

	// 7. Save to DB
	modelVersion := model
	if capResp.Provider.ModelID != "" {
		modelVersion = capResp.Provider.ModelID
	}
	if modelVersion == "" && providerID > 0 {
		modelVersion = fmt.Sprintf("provider:%d", providerID)
	}
	err = s.docRepo.UpdateDocumentRefinement(ctx, datasetID, docKey, finalScore, eval.Reasoning, modelVersion, userID, newStatus)
	if err != nil {
		return nil, fmt.Errorf("更新文档评分元数据失败: %w", err)
	}
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)

	// 8. Write Audit Log
	detailsBytes, _ := json.Marshal(map[string]int{
		"accuracy":     eval.Accuracy,
		"completeness": eval.Completeness,
		"consistency":  eval.Consistency,
	})

	auditLog := &dbmodel.AnnotationLog{
		DocID:      docKey,
		UserID:     userID,
		Action:     "LLM_REFINE",
		FromStatus: doc.AnnotationStage,
		ToStatus:   newStatus,
		Score:      &finalScore,
		Details:    string(detailsBytes),
		Reasoning:  eval.Reasoning,
	}
	s.dbRepo.CreateAnnotationLog(ctx, auditLog)

	result := &LLMRefinementResponse{
		Score: finalScore,
		Details: struct {
			Accuracy     int `json:"accuracy"`
			Completeness int `json:"completeness"`
			Consistency  int `json:"consistency"`
		}{
			Accuracy:     eval.Accuracy,
			Completeness: eval.Completeness,
			Consistency:  eval.Consistency,
		},
		Reasoning: eval.Reasoning,
		Pass:      pass,
		DocStatus: newStatus,
	}

	return result, nil
}

func (s *LLMRefinementService) RollbackRefinement(ctx context.Context, docKey string, userID uint) error {
	return s.rollbackRefinement(ctx, nil, docKey, userID)
}

func (s *LLMRefinementService) RollbackRefinementInDataset(ctx context.Context, datasetID uint, docKey string, userID uint) error {
	return s.rollbackRefinement(ctx, &datasetID, docKey, userID)
}

func (s *LLMRefinementService) rollbackRefinement(ctx context.Context, datasetID *uint, docKey string, userID uint) error {
	doc, err := s.docRepo.FindActiveDocument(ctx, datasetID, docKey, 0)
	if err != nil || doc == nil {
		return fmt.Errorf("文档不存在")
	}

	err = s.docRepo.RollbackDocumentRefinement(ctx, datasetID, docKey)
	if err != nil {
		return err
	}
	_ = s.dbRepo.SyncDatasetCounters(ctx, s.docRepo, doc.DatasetID)

	auditLog := &dbmodel.AnnotationLog{
		DocID:      docKey,
		UserID:     userID,
		Action:     "LLM_REFINE_ROLLBACK",
		FromStatus: doc.AnnotationStage,
		ToStatus:   StageRefining,
	}
	s.dbRepo.CreateAnnotationLog(ctx, auditLog)

	return nil
}

func clampPercent(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

var llmRefinementNumberFieldPattern = regexp.MustCompile(`"([A-Za-z_]+)"\s*:\s*([0-9]{1,3})`)

func parseLLMRefinementEvaluation(raw string) (llmEvaluationResponse, error) {
	cleaned := cleanLLMJSONCandidate(raw)
	var eval llmEvaluationResponse
	if err := json.Unmarshal([]byte(cleaned), &eval); err == nil {
		return eval, nil
	}

	partial, ok := parsePartialLLMRefinementEvaluation(cleaned)
	if ok {
		return partial, nil
	}

	if cleaned != raw {
		if err := json.Unmarshal([]byte(raw), &eval); err == nil {
			return eval, nil
		}
	}
	return llmEvaluationResponse{}, fmt.Errorf("invalid JSON object")
}

func cleanLLMJSONCandidate(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)
	if idx := strings.Index(cleaned, "{"); idx != -1 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx != -1 {
		cleaned = cleaned[:idx+1]
	}
	return strings.TrimSpace(cleaned)
}

func parsePartialLLMRefinementEvaluation(raw string) (llmEvaluationResponse, bool) {
	values := map[string]int{}
	for _, match := range llmRefinementNumberFieldPattern.FindAllStringSubmatch(raw, -1) {
		if len(match) != 3 {
			continue
		}
		v, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		values[strings.ToLower(match[1])] = v
	}
	accuracy, okA := values["accuracy"]
	completeness, okC := values["completeness"]
	consistency, okS := values["consistency"]
	if !okA || !okC || !okS {
		return llmEvaluationResponse{}, false
	}
	reasoning := extractPartialJSONString(raw, "reasoning")
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		reasoning = "模型返回 JSON 不完整，已按已返回分数字段保留评分。"
	} else {
		reasoning += "（模型返回 JSON 不完整，已按已返回分数字段保留评分。）"
	}
	return llmEvaluationResponse{
		Accuracy:     accuracy,
		Completeness: completeness,
		Consistency:  consistency,
		Reasoning:    reasoning,
	}, true
}

func extractPartialJSONString(raw, field string) string {
	idx := strings.Index(raw, `"`+field+`"`)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(field)+2:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if rest == "" {
		return ""
	}
	if rest[0] != '"' {
		if end := strings.IndexAny(rest, ",}"); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	var b strings.Builder
	escaped := false
	for _, r := range rest[1:] {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			return b.String()
		}
		b.WriteRune(r)
	}
	return b.String()
}

func truncateForError(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
