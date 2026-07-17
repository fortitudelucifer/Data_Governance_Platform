package service

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"text-annotation-platform/internal/repository"
	"text-annotation-platform/internal/testutil"
)

// TestDatasetFunctionBackwardCompat_Property verifies that adding a new
// DatasetFunction does not change existing datasets' function_id or
// workflow_config.
//
// Feature: annotation-workflow-v2, Property 19: 新增功能分类向后兼容
// **Validates: Requirement 8.10**
func TestDatasetFunctionBackwardCompat_Property(t *testing.T) {
	dbRepo := &repository.DB{DB: testutil.DB(t, repository.RunMigrations)}

	fnSvc := NewDatasetFunctionService(dbRepo)
	dsSvc := NewDatasetService(dbRepo, nil)
	ctx := context.Background()

	const iterations = 100
	rng := rand.New(rand.NewSource(42))

	// Seed initial dataset functions
	fn1, err := fnSvc.Create(ctx, CreateFunctionReq{
		Name:           "pretrain_label",
		WorkflowConfig: `{"layers":["auto","manual"]}`,
		SortOrder:      1,
	})
	if err != nil {
		t.Fatalf("seed fn1 failed: %v", err)
	}
	fn2, err := fnSvc.Create(ctx, CreateFunctionReq{
		Name:           "task_finetune",
		WorkflowConfig: `{"layers":["manual"]}`,
		SortOrder:      2,
	})
	if err != nil {
		t.Fatalf("seed fn2 failed: %v", err)
	}

	// Create datasets with existing functions
	type snapshot struct {
		DatasetID      uint
		FunctionID     *uint
		WorkflowConfig string
	}
	var snapshots []snapshot

	for i := 0; i < iterations; i++ {
		var funcID *uint
		var expectedConfig string
		switch rng.Intn(3) {
		case 0:
			funcID = nil
			expectedConfig = ""
		case 1:
			funcID = &fn1.ID
			expectedConfig = fn1.WorkflowConfig
		case 2:
			funcID = &fn2.ID
			expectedConfig = fn2.WorkflowConfig
		}

		ds, err := dsSvc.CreateDataset(context.Background(),
			fmt.Sprintf("compat-ds-%d", i), 0, 1, nil, nil, "qa", "criminal", funcID,
		)
		if err != nil {
			t.Fatalf("iteration %d: CreateDataset failed: %v", i, err)
		}

		snapshots = append(snapshots, snapshot{
			DatasetID:      ds.ID,
			FunctionID:     funcID,
			WorkflowConfig: expectedConfig,
		})
	}

	// Now add new dataset functions —this should NOT affect existing datasets
	for i := 0; i < 10; i++ {
		_, err := fnSvc.Create(ctx, CreateFunctionReq{
			Name:           fmt.Sprintf("新功能 %d", i),
			WorkflowConfig: fmt.Sprintf(`{"new_field":"value_%d"}`, i),
			SortOrder:      10 + i,
		})
		if err != nil {
			t.Fatalf("create new function %d failed: %v", i, err)
		}
	}

	// Verify all existing datasets are unchanged
	for _, snap := range snapshots {
		fetched, err := dbRepo.FindDatasetByID(context.Background(), snap.DatasetID)
		if err != nil {
			t.Errorf("dataset %d: FindDatasetByID failed: %v", snap.DatasetID, err)
			continue
		}

		// function_id unchanged
		if snap.FunctionID == nil {
			if fetched.DatasetFunctionID != nil {
				t.Errorf("dataset %d: function_id should be nil, got %v", snap.DatasetID, *fetched.DatasetFunctionID)
			}
		} else {
			if fetched.DatasetFunctionID == nil || *fetched.DatasetFunctionID != *snap.FunctionID {
				t.Errorf("dataset %d: function_id mismatch", snap.DatasetID)
			}
		}

		// workflow_config unchanged (via preloaded DatasetFunction)
		if snap.FunctionID != nil && fetched.DatasetFunction != nil {
			if fetched.DatasetFunction.WorkflowConfig != snap.WorkflowConfig {
				t.Errorf("dataset %d: workflow_config changed from %q to %q",
					snap.DatasetID, snap.WorkflowConfig, fetched.DatasetFunction.WorkflowConfig)
			}
		}
	}
}
