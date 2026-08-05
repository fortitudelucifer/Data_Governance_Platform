package builtin

import (
	"strings"
	"testing"
)

func TestCSVImportPlugin_ParseValid(t *testing.T) {
	p := &CSVImportPlugin{}
	input := "doc_key,text,category\ndoc1,hello,A\ndoc2,world,B\n"
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
	if docs[0].Data["text"] != "hello" {
		t.Errorf("expected text 'hello', got %v", docs[0].Data["text"])
	}
}

func TestCSVImportPlugin_ValidateMissingDocKeyColumn(t *testing.T) {
	p := &CSVImportPlugin{}
	input := "name,text\nfoo,bar\n"
	err := p.Validate(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for missing doc_key column")
	}
}

func TestCSVImportPlugin_ValidateNoDataRows(t *testing.T) {
	p := &CSVImportPlugin{}
	input := "doc_key,text\n"
	err := p.Validate(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for CSV with no data rows")
	}
}

func TestCSVImportPlugin_ParseEmptyDocKey(t *testing.T) {
	p := &CSVImportPlugin{}
	input := "doc_key,text\n,hello\n"
	_, err := p.Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for empty doc_key")
	}
}

func TestCSVImportPlugin_ValidateValidCSV(t *testing.T) {
	p := &CSVImportPlugin{}
	input := "doc_key,text\ndoc1,hello\n"
	err := p.Validate(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCSVImportPlugin_FormatAndExtensions(t *testing.T) {
	p := &CSVImportPlugin{}
	if p.FormatID() != "csv" {
		t.Errorf("expected format ID 'csv', got %q", p.FormatID())
	}
	exts := p.SupportedExtensions()
	if len(exts) != 1 || exts[0] != ".csv" {
		t.Errorf("unexpected extensions: %v", exts)
	}
}
