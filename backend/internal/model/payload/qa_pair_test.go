package payload

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Filter rules: the rules that differ across the three original implementations
// ---------------------------------------------------------------------------

func TestParseQAPairs_FiltersEmptyQuestion(t *testing.T) {
	input := []QAPair{
		{Question: "", Answer: "valid answer"},
		{Question: "valid question here", Answer: "valid answer"},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 || got[0].Question != "valid question here" {
		t.Errorf("expected 1 pair after empty-Q filter, got %v", got)
	}
}

func TestParseQAPairs_FiltersEmptyAnswer(t *testing.T) {
	input := []QAPair{
		{Question: "valid question here", Answer: ""},
		{Question: "another question here", Answer: "answer"},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 || got[0].Question != "another question here" {
		t.Errorf("expected 1 pair after empty-A filter, got %v", got)
	}
}

func TestParseQAPairs_FiltersShortQuestion(t *testing.T) {
	// "hi" is 2 runes < 5; "valid question" is 14 runes
	input := []QAPair{
		{Question: "hi", Answer: "answer"},
		{Question: "valid question here", Answer: "answer"},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 || got[0].Question != "valid question here" {
		t.Errorf("expected 1 pair after short-Q filter, got %v", got)
	}
}

func TestParseQAPairs_DeduplicatesByQuestion(t *testing.T) {
	input := []QAPair{
		{Question: "same question here", Answer: "first answer"},
		{Question: "same question here", Answer: "second answer"},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Errorf("expected 1 pair after dedup, got %d", len(got))
	}
	if got[0].Answer != "first answer" {
		t.Errorf("expected first occurrence kept, got answer=%q", got[0].Answer)
	}
}

func TestParseQAPairs_DeduplicatesByQuestionKey(t *testing.T) {
	input := []QAPair{
		{QuestionKey: "facts", Question: "what happened here", Answer: "first answer"},
		{QuestionKey: "Facts", Question: "describe the basic facts", Answer: "second answer"},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 pair after question_key dedup, got %d", len(got))
	}
	if got[0].Answer != "first answer" {
		t.Errorf("expected first occurrence kept, got answer=%q", got[0].Answer)
	}
}

func TestParseQAPairs_TrimsWhitespace(t *testing.T) {
	input := []QAPair{
		{Question: "  valid question here  ", Answer: "  valid answer  "},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(got))
	}
	if got[0].Question != "valid question here" || got[0].Answer != "valid answer" {
		t.Errorf("expected trimmed Q/A, got Q=%q A=%q", got[0].Question, got[0].Answer)
	}
}

// ---------------------------------------------------------------------------
// Type conversion: []interface{} of maps (decoded-JSON path)
// ---------------------------------------------------------------------------

func TestParseQAPairs_FromDecodedMapSlice(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"question": "what is justice", "answer": "fairness and equity"},
		map[string]interface{}{"question": "what is law", "answer": "rules of society"},
	}
	got := ParseQAPairs(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs from interface slice, got %d", len(got))
	}
	if got[0].Question != "what is justice" {
		t.Errorf("unexpected first question: %q", got[0].Question)
	}
}

func TestParseQAPairs_FromDecodedMapSlice_PreservesAllFields(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{
			"question":     "what is due process",
			"answer":       "legal rights in proceedings",
			"source":       "llm",
			"confirmed":    true,
			"question_key": "facts",
			"category":     "事实",
			"evidence":     "evidence text",
			"confidence":   0.8,
			"reason":       "grounded in text",
			"span_text":    "some text span",
		},
	}
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(got))
	}
	p := got[0]
	if p.Source != "llm" || !p.Confirmed || p.SpanText != "some text span" ||
		p.QuestionKey != "facts" || p.Category != "事实" || p.Evidence != "evidence text" ||
		p.Confidence == nil || *p.Confidence != 0.8 || p.Reason != "grounded in text" {
		t.Errorf("fields not preserved: %+v", p)
	}
}

// ---------------------------------------------------------------------------
// Type conversion: []interface{} (llm_service original path)
// ---------------------------------------------------------------------------

func TestParseQAPairs_FromInterfaceSlice(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"question": "what is habeas corpus", "answer": "right to appear in court"},
		map[string]interface{}{"question": "ok", "answer": "too short question filtered"},
	}
	got := ParseQAPairs(input)
	// "ok" is 2 runes < 5, should be filtered
	if len(got) != 1 {
		t.Fatalf("expected 1 pair (short Q filtered), got %d: %v", len(got), got)
	}
	if got[0].Question != "what is habeas corpus" {
		t.Errorf("unexpected question: %q", got[0].Question)
	}
}

// ---------------------------------------------------------------------------
// String parsing: LLM response (auto_annotation_service original path)
// ---------------------------------------------------------------------------

func TestParseQAPairs_FromPlainJSONString(t *testing.T) {
	input := `[{"question":"what is due diligence","answer":"thorough investigation"},{"question":"what is mens rea","answer":"criminal intent"}]`
	got := ParseQAPairs(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 pairs from JSON string, got %d", len(got))
	}
}

func TestParseQAPairs_FromMarkdownCodeBlock(t *testing.T) {
	input := "```json\n[{\"question\":\"what is tort law\",\"answer\":\"civil wrongs causing harm\"}]\n```"
	got := ParseQAPairs(input)
	if len(got) != 1 || got[0].Question != "what is tort law" {
		t.Errorf("expected 1 pair from markdown code block, got %v", got)
	}
}

func TestParseQAPairs_FromMarkdownCodeBlockPreservesExtendedFields(t *testing.T) {
	input := "```json\n[{\"question_key\":\"issues\",\"category\":\"争议焦点\",\"question\":\"本案争议焦点是什么？\",\"answer\":\"是否应支付经济补偿。\",\"evidence\":\"本案争议焦点为...\",\"confidence\":0.95,\"reason\":\"原文明确归纳。\"}]\n```"
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(got))
	}
	if got[0].QuestionKey != "issues" || got[0].Category != "争议焦点" || got[0].Evidence == "" || got[0].Reason == "" {
		t.Fatalf("extended fields not preserved: %+v", got[0])
	}
	if got[0].Confidence == nil || *got[0].Confidence != 0.95 {
		t.Fatalf("expected numeric confidence 0.95, got %+v", got[0].Confidence)
	}
}

func TestParseQAPairs_StringConfidenceLabel(t *testing.T) {
	input := `[{"question":"本案争议焦点是什么？","answer":"是否应支付经济补偿。","confidence":"high"}]`
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(got))
	}
	if got[0].Confidence == nil || *got[0].Confidence != 0.9 {
		t.Fatalf("expected high confidence to normalize to 0.9, got %+v", got[0].Confidence)
	}
}

func TestParseQAPairs_RepairsBareQuotesInsideJSONString(t *testing.T) {
	input := `[{"question":"关键证据及采信情况是什么？","answer":"法院采信岗位外包并拟将劳动者"优化离岗"的证据。"}]`
	got := ParseQAPairs(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 pair after repairing bare quotes, got %d", len(got))
	}
	if got[0].Answer != `法院采信岗位外包并拟将劳动者"优化离岗"的证据。` {
		t.Fatalf("unexpected repaired answer: %q", got[0].Answer)
	}
}

func TestParseQAPairs_EmptyStringReturnsNil(t *testing.T) {
	if got := ParseQAPairs(""); got != nil {
		t.Errorf("expected nil for empty string, got %v", got)
	}
}

func TestParseQAPairs_InvalidJSONReturnsNil(t *testing.T) {
	if got := ParseQAPairs("not json at all"); got != nil {
		t.Errorf("expected nil for invalid JSON, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// nil / empty input
// ---------------------------------------------------------------------------

func TestParseQAPairs_NilReturnsNil(t *testing.T) {
	if got := ParseQAPairs(nil); got != nil {
		t.Errorf("expected nil for nil input, got %v", got)
	}
}

func TestParseQAPairs_EmptySliceReturnsNil(t *testing.T) {
	if got := ParseQAPairs([]QAPair{}); got != nil {
		t.Errorf("expected nil for empty slice, got %v", got)
	}
}
