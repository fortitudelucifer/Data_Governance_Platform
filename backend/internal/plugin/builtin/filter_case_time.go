package builtin

import (
	"context"
	"fmt"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// CaseTimeFilter filters documents by case occurrence time from document data.
type CaseTimeFilter struct{}

func (f *CaseTimeFilter) FilterID() string    { return "case_occurrence_time" }
func (f *CaseTimeFilter) Name() string        { return "案件发生时间" }
func (f *CaseTimeFilter) Description() string { return "按案件发生时间筛选" }

func (f *CaseTimeFilter) ParamSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"start":      map[string]interface{}{"type": "string", "format": "date-time", "description": "开始时间"},
			"end":        map[string]interface{}{"type": "string", "format": "date-time", "description": "结束时间"},
			"field_path": map[string]interface{}{"type": "string", "description": "时间字段路径", "default": "case_time"},
		},
		"required": []string{"start", "end"},
	}
}

func (f *CaseTimeFilter) ValidateParams(params map[string]interface{}) error {
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

func (f *CaseTimeFilter) Apply(ctx context.Context, docs []paymodel.Document, params map[string]interface{}) ([]paymodel.Document, error) {
	startStr, _ := params["start"].(string)
	endStr, _ := params["end"].(string)
	start, _ := time.Parse(time.RFC3339, startStr)
	end, _ := time.Parse(time.RFC3339, endStr)

	fieldPath := "case_time"
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

// parseTimeValue attempts to parse a time value from various formats.
func parseTimeValue(v interface{}) (time.Time, error) {
	switch val := v.(type) {
	case time.Time:
		return val, nil
	case string:
		formats := []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02", "2006/01/02"}
		for _, f := range formats {
			if t, err := time.Parse(f, val); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("unable to parse time: %s", val)
	}
	return time.Time{}, fmt.Errorf("unsupported time type: %T", v)
}
