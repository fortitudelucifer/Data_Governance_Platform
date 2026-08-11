package service

import (
	"strconv"
	"strings"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	"text-annotation-platform/internal/util"
)

// textExportSchemaVersion identifies the 《通用元数据字段》 envelope layout.
const textExportSchemaVersion = "1.0"

// defaultDataVersion is used when a dataset has not configured data_version.
const defaultDataVersion = "V1.0"

// exportEnvelopeMeta holds the dataset-level constants stamped onto every
// exported record (configured via PUT /datasets/:id/export-meta).
type exportEnvelopeMeta struct {
	AuthType     string
	SourceType   string
	DataVersion  string
	SourceDetail map[string]interface{}
}

// contentFieldOrder mirrors the workbench text-field resolution
// (frontend lib/textAnnotation.ts TEXT_FIELD_ORDER) so the exported `content`
// matches the text annotators actually worked on.
var contentFieldOrder = []string{"text", "content", "full_text", "fact_text", "raw_text"}

// buildTextEnvelope converts a stored document into the 《通用元数据字段》 export
// envelope. resolveUser maps a numeric user id to a display name (may be nil).
func buildTextEnvelope(doc paymodel.Document, meta exportEnvelopeMeta, resolveUser func(uint) string) map[string]interface{} {
	labelInfo, gen := buildLabelInfo(doc, resolveUser)

	dataVersion := strings.TrimSpace(meta.DataVersion)
	if dataVersion == "" {
		dataVersion = defaultDataVersion
	}

	env := map[string]interface{}{
		"schema_version": textExportSchemaVersion,
		"id":             doc.DocKey,
		"rid":            []string{},
		"content":        resolveContent(doc.Data),
		"content_fields": allContentFields(doc.Data), // #16 span.field 可指向非 primary 字段,一并带出
		"label_info":     labelInfo,
		"original_time":  nil,
		"modified_time":  formatExportTime(doc.UpdatedAt.Time),
		"data_version":   dataVersion + "." + strconv.Itoa(doc.Version),
		"auth_type":      meta.AuthType,
		"source_type":    meta.SourceType,
		"source_detail":  buildSourceDetail(meta.SourceDetail, doc.Data),
		"generated_flag": gen,
	}
	return env
}

// buildLabelInfo assembles label_info (annotations + provenance + review state)
// and the generated_flag summary from the document's QA pairs.
func buildLabelInfo(doc paymodel.Document, resolveUser func(uint) string) (map[string]interface{}, map[string]interface{}) {
	// #17 导出用**无损**解码器,不跑生成阶段的清洗(否则短问题/重复问题被静默删掉,
	// 正式 QA 里人工加的记录凭空消失)。按存储顺序原样携带。
	pairs := paymodel.DecodeQAPairs(doc.Data["qa_pairs"])

	cleanPairs := make([]map[string]interface{}, 0, len(pairs))
	modelsSet := map[string]struct{}{}
	hasModel, hasRule, hasManual := false, false, false
	allConfirmed := len(pairs) > 0

	for _, p := range pairs {
		cp := map[string]interface{}{
			"question":  p.Question,
			"answer":    p.Answer,
			"source":    p.Source,
			"confirmed": p.Confirmed,
		}
		if p.QuestionKey != "" {
			cp["question_key"] = p.QuestionKey
		}
		if p.Category != "" {
			cp["category"] = p.Category
		}
		if p.Evidence != "" {
			cp["evidence"] = p.Evidence
		}
		if p.Reason != "" {
			cp["reason"] = p.Reason
		}
		if p.Confidence != nil {
			cp["confidence"] = *p.Confidence
		}
		if p.SpanText != "" || p.SpanStart != nil || p.SpanEnd != nil {
			span := map[string]interface{}{}
			if p.TextField != "" {
				span["field"] = p.TextField
			}
			if p.SpanStart != nil {
				span["start"] = *p.SpanStart
			}
			if p.SpanEnd != nil {
				span["end"] = *p.SpanEnd
			}
			if p.SpanText != "" {
				span["text"] = p.SpanText
			}
			cp["span"] = span
		}
		cleanPairs = append(cleanPairs, cp)

		// #18 交叉校验:带模型运行 ID/Model 的一定是模型产出,不管 source 字符串怎么写。
		cls := classifySource(p.Source)
		if cls != "model" && strings.TrimSpace(p.Model) != "" {
			cls = "model"
		}
		switch cls {
		case "model":
			hasModel = true
		case "rule":
			hasRule = true
		case "human":
			hasManual = true
		}
		if m := strings.TrimSpace(p.Model); m != "" {
			modelsSet[m] = struct{}{}
		}
		if !p.Confirmed {
			allConfirmed = false
		}
	}

	stage := doc.AnnotationStage
	if stage == "" {
		stage = StageNotAnnotated
	}

	annotatorName := strings.TrimSpace(doc.AnnotatorName)
	if annotatorName == "" && doc.CreatedBy != 0 && resolveUser != nil {
		annotatorName = resolveUser(doc.CreatedBy)
	}

	labelInfo := map[string]interface{}{
		"annotation_type": "qa",
		"qa_pairs":        cleanPairs,
		"annotator": map[string]interface{}{
			"name": annotatorName,
			"type": annotatorType(hasModel, hasRule, hasManual),
		},
		"annotation_tool": "text-annotation-platform",
		"review_status":   stage,
		"quality_score":   optionalIntValue(doc.LLMRefinementScore),
	}

	verified := stage == StageRefined || (allConfirmed && len(pairs) > 0)
	gen := map[string]interface{}{
		"generated": hasModel || hasRule,
		"methods":   collectMethods(hasModel, hasRule, hasManual),
		"models":    sortedKeys(modelsSet),
		"verified":  verified,
	}

	return labelInfo, gen
}

// allContentFields returns every non-empty known text field present on the doc
// (#16). The envelope's top-level `content` is only the *primary* field, but a
// span may anchor to another one (e.g. an annotator selected on `full_text` while
// the primary is the short `text`). Without carrying the other fields, that span's
// start/end offsets point into text the export doesn't contain — unresolvable
// downstream. Export all of them so every span.field can be resolved.
func allContentFields(data map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, f := range contentFieldOrder {
		if v, ok := data[f]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				out[f] = s
			}
		}
	}
	return out
}

// resolveContent picks the primary text body, matching the workbench order and
// falling back to the longest string field.
func resolveContent(data map[string]interface{}) interface{} {
	for _, f := range contentFieldOrder {
		if v, ok := data[f]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	var best string
	for _, v := range data {
		if s, ok := v.(string); ok && len(s) > len(best) {
			best = s
		}
	}
	return best
}

// buildSourceDetail merges the dataset-level source_detail constants with
// per-document derivations (origin_id from the imported record).
func buildSourceDetail(base map[string]interface{}, data map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range base {
		out[k] = v
	}
	if _, exists := out["origin_id"]; !exists {
		if v, ok := data["_id"]; ok {
			out["origin_id"] = v
		} else if v, ok := data["id"]; ok {
			out["origin_id"] = v
		}
	}
	return out
}

// classifySource normalises a QA-pair source into model / rule / human / other.
//
// #18 采纳的模型结果 source 是 `llm_candidate` / `llm_judge` / `llm_judge_suggestion`
// 这类**带前缀**的值(text_candidate 采纳时写的)。旧的精确匹配漏掉它们 → 归 other →
// generated=false、methods 缺 model —— 直接破坏"这条是不是模型产出"的数据来源审计
// (医学场景尤其致命)。生产与导出必须共用同一套判据:精确枚举 + llm/ai/model 前缀兜底。
func classifySource(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	switch s {
	case "ai", "model", "llm", "auto", "machine", "generated",
		"llm_candidate", "llm_judge", "llm_judge_suggestion":
		return "model"
	case "rule", "regex", "template":
		return "rule"
	case "manual", "human", "annotator", "edited":
		return "human"
	}
	if strings.HasPrefix(s, "llm") || strings.HasPrefix(s, "model") || strings.HasPrefix(s, "gpt") {
		return "model"
	}
	return "other"
}

func annotatorType(hasModel, hasRule, hasManual bool) string {
	machine := hasModel || hasRule
	switch {
	case machine && hasManual:
		return "mixed"
	case hasManual:
		return "human"
	case machine:
		return "model"
	default:
		return ""
	}
}

func collectMethods(hasModel, hasRule, hasManual bool) []string {
	methods := []string{}
	if hasModel {
		methods = append(methods, "model")
	}
	if hasRule {
		methods = append(methods, "rule")
	}
	if hasManual {
		methods = append(methods, "manual")
	}
	return methods
}

func formatExportTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.In(util.AppLocation()).Format(time.RFC3339)
}

func optionalIntValue(v *int) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	// small sets; simple insertion sort keeps output deterministic
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
