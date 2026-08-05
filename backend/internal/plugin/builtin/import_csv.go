package builtin

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"

	paymodel "text-annotation-platform/internal/model/payload"
)

// CSVImportPlugin implements ImportPlugin for CSV files.
type CSVImportPlugin struct{}

func (p *CSVImportPlugin) FormatID() string {
	return "csv"
}

func (p *CSVImportPlugin) SupportedExtensions() []string {
	return []string{".csv"}
}

// Validate checks that the content is valid CSV with a doc_key header.
func (p *CSVImportPlugin) Validate(reader io.Reader) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return fmt.Errorf("failed to read CSV header: %w", err)
	}
	if !containsString(headers, "doc_key") {
		return fmt.Errorf("CSV header missing required column 'doc_key'")
	}
	// Read at least one data row to validate
	_, err = r.Read()
	if err == io.EOF {
		return fmt.Errorf("CSV file has no data rows")
	}
	if err != nil {
		return fmt.Errorf("failed to read CSV data: %w", err)
	}
	return nil
}

// Parse parses CSV content into ParsedDocuments.
func (p *CSVImportPlugin) Parse(reader io.Reader) ([]paymodel.ParsedDocument, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	r := csv.NewReader(bytes.NewReader(data))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV header: %w", err)
	}

	docKeyIdx := -1
	for i, h := range headers {
		if h == "doc_key" {
			docKeyIdx = i
			break
		}
	}
	if docKeyIdx < 0 {
		return nil, fmt.Errorf("CSV header missing required column 'doc_key'")
	}

	var docs []paymodel.ParsedDocument
	rowNum := 1
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: failed to read CSV: %w", rowNum+1, err)
		}
		data := make(map[string]interface{})
		for i, h := range headers {
			if i < len(record) {
				data[h] = record[i]
			}
		}
		docKey := record[docKeyIdx]
		if docKey == "" {
			return nil, fmt.Errorf("row %d: 'doc_key' is empty", rowNum+1)
		}
		docs = append(docs, paymodel.ParsedDocument{
			DocKey: docKey,
			Data:   data,
		})
		rowNum++
	}
	return docs, nil
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
