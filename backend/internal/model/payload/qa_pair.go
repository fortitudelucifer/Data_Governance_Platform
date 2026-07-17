package payload

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseQAPairs normalises raw QA pair data from any source into a filtered
// slice of QAPair.
//
// Handled input types:
//   - string          – LLM JSON response, optionally wrapped in ``` blocks
//   - []QAPair        – already typed slice (identity conversion)
//   - []interface{}   – generic JSON-decoded array
//   - map[string]interface{} – single decoded object (wrapped in slice)
//
// Filter rules applied (union of all original service implementations):
//   - Skip pair where Question or Answer is empty after TrimSpace  [auto_annotation]
//   - Skip pair where Question rune count < 5                      [auto_annotation]
//   - Deduplicate by QuestionKey when present, otherwise Question (first occurrence wins)
func ParseQAPairs(data interface{}) []QAPair {
	return filterQAPairs(extractQAPairs(data))
}

// extractQAPairs converts any supported raw form to an unfiltered []QAPair.
func extractQAPairs(data interface{}) []QAPair {
	if data == nil {
		return nil
	}
	switch v := data.(type) {
	case []QAPair:
		return v
	case []interface{}:
		return fromInterfaceSlice(v)
	case string:
		return parseStringResponse(v)
	case map[string]interface{}:
		return []QAPair{fromMapItem(v)}
	}
	return nil
}

func fromInterfaceSlice(arr []interface{}) []QAPair {
	pairs := make([]QAPair, 0, len(arr))
	for _, item := range arr {
		switch m := item.(type) {
		case map[string]interface{}:
			pairs = append(pairs, fromMapItem(m))
		}
	}
	return pairs
}

func fromMapItem(m map[string]interface{}) QAPair {
	p := QAPair{}
	if q, ok := m["question"].(string); ok {
		p.Question = q
	}
	if a, ok := m["answer"].(string); ok {
		p.Answer = a
	}
	if key, ok := m["question_key"].(string); ok {
		p.QuestionKey = key
	}
	if category, ok := m["category"].(string); ok {
		p.Category = category
	}
	if evidence, ok := m["evidence"].(string); ok {
		p.Evidence = evidence
	}
	p.Confidence = optionalFloat(m["confidence"])
	if reason, ok := m["reason"].(string); ok {
		p.Reason = reason
	}
	if s, ok := m["source"].(string); ok {
		p.Source = s
	}
	if c, ok := m["confirmed"].(bool); ok {
		p.Confirmed = c
	}
	if st, ok := m["span_text"].(string); ok {
		p.SpanText = st
	}
	p.SpanStart = optionalInt(m["span_start"])
	p.SpanEnd = optionalInt(m["span_end"])
	if tf, ok := m["text_field"].(string); ok {
		p.TextField = tf
	}
	p.ProviderID = optionalUint(m["provider_id"])
	if pn, ok := m["provider_name"].(string); ok {
		p.ProviderName = pn
	}
	if model, ok := m["model"].(string); ok {
		p.Model = model
	}
	p.PromptTemplateID = optionalUint(m["prompt_template_id"])
	if promptName, ok := m["prompt_template_name"].(string); ok {
		p.PromptTemplateName = promptName
	}
	if version := optionalInt(m["prompt_version"]); version != nil {
		p.PromptVersion = *version
	}
	if runID, ok := m["candidate_run_id"].(string); ok {
		p.CandidateRunID = runID
	}
	if judgeRunID, ok := m["judge_run_id"].(string); ok {
		p.JudgeRunID = judgeRunID
	}
	p.SourceCandidateRunIDs = optionalStringSlice(m["source_candidate_run_ids"])
	if len(p.SourceCandidateRunIDs) == 0 {
		p.SourceCandidateRunIDs = optionalStringSlice(m["source_run_ids"])
	}
	if edited, ok := m["edited_after_adopt"].(bool); ok {
		p.EditedAfterAdopt = edited
	}
	if metaMap, ok := m["meta"].(map[string]interface{}); ok {
		p.Meta = metaMap
	}
	return p
}

// parseStringResponse parses an LLM text response that may contain a JSON
// array of {question, answer} objects, optionally wrapped in markdown code
// blocks. Returns nil when no valid array can be found.
func parseStringResponse(response string) []QAPair {
	response = strings.TrimSpace(response)

	// Strip markdown code blocks (```json … ``` or ``` … ```)
	if idx := strings.Index(response, "```json"); idx != -1 {
		response = response[idx+7:]
		if end := strings.Index(response, "```"); end != -1 {
			response = response[:end]
		}
	} else if idx := strings.Index(response, "```"); idx != -1 {
		response = response[idx+3:]
		if end := strings.Index(response, "```"); end != -1 {
			response = response[:end]
		}
	}
	response = strings.TrimSpace(response)

	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	if start == -1 || end == -1 || end <= start {
		return nil
	}
	response = response[start : end+1]

	var rawPairs []map[string]interface{}
	if err := json.Unmarshal([]byte(response), &rawPairs); err != nil {
		repaired := escapeBareQuotesInsideJSONStrings(response)
		if repaired == response {
			return nil
		}
		if err := json.Unmarshal([]byte(repaired), &rawPairs); err != nil {
			return nil
		}
	}
	pairs := make([]QAPair, 0, len(rawPairs))
	for _, raw := range rawPairs {
		pairs = append(pairs, fromMapItem(raw))
	}
	return pairs
}

func escapeBareQuotesInsideJSONStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	changed := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			b.WriteByte(ch)
			escaped = false
			continue
		}
		if inString && ch == '\\' {
			b.WriteByte(ch)
			escaped = true
			continue
		}
		if ch == '"' {
			if !inString {
				inString = true
				b.WriteByte(ch)
				continue
			}
			if looksLikeClosingJSONStringQuote(s, i) {
				inString = false
				b.WriteByte(ch)
				continue
			}
			b.WriteByte('\\')
			b.WriteByte(ch)
			changed = true
			continue
		}
		b.WriteByte(ch)
	}
	if !changed {
		return s
	}
	return b.String()
}

func looksLikeClosingJSONStringQuote(s string, quoteIndex int) bool {
	for i := quoteIndex + 1; i < len(s); i++ {
		switch s[i] {
		case ' ', '\n', '\r', '\t':
			continue
		case ':', ',', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}

// filterQAPairs removes low-quality pairs.
// Rules:
//   - Empty Question or Answer after TrimSpace          [from auto_annotation.FilterQAPairs]
//   - Question rune count < 5                           [from auto_annotation.FilterQAPairs]
//   - Duplicate QuestionKey first, otherwise Question; first wins
func filterQAPairs(pairs []QAPair) []QAPair {
	if len(pairs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(pairs))
	result := make([]QAPair, 0, len(pairs))
	for _, p := range pairs {
		q := strings.TrimSpace(p.Question)
		a := strings.TrimSpace(p.Answer)
		if q == "" || a == "" {
			continue
		}
		if utf8.RuneCountInString(q) < 5 {
			continue
		}
		p.QuestionKey = strings.TrimSpace(p.QuestionKey)
		p.Category = strings.TrimSpace(p.Category)
		p.Evidence = strings.TrimSpace(p.Evidence)
		p.Reason = strings.TrimSpace(p.Reason)
		dedupKey := qaDedupKey(p)
		if _, dup := seen[dedupKey]; dup {
			continue
		}
		seen[dedupKey] = struct{}{}
		p.Question = q
		p.Answer = a
		result = append(result, p)
	}
	return result
}

func qaDedupKey(p QAPair) string {
	if key := strings.TrimSpace(p.QuestionKey); key != "" {
		return "key:" + strings.ToLower(key)
	}
	return "question:" + strings.TrimSpace(p.Question)
}

func optionalInt(v interface{}) *int {
	switch n := v.(type) {
	case int:
		return &n
	case int32:
		i := int(n)
		return &i
	case int64:
		i := int(n)
		return &i
	case float64:
		i := int(n)
		return &i
	case float32:
		i := int(n)
		return &i
	}
	return nil
}

func optionalUint(v interface{}) uint {
	switch n := v.(type) {
	case uint:
		return n
	case uint32:
		return uint(n)
	case uint64:
		return uint(n)
	case int:
		if n > 0 {
			return uint(n)
		}
	case int32:
		if n > 0 {
			return uint(n)
		}
	case int64:
		if n > 0 {
			return uint(n)
		}
	case float64:
		if n > 0 {
			return uint(n)
		}
	case float32:
		if n > 0 {
			return uint(n)
		}
	}
	return 0
}

func optionalFloat(v interface{}) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case float32:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	case int32:
		f := float64(n)
		return &f
	case int64:
		f := float64(n)
		return &f
	case uint:
		f := float64(n)
		return &f
	case uint32:
		f := float64(n)
		return &f
	case uint64:
		f := float64(n)
		return &f
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(n))
		if trimmed == "" {
			return nil
		}
		if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return &parsed
		}
		switch trimmed {
		case "high", "高", "较高":
			f := 0.9
			return &f
		case "medium", "mid", "中", "中等", "一般":
			f := 0.6
			return &f
		case "low", "低", "较低":
			f := 0.3
			return &f
		}
	}
	return nil
}

func optionalStringSlice(v interface{}) []string {
	switch arr := v.(type) {
	case []string:
		return arr
	case []interface{}:
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}
