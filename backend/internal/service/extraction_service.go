package service

import (
	"context"
	"encoding/json"
	"fmt"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/plugin"
	"text-annotation-platform/internal/repository"
)

// ExtractionService handles document extraction with filter pipelines.
type ExtractionService struct {
	filterRegistry *plugin.PluginRegistry[plugin.ExtractionFilter]
	docRepo      repository.DocumentDB
	dbRepo         *repository.DB
}

// NewExtractionService creates a new ExtractionService.
func NewExtractionService(
	filterRegistry *plugin.PluginRegistry[plugin.ExtractionFilter],
	docRepo repository.DocumentDB,
	dbRepo *repository.DB,
) *ExtractionService {
	return &ExtractionService{
		filterRegistry: filterRegistry,
		docRepo:      docRepo,
		dbRepo:         dbRepo,
	}
}

// ExtractionRequest describes an extraction operation.
type ExtractionRequest struct {
	DatasetID uint                     `json:"dataset_id"`
	Name      string                   `json:"name"`
	Filters   []ExtractionFilterConfig `json:"filters"`
}

// ExtractionFilterConfig describes a single filter in the pipeline.
type ExtractionFilterConfig struct {
	Type   string                 `json:"type"`
	Params map[string]interface{} `json:"params"`
}

// Execute runs the extraction pipeline and persists the result.
func (s *ExtractionService) Execute(ctx context.Context, req ExtractionRequest) (*dbmodel.ExtractionResult, error) {
	// Load all active documents for the dataset
	docs, err := s.docRepo.FindDocumentsByDataset(ctx, req.DatasetID, map[string]interface{}{}, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to load documents: %w", err)
	}

	totalCount := len(docs)
	filtered := docs

	// Apply each filter in sequence (AND pipeline)
	for _, fc := range req.Filters {
		f, err := s.filterRegistry.Get(fc.Type)
		if err != nil {
			available := make([]string, 0)
			for id := range s.filterRegistry.List() {
				available = append(available, id)
			}
			return nil, fmt.Errorf("unsupported filter '%s', available: %v", fc.Type, available)
		}

		if err := f.ValidateParams(fc.Params); err != nil {
			return nil, fmt.Errorf("filter '%s' parameter error: %w", fc.Type, err)
		}

		filtered, err = f.Apply(ctx, filtered, fc.Params)
		if err != nil {
			return nil, fmt.Errorf("filter '%s' execution failed: %w", fc.Type, err)
		}
	}

	// Collect matched doc_keys
	docKeys := make([]string, len(filtered))
	for i, doc := range filtered {
		docKeys[i] = doc.DocKey
	}

	// Serialize for storage
	filterConfigJSON, _ := json.Marshal(req.Filters)
	docKeysJSON, _ := json.Marshal(docKeys)

	name := req.Name
	if name == "" {
		name = fmt.Sprintf("extraction-%d", req.DatasetID)
	}

	result := &dbmodel.ExtractionResult{
		DatasetID:    req.DatasetID,
		Name:         name,
		FilterConfig: string(filterConfigJSON),
		DocKeys:      string(docKeysJSON),
		MatchedCount: len(docKeys),
		TotalCount:   totalCount,
	}

	if err := s.dbRepo.DB.Create(result).Error; err != nil {
		return nil, fmt.Errorf("failed to save extraction result: %w", err)
	}

	return result, nil
}

// ListResults returns all extraction results for a dataset.
func (s *ExtractionService) ListResults(ctx context.Context, datasetID uint) ([]dbmodel.ExtractionResult, error) {
	var results []dbmodel.ExtractionResult
	if err := s.dbRepo.DB.Where("dataset_id = ?", datasetID).Order("created_at DESC").Find(&results).Error; err != nil {
		return nil, fmt.Errorf("failed to list extraction results: %w", err)
	}
	return results, nil
}

// GetResultByID returns a single extraction result by ID.
func (s *ExtractionService) GetResultByID(ctx context.Context, id uint) (*dbmodel.ExtractionResult, error) {
	var result dbmodel.ExtractionResult
	if err := s.dbRepo.DB.First(&result, id).Error; err != nil {
		return nil, fmt.Errorf("extraction result not found: %w", err)
	}
	return &result, nil
}
