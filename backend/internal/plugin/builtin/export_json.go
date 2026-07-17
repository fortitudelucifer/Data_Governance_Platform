package builtin

import (
	"encoding/json"
	"fmt"
	"io"

	paymodel "text-annotation-platform/internal/model/payload"
)

// JSONExportPlugin implements ExportPlugin for a single JSON array file.
type JSONExportPlugin struct{}

func (p *JSONExportPlugin) FormatID() string {
	return "json"
}

func (p *JSONExportPlugin) Name() string {
	return "JSON"
}

func (p *JSONExportPlugin) FileExtension() string {
	return ".json"
}

// Serialize writes all documents as a JSON array. When a document carries an
// Envelope (《通用元数据字段》), that is written; otherwise the legacy record.
func (p *JSONExportPlugin) Serialize(docs []paymodel.ExportDocument, writer io.Writer) error {
	records := make([]interface{}, 0, len(docs))
	for _, doc := range docs {
		if doc.Envelope != nil {
			records = append(records, doc.Envelope)
			continue
		}
		records = append(records, jsonlRecord{
			DocKey:    doc.DocKey,
			Version:   doc.Version,
			Data:      doc.Data,
			CreatedBy: doc.CreatedBy,
		})
	}
	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(records); err != nil {
		return fmt.Errorf("failed to encode JSON export: %w", err)
	}
	return nil
}
