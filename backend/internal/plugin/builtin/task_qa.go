package builtin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// QAGenerationTaskPlugin implements TaskPlugin for question-answer pair generation.
type QAGenerationTaskPlugin struct{}

func (p *QAGenerationTaskPlugin) TypeID() string {
	return "qa_generation"
}

func (p *QAGenerationTaskPlugin) Name() string {
	return "QA Generation"
}

// QAPair represents a single question-answer pair.
type QAPair struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// BuildPrompt creates a prompt that constrains the LLM to only use the provided text.
func (p *QAGenerationTaskPlugin) BuildPrompt(textSpan string, params map[string]interface{}) (string, error) {
	if strings.TrimSpace(textSpan) == "" {
		return "", fmt.Errorf("task 'qa_generation': text span cannot be empty")
	}

	maxItems := 5
	if v, ok := params["max_items"]; ok {
		switch val := v.(type) {
		case float64:
			maxItems = int(val)
		case int:
			maxItems = val
		}
	}

	prompt := fmt.Sprintf(`你是一个专业的问答对生成助手。请严格基于以下提供的文本内容生成问答对，禁止引入任何外部信息。

要求：
1. 仅基于提供的文本生成问答对
2. 生成最多 %d 个问答对
3. 每个问答对包含一个问题和一个答案
4. 以 JSON 数组格式返回，每个元素包含 "question" 和 "answer" 字段

文本内容：
%s

请以如下 JSON 格式返回：
[{"question": "问题1", "answer": "答案1"}, {"question": "问题2", "answer": "答案2"}]`, maxItems, textSpan)

	return prompt, nil
}

// ParseResult parses the raw LLM output as a JSON array of QAPair objects.
func (p *QAGenerationTaskPlugin) ParseResult(rawOutput string) (interface{}, error) {
	trimmed := strings.TrimSpace(rawOutput)

	// Try to extract JSON array from the output (LLM may wrap it in markdown code blocks)
	if idx := strings.Index(trimmed, "["); idx >= 0 {
		if endIdx := strings.LastIndex(trimmed, "]"); endIdx > idx {
			trimmed = trimmed[idx : endIdx+1]
		}
	}

	var pairs []QAPair
	if err := json.Unmarshal([]byte(trimmed), &pairs); err != nil {
		return nil, fmt.Errorf("task 'qa_generation' parse failed: invalid JSON array: %w", err)
	}
	return pairs, nil
}

// ValidateResult checks that the parsed result is a non-empty slice of QAPairs
// with non-empty question and answer fields.
func (p *QAGenerationTaskPlugin) ValidateResult(result interface{}) error {
	pairs, ok := result.([]QAPair)
	if !ok {
		return fmt.Errorf("task 'qa_generation' validation failed: expected []QAPair, got %T", result)
	}
	if len(pairs) == 0 {
		return fmt.Errorf("task 'qa_generation' validation failed: no QA pairs generated")
	}
	for i, pair := range pairs {
		if strings.TrimSpace(pair.Question) == "" {
			return fmt.Errorf("task 'qa_generation' validation failed: QA pair %d has empty question", i)
		}
		if strings.TrimSpace(pair.Answer) == "" {
			return fmt.Errorf("task 'qa_generation' validation failed: QA pair %d has empty answer", i)
		}
	}
	return nil
}
