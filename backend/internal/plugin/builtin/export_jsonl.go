package builtin

import (
	"encoding/json"
	"fmt"
	"io"

	paymodel "text-annotation-platform/internal/model/payload"
)

// JSONLExportPlugin implements ExportPlugin for JSONL (line-delimited JSON) format.
type JSONLExportPlugin struct{}

func (p *JSONLExportPlugin) FormatID() string {
	return "jsonl"
}

func (p *JSONLExportPlugin) Name() string {
	return "JSONL"
}

func (p *JSONLExportPlugin) FileExtension() string {
	return ".jsonl"
}

// jsonlRecord is the structure written per line.
type jsonlRecord struct {
	DocKey    string                 `json:"doc_key"`
	Version   int                    `json:"version"`
	Data      map[string]interface{} `json:"data"`
	CreatedBy uint                   `json:"created_by"`
}

// Serialize writes one JSON object per line. When a document carries an
// Envelope (《通用元数据字段》), that is written; otherwise the legacy record.
func (p *JSONLExportPlugin) Serialize(docs []paymodel.ExportDocument, writer io.Writer) error {
	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	for i, doc := range docs {
		var rec interface{}
		if doc.Envelope != nil {
			rec = doc.Envelope
		} else {
			rec = jsonlRecord{
				DocKey:    doc.DocKey,
				Version:   doc.Version,
				Data:      doc.Data,
				CreatedBy: doc.CreatedBy,
			}
		}
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("failed to encode record %d: %w", i, err)
		}
	}
	return nil
}
