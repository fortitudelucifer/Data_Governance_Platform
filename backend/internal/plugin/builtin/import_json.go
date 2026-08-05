package builtin

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
)

// JSONImportPlugin implements ImportPlugin for JSON and JSONL files.
type JSONImportPlugin struct{}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func normalizeJSONInput(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	trimmed = bytes.TrimPrefix(trimmed, utf8BOM)
	return bytes.TrimSpace(trimmed)
}

func (p *JSONImportPlugin) FormatID() string {
	return "json"
}

func (p *JSONImportPlugin) SupportedExtensions() []string {
	return []string{".json", ".jsonl"}
}

// Validate checks that the content is valid JSON array, JSON object, or JSONL.
func (p *JSONImportPlugin) Validate(reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	trimmed := normalizeJSONInput(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("empty input")
	}

	// Try JSON array first
	if trimmed[0] == '[' {
		var arr []map[string]interface{}
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return fmt.Errorf("invalid JSON array: %w", err)
		}
		return nil
	}

	// For JSON objects or JSONL stream, use json.Decoder
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	count := 0
	for {
		var obj map[string]interface{}
		err := decoder.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("record %d: invalid JSON object: %w", count+1, err)
		}
		count++
	}

	if count == 0 {
		return fmt.Errorf("no valid JSON records found")
	}
	return nil
}

// Parse parses JSON array or JSONL content into ParsedDocuments.
// If a record has no "doc_key" field, the "id" field is used as fallback.
func (p *JSONImportPlugin) Parse(reader io.Reader) ([]paymodel.ParsedDocument, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	trimmed := normalizeJSONInput(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty input")
	}

	var records []map[string]interface{}

	// Try JSON array first
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &records); err != nil {
			return nil, fmt.Errorf("invalid JSON array: %w", err)
		}
	} else {
		// Try continuous stream of JSON objects (handles single object, JSONL, and concatenated JSON)
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		count := 0
		for {
			var obj map[string]interface{}
			err := decoder.Decode(&obj)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("record %d: invalid JSON object: %w", count+1, err)
			}
			records = append(records, obj)
			count++
		}
	}

	var docs []paymodel.ParsedDocument
	seenKeys := make(map[string]int)
	for i, rec := range records {
		baseDocKey := extractDocKey(rec, i)
		if baseDocKey == "" {
			return nil, fmt.Errorf("record %d: could not determine doc_key", i)
		}
		seenKeys[baseDocKey]++
		docKey := baseDocKey
		if seenKeys[baseDocKey] > 1 {
			docKey = fmt.Sprintf("%s__%d", baseDocKey, seenKeys[baseDocKey])
		}

		// Normalize fields
		if _, ok := rec["full_text"]; !ok {
			if textVal, ok := rec["text"]; ok {
				rec["full_text"] = textVal
			}
		}

		if qaVal, ok := rec["Q&A"]; ok {
			if qaList, ok := qaVal.([]interface{}); ok {
				var qaPairs []map[string]interface{}
				for _, item := range qaList {
					if m, ok := item.(map[string]interface{}); ok {
						qaItem := make(map[string]interface{})
						if q, ok := m["question"].(string); ok {
							qaItem["question"] = q
						}
						if a, ok := m["answer"]; ok {
							qaItem["answer"] = a
						}
						qaItem["source"] = "manual"
						qaPairs = append(qaPairs, qaItem)
					}
				}
				rec["qa_pairs"] = qaPairs
			}
		}
		normalizeRecordQAPairs(rec)

		docs = append(docs, paymodel.ParsedDocument{
			DocKey: docKey,
			Data:   rec,
		})
	}
	return docs, nil
}

func normalizeRecordQAPairs(rec map[string]interface{}) {
	rawPairs, ok := rec["qa_pairs"]
	if !ok || rawPairs == nil {
		return
	}
	qaList, ok := rawPairs.([]interface{})
	if !ok {
		return
	}

	for i, item := range qaList {
		pairMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		answerRaw, hasAnswer := pairMap["answer"]
		if !hasAnswer {
			continue
		}

		normalized, structured := normalizeAnswerValue(answerRaw)
		pairMap["answer"] = normalized

		if structured != nil {
			meta := map[string]interface{}{}
			if existingMeta, ok := pairMap["meta"].(map[string]interface{}); ok {
				meta = existingMeta
			}
			meta["raw_answer"] = answerRaw
			meta["answer_structured"] = structured
			pairMap["meta"] = meta
		}
		qaList[i] = pairMap
	}
	rec["qa_pairs"] = qaList
}

func normalizeAnswerValue(raw interface{}) (string, interface{}) {
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "", nil
		}
		if parsed, ok := tryParseStructuredString(trimmed); ok {
			return structuredToReadable(parsed), parsed
		}
		return trimmed, nil
	case map[string]interface{}, []interface{}:
		return structuredToReadable(v), v
	default:
		return fmt.Sprintf("%v", raw), nil
	}
}

func tryParseStructuredString(s string) (interface{}, bool) {
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return nil, false
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func structuredToReadable(v interface{}) string {
	switch x := v.(type) {
	case []interface{}:
		if allStrings(x) {
			items := make([]string, 0, len(x))
			for _, item := range x {
				s := strings.TrimSpace(fmt.Sprintf("%v", item))
				if s != "" {
					items = append(items, s)
				}
			}
			return strings.Join(items, "；")
		}
		parts := make([]string, 0, len(x))
		for _, item := range x {
			txt := strings.TrimSpace(structuredToReadable(item))
			if txt != "" {
				parts = append(parts, txt)
			}
		}
		return strings.Join(parts, "；")
	case map[string]interface{}:
		parts := make([]string, 0, 8)
		if cause := strings.TrimSpace(fmt.Sprintf("%v", x["conviction_or_cause"])); cause != "" && cause != "<nil>" {
			parts = append(parts, fmt.Sprintf("案由：%s", cause))
		}
		if result := strings.TrimSpace(fmt.Sprintf("%v", x["result_type"])); result != "" && result != "<nil>" {
			parts = append(parts, fmt.Sprintf("裁判结果：%s", result))
		}
		if statutes, ok := asStringSlice(x["法条"]); ok && len(statutes) > 0 {
			parts = append(parts, fmt.Sprintf("涉及法条：%s", strings.Join(statutes, "；")))
		}
		if pays, ok := x["赔偿/给付金额"]; ok {
			if arr, ok := pays.([]interface{}); ok {
				items := make([]string, 0, len(arr))
				for _, item := range arr {
					if m, ok := item.(map[string]interface{}); ok {
						tp := strings.TrimSpace(fmt.Sprintf("%v", m["类型"]))
						amt := strings.TrimSpace(fmt.Sprintf("%v", m["金额_元"]))
						if tp != "" && amt != "" {
							items = append(items, fmt.Sprintf("%s%s元", tp, amt))
						} else if amt != "" {
							items = append(items, fmt.Sprintf("%s元", amt))
						}
					}
				}
				if len(items) > 0 {
					parts = append(parts, fmt.Sprintf("金额：%s", strings.Join(items, "；")))
				}
			}
		}
		if amount := strings.TrimSpace(fmt.Sprintf("%v", x["赔偿/给付金额_元"])); amount != "" && amount != "<nil>" {
			parts = append(parts, fmt.Sprintf("给付金额：%s元", amount))
		}
		if sentence, ok := x["sentence"].(map[string]interface{}); ok {
			if penalties, ok := sentence["penalty_types"].([]interface{}); ok && len(penalties) > 0 {
				names := make([]string, 0, len(penalties))
				for _, item := range penalties {
					s := strings.TrimSpace(fmt.Sprintf("%v", item))
					if s != "" && s != "<nil>" {
						names = append(names, s)
					}
				}
				if len(names) > 0 {
					parts = append(parts, fmt.Sprintf("处理方式：%s", strings.Join(names, "、")))
				}
			}
			if compensation := strings.TrimSpace(fmt.Sprintf("%v", sentence["compensation_amount"])); compensation != "" && compensation != "<nil>" {
				parts = append(parts, fmt.Sprintf("给付金额：%s元", compensation))
			}
			if fine := strings.TrimSpace(fmt.Sprintf("%v", sentence["fine_amount"])); fine != "" && fine != "<nil>" {
				parts = append(parts, fmt.Sprintf("罚金：%s元", fine))
			}
			if term := strings.TrimSpace(fmt.Sprintf("%v", sentence["term_months"])); term != "" && term != "<nil>" {
				parts = append(parts, fmt.Sprintf("期限：%s个月", term))
			}
			if other := strings.TrimSpace(fmt.Sprintf("%v", sentence["other"])); other != "" && other != "<nil>" {
				parts = append(parts, fmt.Sprintf("说明：%s", other))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "；")
		}
		for k, val := range x {
			switch vv := val.(type) {
			case []interface{}, map[string]interface{}:
				txt := strings.TrimSpace(structuredToReadable(vv))
				if txt != "" {
					parts = append(parts, fmt.Sprintf("%s：%s", k, txt))
				}
			default:
				valTxt := strings.TrimSpace(fmt.Sprintf("%v", vv))
				if valTxt != "" {
					parts = append(parts, fmt.Sprintf("%s：%s", k, valTxt))
				}
			}
		}
		return strings.Join(parts, "；")
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func asStringSlice(v interface{}) ([]string, bool) {
	arr, ok := v.([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(fmt.Sprintf("%v", item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

func allStrings(items []interface{}) bool {
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

// extractDocKey tries to get doc_key from a record, falling back to id field.
func extractDocKey(rec map[string]interface{}, index int) string {
	// Try doc_key first
	if v, ok := rec["doc_key"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	// Fallback to id field
	if v, ok := rec["id"]; ok {
		return fmt.Sprintf("%v", v)
	}

	// Last resort: generate a stable key from record content + index
	payload, _ := json.Marshal(rec)
	sum := sha1.Sum(payload)
	return fmt.Sprintf("auto_%x_%d", sum[:8], index+1)
}
