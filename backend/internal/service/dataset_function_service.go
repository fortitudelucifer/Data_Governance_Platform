package service

import (
	"context"
	"fmt"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// DatasetFunctionService manages dataset function classifications.
type DatasetFunctionService struct {
	dbRepo *repository.DB
}

// NewDatasetFunctionService creates a new DatasetFunctionService.
func NewDatasetFunctionService(dbRepo *repository.DB) *DatasetFunctionService {
	return &DatasetFunctionService{dbRepo: dbRepo}
}

// CreateFunctionReq describes a request to create a dataset function.
type CreateFunctionReq struct {
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	WorkflowConfig string  `json:"workflow_config"`
	LayoutConfig   *string `json:"layout_config,omitempty"`
	SortOrder      int     `json:"sort_order"`
}

// UpdateFunctionReq describes a request to update a dataset function.
type UpdateFunctionReq struct {
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	WorkflowConfig string  `json:"workflow_config"`
	LayoutConfig   *string `json:"layout_config,omitempty"`
	SortOrder      int     `json:"sort_order"`
}

// Create creates a new dataset function.
func (s *DatasetFunctionService) Create(ctx context.Context, req CreateFunctionReq) (*dbmodel.DatasetFunction, error) {
	fn := &dbmodel.DatasetFunction{
		Name:           req.Name,
		Description:    req.Description,
		WorkflowConfig: req.WorkflowConfig,
		LayoutConfig:   req.LayoutConfig,
		SortOrder:      req.SortOrder,
	}
	if err := s.dbRepo.DB.Create(fn).Error; err != nil {
		return nil, fmt.Errorf("failed to create dataset function: %w", err)
	}
	return fn, nil
}

// Update updates an existing dataset function.
func (s *DatasetFunctionService) Update(ctx context.Context, id uint, req UpdateFunctionReq) error {
	updates := map[string]interface{}{
		"name":            req.Name,
		"description":     req.Description,
		"workflow_config": req.WorkflowConfig,
		"layout_config":   req.LayoutConfig,
		"sort_order":      req.SortOrder,
	}
	result := s.dbRepo.DB.Model(&dbmodel.DatasetFunction{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to update dataset function: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("dataset function not found: %d", id)
	}
	return nil
}

// Delete removes a dataset function by ID.
func (s *DatasetFunctionService) Delete(ctx context.Context, id uint) error {
	result := s.dbRepo.DB.Delete(&dbmodel.DatasetFunction{}, id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete dataset function: %w", result.Error)
	}
	return nil
}

// List returns all dataset functions ordered by sort_order.
func (s *DatasetFunctionService) List(ctx context.Context) ([]dbmodel.DatasetFunction, error) {
	var functions []dbmodel.DatasetFunction
	if err := s.dbRepo.DB.Order("sort_order ASC").Find(&functions).Error; err != nil {
		return nil, fmt.Errorf("failed to list dataset functions: %w", err)
	}
	return functions, nil
}

// GetByID returns a dataset function by ID.
func (s *DatasetFunctionService) GetByID(ctx context.Context, id uint) (*dbmodel.DatasetFunction, error) {
	var fn dbmodel.DatasetFunction
	if err := s.dbRepo.DB.First(&fn, id).Error; err != nil {
		return nil, fmt.Errorf("dataset function not found: %w", err)
	}
	return &fn, nil
}
