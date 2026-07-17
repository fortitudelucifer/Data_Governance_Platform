package payload

import "time"

const CollTextAIJudgeRun = "text_ai_judge_runs"

// TextAIJudgeRun stores one Judge Agent evaluation over a set of text
// auto-annotation candidate runs. It is advisory until a human adopts it.
type TextAIJudgeRun struct {
	ID                   string                      `json:"id"`
	RunID                string                      `json:"run_id"`
	TraceID              string                      `json:"trace_id"`
	DatasetID            uint                        `json:"dataset_id"`
	DocKey               string                      `json:"doc_key"`
	TextField            string                      `json:"text_field"`
	CandidateRunIDs      []string                    `json:"candidate_run_ids"`
	Provider             ModelProviderRef            `json:"provider"`
	PromptTemplateID     uint                        `json:"prompt_template_id,omitempty"`
	PromptTemplateName   string                      `json:"prompt_template_name,omitempty"`
	PromptVersion        int                         `json:"prompt_version,omitempty"`
	SystemPromptSnapshot string                      `json:"system_prompt_snapshot,omitempty"`
	UserPromptSnapshot   string                      `json:"user_prompt_snapshot,omitempty"`
	GenerationParams     map[string]interface{}      `json:"generation_params,omitempty"`
	Status               string                      `json:"status"`
	Error                string                      `json:"error,omitempty"`
	OverallScore         *float64                    `json:"overall_score,omitempty"`
	Decision             string                      `json:"decision,omitempty"`
	Summary              string                      `json:"summary,omitempty"`
	ReviewReasons        []string                    `json:"review_reasons,omitempty"`
	CandidateScores      []TextAIJudgeCandidateScore `json:"candidate_scores,omitempty"`
	MergedQAPairs        []QAPair                    `json:"merged_qa_pairs,omitempty"`
	RawResponse          interface{}                 `json:"raw_response,omitempty"`
	LatencyMs            int64                       `json:"latency_ms"`
	Cost                 float64                     `json:"cost,omitempty"`
	EstimatedCost        float64                     `json:"estimated_cost,omitempty"`
	CreatedBy            uint                        `json:"created_by"`
	CreatedAt            time.Time                   `json:"created_at"`
	AdoptedCount         int                         `json:"adopted_count,omitempty"`
	LastAdoptedBy        uint                        `json:"last_adopted_by,omitempty"`
	LastAdoptedAt        *time.Time                  `json:"last_adopted_at,omitempty"`
}

type TextAIJudgeCandidateScore struct {
	RunID    string   `json:"run_id"`
	Score    *float64 `json:"score,omitempty"`
	Decision string   `json:"decision,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Risks    []string `json:"risks,omitempty"`
	QACount  int      `json:"qa_count,omitempty"`
}
