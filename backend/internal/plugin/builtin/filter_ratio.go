package builtin

import (
	"context"
	"fmt"
	"math/rand"

	paymodel "text-annotation-platform/internal/model/payload"
)

// RatioFilter randomly selects a proportion of documents.
type RatioFilter struct{}

func (f *RatioFilter) FilterID() string    { return "ratio" }
func (f *RatioFilter) Name() string        { return "比例抽取" }
func (f *RatioFilter) Description() string { return "随机抽取指定比例的文档" }

func (f *RatioFilter) ParamSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ratio": map[string]interface{}{
				"type":        "number",
				"minimum":     0,
				"maximum":     1,
				"description": "抽取比例 (0-1)",
			},
		},
		"required": []string{"ratio"},
	}
}

func (f *RatioFilter) ValidateParams(params map[string]interface{}) error {
	ratio, ok := params["ratio"]
	if !ok {
		return fmt.Errorf("ratio parameter is required")
	}
	r, ok := toFloat64(ratio)
	if !ok || r < 0 || r > 1 {
		return fmt.Errorf("ratio must be a number between 0 and 1")
	}
	return nil
}

func (f *RatioFilter) Apply(ctx context.Context, docs []paymodel.Document, params map[string]interface{}) ([]paymodel.Document, error) {
	ratio, _ := toFloat64(params["ratio"])
	if len(docs) == 0 || ratio <= 0 {
		return nil, nil
	}
	if ratio >= 1 {
		return docs, nil
	}

	// Shuffle and take the first N
	n := int(float64(len(docs))*ratio + 0.5)
	if n == 0 {
		n = 1
	}
	if n > len(docs) {
		n = len(docs)
	}

	shuffled := make([]paymodel.Document, len(docs))
	copy(shuffled, docs)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	return shuffled[:n], nil
}

// toFloat64 converts various numeric types to float64.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}
