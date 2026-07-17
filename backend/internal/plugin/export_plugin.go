package plugin

import (
	"io"

	paymodel "text-annotation-platform/internal/model/payload"
)

// ExportPlugin defines the interface for data export plugins.
type ExportPlugin interface {
	// FormatID returns the format identifier, e.g. "jsonl", "csv".
	FormatID() string
	// Name returns a human-readable name for this format.
	Name() string
	// Serialize writes the documents to the writer in this format.
	Serialize(docs []paymodel.ExportDocument, writer io.Writer) error
	// FileExtension returns the file extension for exported files, e.g. ".jsonl".
	FileExtension() string
}
