package plugin

// TaskPlugin defines the interface for LLM task plugins.
type TaskPlugin interface {
	// TypeID returns the task type identifier, e.g. "qa_generation".
	TypeID() string
	// Name returns a human-readable name for this task type.
	Name() string
	// BuildPrompt generates a prompt from the input text and parameters.
	BuildPrompt(textSpan string, params map[string]interface{}) (string, error)
	// ParseResult parses the raw LLM output into a structured result.
	ParseResult(rawOutput string) (interface{}, error)
	// ValidateResult validates the parsed result.
	ValidateResult(result interface{}) error
}

// TaskRequest represents a request to execute an LLM task.
type TaskRequest struct {
	DocKey   string                 `json:"doc_key"`
	TextSpan string                 `json:"text_span"`
	TaskType string                 `json:"task_type"`
	Params   map[string]interface{} `json:"params,omitempty"`
}

// TaskTypeInfo describes a registered task type.
type TaskTypeInfo struct {
	TypeID      string `json:"type_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
