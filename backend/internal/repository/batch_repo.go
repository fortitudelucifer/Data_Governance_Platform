package repository

import (
	"context"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// batch_repo.go — persistence for list-level batch auto-annotation jobs (item 3).

// CreateBatchJob inserts a new job row.
func (r *DB) CreateBatchJob(ctx context.Context, j *dbmodel.BatchJob) error {
	return r.DB.WithContext(ctx).Create(j).Error
}

// UpdateBatchJobProgress sets the running counters.
func (r *DB) UpdateBatchJobProgress(ctx context.Context, jobID string, done, failed int) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.BatchJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]interface{}{"done": done, "failed": failed}).Error
}

// SetBatchJobStatus updates the lifecycle status.
func (r *DB) SetBatchJobStatus(ctx context.Context, jobID, status string) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.BatchJob{}).
		Where("job_id = ?", jobID).Update("status", status).Error
}

// GetBatchJobStatus returns just the status (for the runner's cancel poll).
func (r *DB) GetBatchJobStatus(ctx context.Context, jobID string) (string, error) {
	var j dbmodel.BatchJob
	if err := r.DB.WithContext(ctx).Select("status").Where("job_id = ?", jobID).First(&j).Error; err != nil {
		return "", err
	}
	return j.Status, nil
}

// FindLatestBatchJobByDataset returns the most recent job for a dataset.
func (r *DB) FindLatestBatchJobByDataset(ctx context.Context, datasetID uint) (*dbmodel.BatchJob, error) {
	var j dbmodel.BatchJob
	err := r.DB.WithContext(ctx).Where("dataset_id = ?", datasetID).
		Order("id DESC").First(&j).Error
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// HasRunningBatchJob reports whether a dataset already has a running job.
func (r *DB) HasRunningBatchJob(ctx context.Context, datasetID uint) (bool, error) {
	var n int64
	err := r.DB.WithContext(ctx).Model(&dbmodel.BatchJob{}).
		Where("dataset_id = ? AND status = ?", datasetID, dbmodel.BatchStatusRunning).
		Count(&n).Error
	return n > 0, err
}

// ReconcileRunningBatchJobs marks any still-"running" jobs as "interrupted" —
// called on startup, since their in-memory goroutines are gone after a restart.
func (r *DB) ReconcileRunningBatchJobs(ctx context.Context) error {
	return r.DB.WithContext(ctx).Model(&dbmodel.BatchJob{}).
		Where("status = ?", dbmodel.BatchStatusRunning).
		Update("status", dbmodel.BatchStatusInterrupted).Error
}
