package builtin

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	paymodel "text-annotation-platform/internal/model/payload"
)

// CSVExportPlugin implements ExportPlugin for CSV format.
type CSVExportPlugin struct{}

func (p *CSVExportPlugin) FormatID() string {
	return "csv"
}

func (p *CSVExportPlugin) Name() string {
	return "CSV"
}

func (p *CSVExportPlugin) FileExtension() string {
	return ".csv"
}

// envelopeCSVColumns is the fixed 《通用元数据字段》 column order for CSV export.
// Nested objects (label_info / source_detail / generated_flag / rid) are
// flattened to JSON strings so the file stays a flat table.
var envelopeCSVColumns = []string{
	"id", "content", "label_info", "original_time", "modified_time",
	"data_version", "auth_type", "source_type", "source_detail", "generated_flag", "rid",
}

// Serialize writes CSV. Envelope documents use the 《通用元数据字段》 columns;
// legacy documents fall back to doc_key, version, data, created_by.
func (p *CSVExportPlugin) Serialize(docs []paymodel.ExportDocument, writer io.Writer) error {
	w := csv.NewWriter(writer)
	defer w.Flush()

	useEnvelope := len(docs) > 0 && docs[0].Envelope != nil
	if useEnvelope {
		return p.serializeEnvelope(w, docs)
	}

	// Legacy: header doc_key, version, data, created_by.
	if err := w.Write([]string{"doc_key", "version", "data", "created_by"}); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}
	for i, doc := range docs {
		dataJSON, err := json.Marshal(doc.Data)
		if err != nil {
			return fmt.Errorf("record %d: failed to marshal data: %w", i, err)
		}
		row := []string{
			doc.DocKey,
			strconv.Itoa(doc.Version),
			string(dataJSON),
			strconv.FormatUint(uint64(doc.CreatedBy), 10),
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("record %d: failed to write CSV row: %w", i, err)
		}
	}
	return nil
}

func (p *CSVExportPlugin) serializeEnvelope(w *csv.Writer, docs []paymodel.ExportDocument) error {
	if err := w.Write(envelopeCSVColumns); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}
	for i, doc := range docs {
		row := make([]string, len(envelopeCSVColumns))
		for j, col := range envelopeCSVColumns {
			cell, err := csvCell(doc.Envelope[col])
			if err != nil {
				return fmt.Errorf("record %d: field %s: %w", i, col, err)
			}
			row[j] = cell
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("record %d: failed to write CSV row: %w", i, err)
		}
	}
	return nil
}

// csvCell renders an envelope value as a CSV cell: scalars verbatim, nested
// objects/arrays as compact JSON, nil as empty.
func csvCell(v interface{}) (string, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string:
		return val, nil
	case bool:
		return strconv.FormatBool(val), nil
	case int:
		return strconv.Itoa(val), nil
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
