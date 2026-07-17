package service

import (
	"strings"
	"testing"
)

func TestParseLLMRefinementEvaluationStrictJSON(t *testing.T) {
	got, err := parseLLMRefinementEvaluation(`{
		"accuracy": 91,
		"completeness": 82,
		"consistency": 73,
		"reasoning": "整体可用，但证据字段需要更精确"
	}`)
	if err != nil {
		t.Fatalf("parse strict JSON: %v", err)
	}
	if got.Accuracy != 91 || got.Completeness != 82 || got.Consistency != 73 {
		t.Fatalf("scores = %+v", got)
	}
	if got.Reasoning == "" {
		t.Fatal("reasoning should be kept")
	}
}

func TestParseLLMRefinementEvaluationPartialReasoning(t *testing.T) {
	got, err := parseLLMRefinementEvaluation(`{
		"accuracy": 41,
		"completeness": 52,
		"consistency": 63,
		"reasoning": "存在多处事实错误：将一审判决结果类型误标为部分改判；财产性义务金额只`)
	if err != nil {
		t.Fatalf("parse partial JSON: %v", err)
	}
	if got.Accuracy != 41 || got.Completeness != 52 || got.Consistency != 63 {
		t.Fatalf("scores = %+v", got)
	}
	if !strings.Contains(got.Reasoning, "JSON 不完整") {
		t.Fatalf("reasoning should mention partial recovery, got %q", got.Reasoning)
	}
}

func TestParseLLMRefinementEvaluationPartialWithoutScoresFails(t *testing.T) {
	_, err := parseLLMRefinementEvaluation(`{ "reasoning": "存在多处事实错误：财产性义务金额只`)
	if err == nil {
		t.Fatal("expected error when partial JSON has no scores")
	}
}
