package plugin

import (
	"context"

	paymodel "text-annotation-platform/internal/model/payload"
)

// ExtractionFilter defines the interface for document extraction filter plugins.
type ExtractionFilter interface {
	// FilterID returns the unique identifier for this filter.
	FilterID() string
	// Name returns a human-readable name.
	Name() string
	// Description returns a description of what this filter does.
	Description() string
	// ParamSchema returns a JSON Schema describing the filter's parameters.
	ParamSchema() map[string]interface{}
	// ValidateParams validates the given parameters.
	ValidateParams(params map[string]interface{}) error
	// Apply filters the document list and returns matching documents.
	Apply(ctx context.Context, docs []paymodel.Document, params map[string]interface{}) ([]paymodel.Document, error)
}
