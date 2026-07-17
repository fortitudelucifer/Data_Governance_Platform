package builtin

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"text-annotation-platform/internal/plugin"
)

// Feature: text-annotation-platform, Property 6: 抽样策略非法参数错误
// Validates: Requirements 17.6

// TestRandomSampling_InvalidParams_Property verifies that invalid params always produce errors.
func TestRandomSampling_InvalidParams_Property(t *testing.T) {
	s := &RandomSamplingStrategy{}

	invalidParams := []map[string]interface{}{
		{},                          // missing ratio
		{"ratio": "not_a_number"},   // wrong type
		{"ratio": float64(0)},       // zero
		{"ratio": float64(-0.5)},    // negative
		{"ratio": float64(1.5)},     // > 1
		{"ratio": float64(-1)},      // negative
	}

	for i, params := range invalidParams {
		err := s.ValidateParams(params)
		if err == nil {
			t.Errorf("case %d: expected error for params %v", i, params)
		}
	}
}

// TestRuleSampling_InvalidParams_Property verifies that invalid params always produce errors.
func TestRuleSampling_InvalidParams_Property(t *testing.T) {
	s := &RuleSamplingStrategy{}

	invalidParams := []map[string]interface{}{
		{},                          // missing keywords
		{"keywords": ""},            // empty string
		{"keywords": []interface{}{}}, // empty array
		{"keywords": 123},           // wrong type
	}

	for i, params := range invalidParams {
		err := s.ValidateParams(params)
		if err == nil {
			t.Errorf("case %d: expected error for params %v", i, params)
		}
	}
}

// TestInvalidParamsError_Property runs a property test with random invalid ratio values.
func TestInvalidParamsError_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	s := &RandomSamplingStrategy{}

	for i := 0; i < 100; i++ {
		// Generate invalid ratios: negative or > 1
		var ratio float64
		if rng.Intn(2) == 0 {
			ratio = -rng.Float64() * 10 // negative
		} else {
			ratio = 1.0 + rng.Float64()*10 // > 1
		}

		params := map[string]interface{}{"ratio": ratio}
		err := s.ValidateParams(params)
		if err == nil {
			t.Errorf("iteration %d: expected error for ratio %v", i, ratio)
		}
		if !strings.Contains(err.Error(), "ratio") {
			t.Errorf("iteration %d: error should mention 'ratio': %v", i, err)
		}
	}
}

func TestRandomSampling_ValidRatio(t *testing.T) {
	s := &RandomSamplingStrategy{}
	segments := makeSegments(10)

	result, err := s.Sample(segments, map[string]interface{}{"ratio": float64(0.5)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 segments, got %d", len(result))
	}
}

func TestRandomSampling_RatioOne(t *testing.T) {
	s := &RandomSamplingStrategy{}
	segments := makeSegments(5)

	result, err := s.Sample(segments, map[string]interface{}{"ratio": float64(1.0)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("expected 5 segments, got %d", len(result))
	}
}

func TestRandomSampling_EmptySegments(t *testing.T) {
	s := &RandomSamplingStrategy{}
	result, err := s.Sample(nil, map[string]interface{}{"ratio": float64(0.5)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty segments, got %v", result)
	}
}

func TestRuleSampling_MatchKeywords(t *testing.T) {
	s := &RuleSamplingStrategy{}
	segments := []plugin.SegmentUnit{
		{DocKey: "d1", SegmentIdx: 0, Text: "被告人张三犯罪"},
		{DocKey: "d1", SegmentIdx: 1, Text: "证据不足"},
		{DocKey: "d2", SegmentIdx: 0, Text: "张三的供述"},
	}

	result, err := s.Sample(segments, map[string]interface{}{"keywords": "张三"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 matching segments, got %d", len(result))
	}
}

func TestRuleSampling_MultipleKeywords(t *testing.T) {
	s := &RuleSamplingStrategy{}
	segments := []plugin.SegmentUnit{
		{DocKey: "d1", SegmentIdx: 0, Text: "被告人张三"},
		{DocKey: "d1", SegmentIdx: 1, Text: "证据充分"},
		{DocKey: "d2", SegmentIdx: 0, Text: "无关内容"},
	}

	result, err := s.Sample(segments, map[string]interface{}{
		"keywords": []interface{}{"张三", "证据"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 matching segments, got %d", len(result))
	}
}

func TestRuleSampling_NoMatch(t *testing.T) {
	s := &RuleSamplingStrategy{}
	segments := makeSegments(3)

	result, err := s.Sample(segments, map[string]interface{}{"keywords": "nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 matching segments, got %d", len(result))
	}
}

func TestRandomSampling_MetaFields(t *testing.T) {
	s := &RandomSamplingStrategy{}
	if s.StrategyID() != "random" {
		t.Errorf("expected 'random', got %q", s.StrategyID())
	}
	if s.Name() != "Random Sampling" {
		t.Errorf("expected 'Random Sampling', got %q", s.Name())
	}
}

func TestRuleSampling_MetaFields(t *testing.T) {
	s := &RuleSamplingStrategy{}
	if s.StrategyID() != "rule" {
		t.Errorf("expected 'rule', got %q", s.StrategyID())
	}
	if s.Name() != "Rule-based Sampling" {
		t.Errorf("expected 'Rule-based Sampling', got %q", s.Name())
	}
}

func makeSegments(n int) []plugin.SegmentUnit {
	segs := make([]plugin.SegmentUnit, n)
	for i := range segs {
		segs[i] = plugin.SegmentUnit{
			DocKey:     fmt.Sprintf("doc_%d", i),
			SegmentIdx: i,
			Text:       fmt.Sprintf("segment text %d", i),
		}
	}
	return segs
}
