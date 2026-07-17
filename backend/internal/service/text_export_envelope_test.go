package service

import (
	"encoding/json"
	"testing"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
)

// TestBuildTextEnvelope_Shape verifies the 《通用元数据字段》 envelope is assembled
// correctly from a realistic refined document: clean qa_pairs, resolved
// annotator, mixed generated_flag, dataset-level constants, derived origin_id.
func TestBuildTextEnvelope_Shape(t *testing.T) {
	conf := 0.9
	start, end := 10, 42
	doc := paymodel.Document{
		DocKey:          "cpws-001",
		Version:         3,
		AnnotationStage: StageRefined,
		AnnotatorName:   "张三",
		CreatedBy:       7,
		UpdatedAt:       paymodel.NewJSONTime(time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)),
		Data: map[string]interface{}{
			"_id":       "69a2aad5ba37c071e3a0d949",
			"full_text": "苏州市吴中区人民法院 民事判决书 …",
			"qa_pairs": []interface{}{
				map[string]interface{}{
					"question": "本案的最终判决结果是什么？", "answer": "驳回原告诉讼请求。",
					"category": "裁判结果", "evidence": "本院认为……",
					"span_text": "驳回原告", "span_start": start, "span_end": end,
					"text_field": "full_text", "confidence": conf,
					"source": "ai", "model": "qwen-vl-max", "confirmed": true,
					"candidate_run_id": "run-xyz", // internal field must be dropped
				},
				map[string]interface{}{
					"question": "被告是否需支付经济补偿金？", "answer": "需支付28000元。",
					"source": "manual", "confirmed": true,
				},
			},
		},
	}
	score := 92
	doc.LLMRefinementScore = &score

	meta := exportEnvelopeMeta{
		AuthType:     "内部受控",
		SourceType:   "公开法律文本",
		DataVersion:  "V1.0",
		SourceDetail: map[string]interface{}{"system": "裁判文书网"},
	}

	env := buildTextEnvelope(doc, meta, func(id uint) string { return "resolved-user" })

	// Round-trip through JSON so we assert on the actual serialized shape.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if got["id"] != "cpws-001" {
		t.Errorf("id = %v, want cpws-001", got["id"])
	}
	if got["data_version"] != "V1.0.3" {
		t.Errorf("data_version = %v, want V1.0.3", got["data_version"])
	}
	if got["auth_type"] != "内部受控" || got["source_type"] != "公开法律文本" {
		t.Errorf("auth/source = %v / %v", got["auth_type"], got["source_type"])
	}
	if got["original_time"] != nil {
		t.Errorf("original_time = %v, want null", got["original_time"])
	}
	if got["modified_time"] == nil {
		t.Errorf("modified_time should be set")
	}

	sd := got["source_detail"].(map[string]interface{})
	if sd["system"] != "裁判文书网" {
		t.Errorf("source_detail.system = %v", sd["system"])
	}
	if sd["origin_id"] != "69a2aad5ba37c071e3a0d949" {
		t.Errorf("source_detail.origin_id = %v (should derive from data._id)", sd["origin_id"])
	}

	li := got["label_info"].(map[string]interface{})
	if li["review_status"] != StageRefined {
		t.Errorf("review_status = %v", li["review_status"])
	}
	if li["quality_score"].(float64) != 92 {
		t.Errorf("quality_score = %v, want 92", li["quality_score"])
	}
	ann := li["annotator"].(map[string]interface{})
	if ann["name"] != "张三" {
		t.Errorf("annotator.name = %v, want 张三 (stored name preferred)", ann["name"])
	}
	if ann["type"] != "mixed" {
		t.Errorf("annotator.type = %v, want mixed (ai + manual)", ann["type"])
	}

	pairs := li["qa_pairs"].([]interface{})
	if len(pairs) != 2 {
		t.Fatalf("qa_pairs len = %d, want 2", len(pairs))
	}
	p0 := pairs[0].(map[string]interface{})
	if _, leaked := p0["candidate_run_id"]; leaked {
		t.Errorf("candidate_run_id leaked into export envelope")
	}
	if _, leaked := p0["model"]; leaked {
		t.Errorf("model leaked into qa_pair (belongs in generated_flag)")
	}
	span := p0["span"].(map[string]interface{})
	if span["field"] != "full_text" || span["start"].(float64) != 10 || span["end"].(float64) != 42 {
		t.Errorf("span = %v", span)
	}

	gen := got["generated_flag"].(map[string]interface{})
	if gen["generated"] != true {
		t.Errorf("generated = %v, want true", gen["generated"])
	}
	models := gen["models"].([]interface{})
	if len(models) != 1 || models[0] != "qwen-vl-max" {
		t.Errorf("models = %v, want [qwen-vl-max]", models)
	}
	if gen["verified"] != true {
		t.Errorf("verified = %v, want true (refined stage)", gen["verified"])
	}
}

// TestBuildTextEnvelope_FallbackAnnotator verifies the annotator name falls back
// to the resolved user when no stored name exists, and content falls back across
// field names.
func TestBuildTextEnvelope_FallbackAnnotator(t *testing.T) {
	doc := paymodel.Document{
		DocKey:    "d2",
		Version:   1,
		CreatedBy: 5,
		Data: map[string]interface{}{
			"text":     "正文内容",
			"qa_pairs": []interface{}{map[string]interface{}{"question": "问题一二三四五", "answer": "答", "source": "manual", "confirmed": false}},
		},
	}
	env := buildTextEnvelope(doc, exportEnvelopeMeta{}, func(id uint) string {
		if id == 5 {
			return "李四"
		}
		return ""
	})
	li := env["label_info"].(map[string]interface{})
	ann := li["annotator"].(map[string]interface{})
	if ann["name"] != "李四" {
		t.Errorf("annotator.name = %v, want 李四 (resolved from created_by)", ann["name"])
	}
	if env["content"] != "正文内容" {
		t.Errorf("content = %v, want 正文内容", env["content"])
	}
	if env["data_version"] != "V1.0.1" {
		t.Errorf("data_version = %v, want V1.0.1 (default prefix)", env["data_version"])
	}
	gen := env["generated_flag"].(map[string]interface{})
	if gen["generated"] != false {
		t.Errorf("generated = %v, want false (manual only)", gen["generated"])
	}
}
