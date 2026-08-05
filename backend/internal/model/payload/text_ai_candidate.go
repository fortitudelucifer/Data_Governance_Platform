package payload

import "time"

const CollTextAICandidate = "text_ai_candidates"

// TextAICandidate stores one provider/model's proposed QA set for a text
// document. It is intentionally separate from Document.data.qa_pairs so model
// comparison never overwrites the human refinement draft.
type TextAICandidate struct {
	ID                   string                 `json:"id"`
	RunID                string                 `json:"run_id"`
	TraceID              string                 `json:"trace_id"`
	DatasetID            uint                   `json:"dataset_id"`
	DocKey               string                 `json:"doc_key"`
	TextField            string                 `json:"text_field"`
	Provider             ModelProviderRef       `json:"provider"`
	PromptTemplateID     uint                   `json:"prompt_template_id,omitempty"`
	PromptTemplateName   string                 `json:"prompt_template_name,omitempty"`
	PromptVersion        int                    `json:"prompt_version,omitempty"`
	SystemPromptSnapshot string                 `json:"system_prompt_snapshot,omitempty"`
	UserPromptSnapshot   string                 `json:"user_prompt_snapshot,omitempty"`
	GenerationParams     map[string]interface{} `json:"generation_params,omitempty"`
	Status               string                 `json:"status"` // success | failed | timeout
	Error                string                 `json:"error,omitempty"`
	QAPairs              []QAPair               `json:"qa_pairs"`
	RawResponse          interface{}            `json:"raw_response,omitempty"`
	LatencyMs            int64                  `json:"latency_ms"`
	Cost                 float64                `json:"cost,omitempty"`
	EstimatedCost        float64                `json:"estimated_cost,omitempty"`
	CreatedBy            uint                   `json:"created_by"`
	CreatedAt            time.Time              `json:"created_at"`
	AdoptedCount         int                    `json:"adopted_count,omitempty"`
	LastAdoptedBy        uint                   `json:"last_adopted_by,omitempty"`
	LastAdoptedAt        *time.Time             `json:"last_adopted_at,omitempty"`
}
