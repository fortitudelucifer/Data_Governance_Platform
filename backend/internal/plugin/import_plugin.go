package plugin

import (
	"io"

	paymodel "text-annotation-platform/internal/model/payload"
)

// ImportPlugin defines the interface for file import plugins.
type ImportPlugin interface {
	// FormatID returns the format identifier, e.g. "json", "csv".
	FormatID() string
	// SupportedExtensions returns the list of supported file extensions, e.g. [".json", ".jsonl"].
	SupportedExtensions() []string
	// Validate checks whether the reader content conforms to this format.
	Validate(reader io.Reader) error
	// Parse parses the reader content and returns a list of parsed documents.
	Parse(reader io.Reader) ([]paymodel.ParsedDocument, error)
}
