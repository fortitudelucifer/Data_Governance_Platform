package builtin

import (
	"context"
	"fmt"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// JudgmentTimeFilter filters documents by judgment time from document data.
type JudgmentTimeFilter struct{}

func (f *JudgmentTimeFilter) FilterID() string    { return "judgment_time" }
func (f *JudgmentTimeFilter) Name() string        { return "判决时间" }
func (f *JudgmentTimeFilter) Description() string { return "按判决时间筛选" }

func (f *JudgmentTimeFilter) ParamSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"start":      map[string]interface{}{"type": "string", "format": "date-time", "description": "开始时间"},
			"end":        map[string]interface{}{"type": "string", "format": "date-time", "description": "结束时间"},
			"field_path": map[string]interface{}{"type": "string", "description": "时间字段路径", "default": "judgment_time"},
		},
		"required": []string{"start", "end"},
	}
}

func (f *JudgmentTimeFilter) ValidateParams(params map[string]interface{}) error {
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

func (f *JudgmentTimeFilter) Apply(ctx context.Context, docs []paymodel.Document, params map[string]interface{}) ([]paymodel.Document, error) {
	startStr, _ := params["start"].(string)
	endStr, _ := params["end"].(string)
	start, _ := time.Parse(time.RFC3339, startStr)
	end, _ := time.Parse(time.RFC3339, endStr)

	fieldPath := "judgment_time"
	if fp, ok := params["field_path"].(string); ok && fp != "" {
		fieldPath = fp
	}

	var result []paymodel.Document
	for _, doc := range docs {
		if doc.Data == nil {
			continue
		}
		timeVal, ok := doc.Data[fieldPath]
		if !ok {
			continue
		}
		t, err := parseTimeValue(timeVal)
		if err != nil {
			continue
		}
		if !t.Before(start) && !t.After(end) {
			result = append(result, doc)
		}
	}
	return result, nil
}
