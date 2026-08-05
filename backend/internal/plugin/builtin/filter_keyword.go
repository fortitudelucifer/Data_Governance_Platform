package builtin

import (
	"context"
	"fmt"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
)

// KeywordFilter filters documents containing a keyword.
type KeywordFilter struct{}

func (f *KeywordFilter) FilterID() string    { return "keyword" }
func (f *KeywordFilter) Name() string        { return "关键词" }
func (f *KeywordFilter) Description() string { return "全文或指定字段包含关键词" }

func (f *KeywordFilter) ParamSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"keyword":    map[string]interface{}{"type": "string", "description": "搜索关键词"},
			"field_path": map[string]interface{}{"type": "string", "description": "指定字段路径（可选，默认搜索 raw_text）"},
		},
		"required": []string{"keyword"},
	}
}

func (f *KeywordFilter) ValidateParams(params map[string]interface{}) error {
	keyword, ok := params["keyword"].(string)
	if !ok || strings.TrimSpace(keyword) == "" {
		return fmt.Errorf("keyword parameter is required and must be non-empty")
	}
	return nil
}

func (f *KeywordFilter) Apply(ctx context.Context, docs []paymodel.Document, params map[string]interface{}) ([]paymodel.Document, error) {
	keyword, _ := params["keyword"].(string)
	keyword = strings.TrimSpace(keyword)

	fieldPath := "raw_text"
	if fp, ok := params["field_path"].(string); ok && fp != "" {
		fieldPath = fp
	}

	var result []paymodel.Document
	for _, doc := range docs {
		if doc.Data == nil {
			continue
		}
		val, ok := doc.Data[fieldPath]
		if !ok {
			continue
		}
		text, ok := val.(string)
		if !ok {
			continue
		}
		if strings.Contains(text, keyword) {
			result = append(result, doc)
		}
	}
	return result, nil
}
