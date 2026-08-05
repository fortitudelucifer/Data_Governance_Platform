package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"text-annotation-platform/internal/cache"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"

	"gorm.io/gorm"
)

const systemPromptTTL = 24 * time.Hour
const AutoPromptTaskTextQA = "text_auto_qa"
const AutoPromptTaskTextJudge = "text_ai_judge"

const defaultAutoPromptUserTemplate = `请基于以下正文生成高质量问答对，仅返回 JSON 数组。

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
{{text}}`

const defaultAutoPromptOutputSchema = `{"type":"array","items":{"type":"object","properties":{"question_key":{"type":"string"},"category":{"type":"string"},"question":{"type":"string"},"answer":{"type":"string"},"evidence":{"type":"string"},"span_text":{"type":"string"},"confidence":{"type":"number"},"reason":{"type":"string"}},"required":["question_key","category","question","answer","evidence"]}}`

const defaultAutoPromptGuide = "user_prompt_template 必须包含 {{text}}；建议使用固定 question_key 枚举，并要求 question_key 对应的 question 原样输出。evidence/span_text 应为答案依据对应原文片段，供 Judge Agent 按 key 合并与人工复核。"

const defaultJudgePromptUserTemplate = `请评审以下正文和多路自动标注候选，判断是否需要人工审核，并给出可人工采纳的合并建议。

要求：
1. 只返回 JSON 对象，不要输出 Markdown。
2. decision 只能是 pass、merge、needs_review 之一。
3. candidate_scores 按 run_id 逐一评分，指出风险。
4. merged_qa_pairs 必须优先按 question_key 合并；同一 question_key 选择证据最充分、答案最稳妥的结果。
5. merged_qa_pairs 中每条必须包含 source_candidate_run_ids，记录来源 run_id。
6. 若模型间冲突、证据不足、span_text 无法定位、候选为空或失败比例高，应输出 needs_review。

正文：
{{text}}

候选：
{{candidates}}`

const defaultJudgePromptOutputSchema = `{"type":"object","properties":{"overall_score":{"type":"number"},"decision":{"type":"string","enum":["pass","merge","needs_review"]},"summary":{"type":"string"},"review_reasons":{"type":"array","items":{"type":"string"}},"candidate_scores":{"type":"array","items":{"type":"object","properties":{"run_id":{"type":"string"},"score":{"type":"number"},"decision":{"type":"string"},"summary":{"type":"string"},"risks":{"type":"array","items":{"type":"string"}},"qa_count":{"type":"number"}}}},"merged_qa_pairs":{"type":"array","items":{"type":"object","properties":{"question_key":{"type":"string"},"category":{"type":"string"},"question":{"type":"string"},"answer":{"type":"string"},"evidence":{"type":"string"},"span_text":{"type":"string"},"confidence":{"type":"number"},"reason":{"type":"string"},"source_candidate_run_ids":{"type":"array","items":{"type":"string"}}}}}}}`

const defaultJudgePromptGuide = "Judge 模板必须包含 {{text}} 与 {{candidates}}；输出 JSON 对象，decision 仅允许 pass/merge/needs_review；merged_qa_pairs 必须包含 source_candidate_run_ids，供人工采纳后追溯来源。"

const defaultJudgeSystemPrompt = `你是文本自动标注结果的 Judge Agent，负责审查多路模型候选问答是否可信、是否需要人工审核，并给出可追溯的合并建议。

你的职责：
1. 对每一路候选按证据充分性、问题稳定性、答案准确性和原文可核验性评分。
2. 优先使用 question_key 合并同类问答，不要只按自然语言 question 判断。
3. 合并建议必须保留来源 candidate run_id。
4. 你只能提出建议，不能声明已自动通过人工审核。`

// SystemPromptService provides CRUD operations for case-type-specific system prompts.
type SystemPromptService struct {
	dbRepo *repository.DB
	cache     *cache.Cache // nil = no Redis
}

// NewSystemPromptService creates a new SystemPromptService.
func NewSystemPromptService(dbRepo *repository.DB) *SystemPromptService {
	return &SystemPromptService{dbRepo: dbRepo}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *SystemPromptService) WithCache(c *cache.Cache) *SystemPromptService {
	s.cache = c
	return s
}

// GetByCaseType returns the system prompt for the given case type.
// Result is cached under "system_prompts:{caseType}" for 24 hours (data rarely changes).
func (s *SystemPromptService) GetByCaseType(ctx context.Context, caseType string) (*dbmodel.SystemPrompt, error) {
	key := "system_prompts:" + caseType
	if s.cache != nil {
		var v dbmodel.SystemPrompt
		if hit, _ := s.cache.GetJSON(ctx, key, &v); hit {
			return &v, nil
		}
	}
	var prompt dbmodel.SystemPrompt
	if err := s.dbRepo.DB.Where("case_type = ?", caseType).First(&prompt).Error; err != nil {
		return nil, fmt.Errorf("System Prompt 不存在 (case_type: %s): %w", caseType, err)
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, key, &prompt, systemPromptTTL)
	}
	return &prompt, nil
}

// GetOrDefault returns the prompt for the given case type, falling back to "criminal" if not found.
func (s *SystemPromptService) GetOrDefault(ctx context.Context, caseType string) (*dbmodel.SystemPrompt, error) {
	if caseType == "" {
		caseType = "criminal"
	}
	prompt, err := s.GetByCaseType(ctx, caseType)
	if err == nil {
		return prompt, nil
	}
	// Fallback to criminal default
	return s.GetByCaseType(ctx, "criminal")
}

// List returns all system prompts.
func (s *SystemPromptService) List(ctx context.Context) ([]dbmodel.SystemPrompt, error) {
	var prompts []dbmodel.SystemPrompt
	if err := s.dbRepo.DB.Order("case_type").Find(&prompts).Error; err != nil {
		return nil, fmt.Errorf("查询 System Prompt 列表失败: %w", err)
	}
	return prompts, nil
}

// Update modifies the content of an existing system prompt by case type.
func (s *SystemPromptService) Update(ctx context.Context, caseType string, content string) error {
	if content == "" {
		return fmt.Errorf("System Prompt 内容不能为空")
	}
	result := s.dbRepo.DB.Model(&dbmodel.SystemPrompt{}).Where("case_type = ?", caseType).Update("content", content)
	if result.Error != nil {
		return fmt.Errorf("更新 System Prompt 失败: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("System Prompt 不存在 (case_type: %s)", caseType)
	}
	if s.cache != nil {
		s.cache.Delete(ctx, "system_prompts:"+caseType)
	}
	return nil
}

// Create inserts a new system prompt for a custom case type.
func (s *SystemPromptService) Create(ctx context.Context, caseType, name, content string) (*dbmodel.SystemPrompt, error) {
	if content == "" {
		return nil, fmt.Errorf("System Prompt 内容不能为空")
	}
	// Check for duplicate
	var existing dbmodel.SystemPrompt
	if err := s.dbRepo.DB.Where("case_type = ?", caseType).First(&existing).Error; err == nil {
		return nil, fmt.Errorf("案件类型 '%s' 的 Prompt 已存在", caseType)
	} else if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("查询 System Prompt 失败: %w", err)
	}

	prompt := dbmodel.SystemPrompt{
		CaseType: caseType,
		Name:     name,
		Content:  content,
	}
	if err := s.dbRepo.DB.Create(&prompt).Error; err != nil {
		return nil, fmt.Errorf("创建 System Prompt 失败: %w", err)
	}
	return &prompt, nil
}

// ListAutoPromptTemplates returns auto-annotation prompt templates.
func (s *SystemPromptService) ListAutoPromptTemplates(ctx context.Context, caseType string, includeDisabled bool) ([]dbmodel.AutoPromptTemplate, error) {
	db := s.dbRepo.DB.WithContext(ctx).Order("case_type ASC, enabled DESC, updated_at DESC, id ASC")
	if caseType != "" {
		db = db.Where("case_type = ?", caseType)
	}
	if !includeDisabled {
		db = db.Where("enabled = ?", true)
	}
	var templates []dbmodel.AutoPromptTemplate
	if err := db.Find(&templates).Error; err != nil {
		return nil, fmt.Errorf("查询自动标注 Prompt 模板失败: %w", err)
	}
	return templates, nil
}

// GetAutoPromptTemplate returns a template by ID.
func (s *SystemPromptService) GetAutoPromptTemplate(ctx context.Context, id uint) (*dbmodel.AutoPromptTemplate, error) {
	var tpl dbmodel.AutoPromptTemplate
	if err := s.dbRepo.DB.WithContext(ctx).First(&tpl, id).Error; err != nil {
		return nil, fmt.Errorf("自动标注 Prompt 模板不存在 (id: %d): %w", id, err)
	}
	return &tpl, nil
}

// CreateAutoPromptTemplate creates a system/user auto-annotation prompt template.
func (s *SystemPromptService) CreateAutoPromptTemplate(ctx context.Context, tpl dbmodel.AutoPromptTemplate) (*dbmodel.AutoPromptTemplate, error) {
	normalizeAutoPromptTemplate(&tpl)
	if err := validateAutoPromptTemplate(tpl); err != nil {
		return nil, err
	}
	if tpl.Version <= 0 {
		tpl.Version = 1
	}
	if err := s.dbRepo.DB.WithContext(ctx).Create(&tpl).Error; err != nil {
		return nil, fmt.Errorf("创建自动标注 Prompt 模板失败: %w", err)
	}
	return &tpl, nil
}

// EnsureDefaultJudgePromptTemplates inserts a basic Judge Agent prompt for each
// existing case type that does not already have one. It never overwrites user
// edits, so admins can freely tune the seeded prompt afterwards.
func (s *SystemPromptService) EnsureDefaultJudgePromptTemplates(ctx context.Context) error {
	var prompts []dbmodel.SystemPrompt
	if err := s.dbRepo.DB.WithContext(ctx).Order("case_type ASC").Find(&prompts).Error; err != nil {
		return fmt.Errorf("查询 System Prompt 列表失败: %w", err)
	}
	for _, p := range prompts {
		var count int64
		if err := s.dbRepo.DB.WithContext(ctx).Model(&dbmodel.AutoPromptTemplate{}).
			Where("case_type = ? AND task_type = ?", p.CaseType, AutoPromptTaskTextJudge).
			Count(&count).Error; err != nil {
			return fmt.Errorf("查询 Judge Prompt 模板失败: %w", err)
		}
		if count > 0 {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.CaseType
		}
		tpl := dbmodel.AutoPromptTemplate{
			Name:               name + " Judge Agent 基础评审",
			CaseType:           p.CaseType,
			TaskType:           AutoPromptTaskTextJudge,
			SystemPrompt:       defaultJudgeSystemPrompt,
			UserPromptTemplate: defaultJudgePromptUserTemplate,
			OutputSchema:       defaultJudgePromptOutputSchema,
			Guide:              defaultJudgePromptGuide,
			Enabled:            true,
			Version:            1,
		}
		normalizeAutoPromptTemplate(&tpl)
		if err := validateAutoPromptTemplate(tpl); err != nil {
			return err
		}
		if err := s.dbRepo.DB.WithContext(ctx).Create(&tpl).Error; err != nil {
			return fmt.Errorf("创建 Judge Prompt 模板失败: %w", err)
		}
	}
	return nil
}

// UpdateAutoPromptTemplate updates a template and increments its version.
func (s *SystemPromptService) UpdateAutoPromptTemplate(ctx context.Context, id uint, next dbmodel.AutoPromptTemplate) (*dbmodel.AutoPromptTemplate, error) {
	var current dbmodel.AutoPromptTemplate
	if err := s.dbRepo.DB.WithContext(ctx).First(&current, id).Error; err != nil {
		return nil, fmt.Errorf("自动标注 Prompt 模板不存在 (id: %d): %w", id, err)
	}
	current.Name = next.Name
	current.CaseType = next.CaseType
	current.TaskType = next.TaskType
	current.SystemPrompt = next.SystemPrompt
	current.UserPromptTemplate = next.UserPromptTemplate
	current.OutputSchema = next.OutputSchema
	current.Guide = next.Guide
	current.Enabled = next.Enabled
	current.Version += 1
	normalizeAutoPromptTemplate(&current)
	if err := validateAutoPromptTemplate(current); err != nil {
		return nil, err
	}
	if err := s.dbRepo.DB.WithContext(ctx).Save(&current).Error; err != nil {
		return nil, fmt.Errorf("更新自动标注 Prompt 模板失败: %w", err)
	}
	return &current, nil
}

func normalizeAutoPromptTemplate(tpl *dbmodel.AutoPromptTemplate) {
	tpl.Name = strings.TrimSpace(tpl.Name)
	tpl.CaseType = strings.TrimSpace(tpl.CaseType)
	tpl.TaskType = strings.TrimSpace(tpl.TaskType)
	tpl.SystemPrompt = strings.TrimSpace(tpl.SystemPrompt)
	tpl.UserPromptTemplate = strings.TrimSpace(tpl.UserPromptTemplate)
	tpl.OutputSchema = strings.TrimSpace(tpl.OutputSchema)
	tpl.Guide = strings.TrimSpace(tpl.Guide)
	if tpl.CaseType == "" {
		tpl.CaseType = "criminal"
	}
	if tpl.TaskType == "" {
		tpl.TaskType = AutoPromptTaskTextQA
	}
	if tpl.UserPromptTemplate == "" {
		if tpl.TaskType == AutoPromptTaskTextJudge {
			tpl.UserPromptTemplate = defaultJudgePromptUserTemplate
		} else {
			tpl.UserPromptTemplate = defaultAutoPromptUserTemplate
		}
	}
	if tpl.OutputSchema == "" {
		if tpl.TaskType == AutoPromptTaskTextJudge {
			tpl.OutputSchema = defaultJudgePromptOutputSchema
		} else {
			tpl.OutputSchema = defaultAutoPromptOutputSchema
		}
	}
	if tpl.Guide == "" {
		if tpl.TaskType == AutoPromptTaskTextJudge {
			tpl.Guide = defaultJudgePromptGuide
		} else {
			tpl.Guide = defaultAutoPromptGuide
		}
	}
}

func validateAutoPromptTemplate(tpl dbmodel.AutoPromptTemplate) error {
	if tpl.Name == "" {
		return fmt.Errorf("模板名称不能为空")
	}
	if tpl.SystemPrompt == "" {
		return fmt.Errorf("system prompt 不能为空")
	}
	if !strings.Contains(tpl.UserPromptTemplate, "{{text}}") {
		return fmt.Errorf("user prompt 模板必须包含 {{text}} 占位符")
	}
	if tpl.TaskType == AutoPromptTaskTextJudge && !strings.Contains(tpl.UserPromptTemplate, "{{candidates}}") {
		return fmt.Errorf("Judge prompt 模板必须包含 {{candidates}} 占位符")
	}
	return nil
}
