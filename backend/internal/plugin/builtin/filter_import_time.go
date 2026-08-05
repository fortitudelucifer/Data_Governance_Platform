package builtin

import (
	"context"
	"fmt"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// ImportTimeFilter filters documents by their created_at (import time).
type ImportTimeFilter struct{}

func (f *ImportTimeFilter) FilterID() string    { return "import_time" }
func (f *ImportTimeFilter) Name() string        { return "导入时间" }
func (f *ImportTimeFilter) Description() string { return "按文档导入时间筛选" }

func (f *ImportTimeFilter) ParamSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"start": map[string]interface{}{"type": "string", "format": "date-time", "description": "开始时间"},
			"end":   map[string]interface{}{"type": "string", "format": "date-time", "description": "结束时间"},
		},
		"required": []string{"start", "end"},
	}
}

func (f *ImportTimeFilter) ValidateParams(params map[string]interface{}) error {
	start, ok := params["start"].(string)
	if !ok || start == "" {
		return fmt.Errorf("start parameter is required")
	}
	end, ok := params["end"].(string)
	if !ok || end == "" {
		return fmt.Errorf("end parameter is required")
	}
	if _, err := time.Parse(time.RFC3339, start); err != nil {
		return fmt.Errorf("invalid start time format: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, end); err != nil {
		return fmt.Errorf("invalid end time format: %w", err)
	}
	return nil
}

func (f *ImportTimeFilter) Apply(ctx context.Context, docs []paymodel.Document, params map[string]interface{}) ([]paymodel.Document, error) {
	startStr, _ := params["start"].(string)
	endStr, _ := params["end"].(string)
	start, _ := time.Parse(time.RFC3339, startStr)
	end, _ := time.Parse(time.RFC3339, endStr)

	var result []paymodel.Document
	for _, doc := range docs {
		if !doc.CreatedAt.Before(start) && !doc.CreatedAt.After(end) {
			result = append(result, doc)
		}
	}
	return result, nil
}
