package builtin

import (
	"strings"
	"testing"
)

func TestQATaskPlugin_BuildPrompt(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	prompt, err := p.BuildPrompt("被告人张三于2024年1月犯罪", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "被告人张三") {
		t.Error("prompt should contain the input text")
	}
	if !strings.Contains(prompt, "禁止引入") {
		t.Error("prompt should contain constraint about not using external info")
	}
}

func TestQATaskPlugin_BuildPromptEmpty(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	_, err := p.BuildPrompt("  ", nil)
	if err == nil {
		t.Fatal("expected error for empty text span")
	}
}

func TestQATaskPlugin_BuildPromptWithMaxItems(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	prompt, err := p.BuildPrompt("some text", map[string]interface{}{"max_items": float64(3)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "3") {
		t.Error("prompt should contain the max_items value")
	}
}

func TestQATaskPlugin_ParseResultValid(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	raw := `[{"question":"什么时候?","answer":"2024年1月"},{"question":"谁?","answer":"张三"}]`
	result, err := p.ParseResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pairs, ok := result.([]QAPair)
	if !ok {
		t.Fatal("expected []QAPair")
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs, got %d", len(pairs))
	}
}

func TestQATaskPlugin_ParseResultWithMarkdown(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	raw := "```json\n[{\"question\":\"Q\",\"answer\":\"A\"}]\n```"
	result, err := p.ParseResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pairs := result.([]QAPair)
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(pairs))
	}
}

func TestQATaskPlugin_ParseResultInvalid(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	_, err := p.ParseResult("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "qa_generation") {
		t.Error("error should contain task type ID")
	}
}

func TestQATaskPlugin_ValidateResultValid(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	pairs := []QAPair{{Question: "Q", Answer: "A"}}
	if err := p.ValidateResult(pairs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQATaskPlugin_ValidateResultEmpty(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	if err := p.ValidateResult([]QAPair{}); err == nil {
		t.Fatal("expected error for empty pairs")
	}
}

func TestQATaskPlugin_ValidateResultEmptyQuestion(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	pairs := []QAPair{{Question: "", Answer: "A"}}
	if err := p.ValidateResult(pairs); err == nil {
		t.Fatal("expected error for empty question")
	}
}

func TestQATaskPlugin_ValidateResultWrongType(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	if err := p.ValidateResult("not a slice"); err == nil {
		t.Fatal("expected error for wrong type")
	}
}

func TestQATaskPlugin_TypeIDAndName(t *testing.T) {
	p := &QAGenerationTaskPlugin{}
	if p.TypeID() != "qa_generation" {
		t.Errorf("expected 'qa_generation', got %q", p.TypeID())
	}
	if p.Name() != "QA Generation" {
		t.Errorf("expected 'QA Generation', got %q", p.Name())
	}
}
