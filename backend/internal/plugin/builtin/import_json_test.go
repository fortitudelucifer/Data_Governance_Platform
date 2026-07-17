package builtin

import (
	"strings"
	"testing"
)

func TestJSONImportPlugin_ParseValidArray(t *testing.T) {
	p := &JSONImportPlugin{}
	input := `[{"doc_key":"doc1","text":"hello"},{"doc_key":"doc2","text":"world"}]`
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].DocKey != "doc1" {
		t.Errorf("expected doc_key 'doc1', got %q", docs[0].DocKey)
	}
	if docs[1].DocKey != "doc2" {
		t.Errorf("expected doc_key 'doc2', got %q", docs[1].DocKey)
	}
}

func TestJSONImportPlugin_ParseValidJSONL(t *testing.T) {
	p := &JSONImportPlugin{}
	input := "{\"doc_key\":\"a\",\"val\":1}\n{\"doc_key\":\"b\",\"val\":2}\n"
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].DocKey != "a" || docs[1].DocKey != "b" {
		t.Fatalf("JSONL order not preserved: got %q, %q", docs[0].DocKey, docs[1].DocKey)
	}
	if docs[0].Data["val"] != float64(1) || docs[1].Data["val"] != float64(2) {
		t.Fatalf("unexpected JSONL payload order: %#v", docs)
	}
}

func TestJSONImportPlugin_ParseJSONLWithUTF8BOM(t *testing.T) {
	p := &JSONImportPlugin{}
	input := "\xEF\xBB\xBF{\"doc_key\":\"bom1\",\"text\":\"hello\"}\n{\"doc_key\":\"bom2\",\"text\":\"world\"}\n"
	if err := p.Validate(strings.NewReader(input)); err != nil {
		t.Fatalf("Validate should ignore UTF-8 BOM: %v", err)
	}
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse should ignore UTF-8 BOM: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].DocKey != "bom1" || docs[1].DocKey != "bom2" {
		t.Fatalf("unexpected doc order after BOM trim: %#v", docs)
	}
}

func TestJSONImportPlugin_ParseJSONLWithoutDocKeyKeepsLineOrder(t *testing.T) {
	p := &JSONImportPlugin{}
	input := "{\"text\":\"first\"}\n{\"text\":\"second\"}\n"
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].Data["text"] != "first" || docs[1].Data["text"] != "second" {
		t.Fatalf("JSONL line order not preserved: %#v", docs)
	}
	if !strings.HasPrefix(docs[0].DocKey, "auto_") || !strings.HasSuffix(docs[0].DocKey, "_1") {
		t.Fatalf("first JSONL row should synthesize an order-aware doc_key, got %q", docs[0].DocKey)
	}
	if !strings.HasPrefix(docs[1].DocKey, "auto_") || !strings.HasSuffix(docs[1].DocKey, "_2") {
		t.Fatalf("second JSONL row should synthesize an order-aware doc_key, got %q", docs[1].DocKey)
	}
}

func TestJSONImportPlugin_ValidateInvalidJSON(t *testing.T) {
	p := &JSONImportPlugin{}
	input := `{not valid json`
	err := p.Validate(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestJSONImportPlugin_ValidateMissingDocKey(t *testing.T) {
	// Current contract (see extractDocKey in import_json.go): Validate only
	// checks JSON syntax, and Parse synthesizes an `auto_<sha1>_<index>`
	// doc_key when neither doc_key nor id are provided so the import does
	// not silently drop records. This test pins down both behaviors.
	p := &JSONImportPlugin{}
	input := `[{"text":"no doc_key here"}]`
	if err := p.Validate(strings.NewReader(input)); err != nil {
		t.Fatalf("Validate should pass on syntactically valid JSON: %v", err)
	}
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse should synthesize a doc_key, got error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if !strings.HasPrefix(docs[0].DocKey, "auto_") {
		t.Fatalf("expected synthesized doc_key with auto_ prefix, got %q", docs[0].DocKey)
	}
}

func TestJSONImportPlugin_ParseEmpty(t *testing.T) {
	p := &JSONImportPlugin{}
	_, err := p.Parse(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestJSONImportPlugin_ValidateEmptyArray(t *testing.T) {
	p := &JSONImportPlugin{}
	err := p.Validate(strings.NewReader("[]"))
	if err != nil {
		t.Fatalf("empty array should be valid: %v", err)
	}
}

func TestJSONImportPlugin_FormatAndExtensions(t *testing.T) {
	p := &JSONImportPlugin{}
	if p.FormatID() != "json" {
		t.Errorf("expected format ID 'json', got %q", p.FormatID())
	}
	exts := p.SupportedExtensions()
	if len(exts) != 2 || exts[0] != ".json" || exts[1] != ".jsonl" {
		t.Errorf("unexpected extensions: %v", exts)
	}
}

func TestJSONImportPlugin_ParseStructuredAnswerString(t *testing.T) {
	p := &JSONImportPlugin{}
	input := `{"doc_key":"d1","qa_pairs":[{"question":"法条","answer":"[\"中华人民共和国保险法 第十条\",\"中华人民共和国保险法 第十四条\"]"}]}`
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	rawPairs, ok := docs[0].Data["qa_pairs"].([]interface{})
	if !ok || len(rawPairs) != 1 {
		t.Fatalf("expected one qa pair")
	}
	pair, ok := rawPairs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("invalid qa pair type")
	}
	if pair["answer"] != "中华人民共和国保险法 第十条；中华人民共和国保险法 第十四条" {
		t.Fatalf("unexpected normalized answer: %v", pair["answer"])
	}
	meta, ok := pair["meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected meta map")
	}
	if _, ok := meta["raw_answer"]; !ok {
		t.Fatalf("expected raw_answer in meta")
	}
}

func TestJSONImportPlugin_ParseStructuredAnswerObject(t *testing.T) {
	p := &JSONImportPlugin{}
	input := `{"doc_key":"d2","qa_pairs":[{"question":"赔偿","answer":{"赔偿/给付金额":[{"类型":"赔偿金","金额_元":47000}]}}]}`
	docs, err := p.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rawPairs, ok := docs[0].Data["qa_pairs"].([]interface{})
	if !ok || len(rawPairs) != 1 {
		t.Fatalf("expected one qa pair")
	}
	pair, ok := rawPairs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("invalid qa pair type")
	}
	// Normalization template (import_json.go §赔偿/给付金额) renders the
	// payments list with a "金额：" prefix and joins type+amount+元 without
	// a separator. This pins down the current contract; if the template is
	// re-tuned, both sides must move together.
	if pair["answer"] != "金额：赔偿金47000元" {
		t.Fatalf("unexpected normalized answer: %v", pair["answer"])
	}
	meta, ok := pair["meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected meta map")
	}
	if _, ok := meta["answer_structured"]; !ok {
		t.Fatalf("expected answer_structured in meta")
	}
}
