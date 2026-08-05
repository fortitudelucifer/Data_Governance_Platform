package builtin

import (
	"fmt"
	"strings"

	"text-annotation-platform/internal/plugin"
)

// RuleSamplingStrategy implements SamplingStrategy for keyword-based rule sampling.
type RuleSamplingStrategy struct{}

func (s *RuleSamplingStrategy) StrategyID() string {
	return "rule"
}

func (s *RuleSamplingStrategy) Name() string {
	return "Rule-based Sampling"
}

// ValidateParams checks that keywords is a non-empty string or []string.
func (s *RuleSamplingStrategy) ValidateParams(params map[string]interface{}) error {
	kwVal, ok := params["keywords"]
	if !ok {
		return fmt.Errorf("strategy 'rule' parameter error: 'keywords' is required")
	}

	keywords, err := extractKeywords(kwVal)
	if err != nil {
		return fmt.Errorf("strategy 'rule' parameter error: %w", err)
	}
	if len(keywords) == 0 {
		return fmt.Errorf("strategy 'rule' parameter error: 'keywords' must not be empty")
	}
	return nil
}

// Sample filters segments that contain any of the specified keywords.
func (s *RuleSamplingStrategy) Sample(segments []plugin.SegmentUnit, params map[string]interface{}) ([]plugin.SegmentUnit, error) {
	if err := s.ValidateParams(params); err != nil {
		return nil, err
	}

	keywords, _ := extractKeywords(params["keywords"])

	var result []plugin.SegmentUnit
	for _, seg := range segments {
		for _, kw := range keywords {
			if strings.Contains(seg.Text, kw) {
				result = append(result, seg)
				break
			}
		}
	}
	return result, nil
}

// extractKeywords converts the keywords param to a []string.
func extractKeywords(val interface{}) ([]string, error) {
	switch v := val.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, nil
		}
		return []string{trimmed}, nil
	case []interface{}:
		var result []string
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("'keywords' array elements must be strings")
			}
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result, nil
	case []string:
		var result []string
		for _, s := range v {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		return result, nil
	default:
		return nil, fmt.Errorf("'keywords' must be a string or array of strings, got %T", val)
	}
}
