package relational

import "time"

// Batch auto-annotation job status (item 3 / item 4).
const (
	BatchStatusRunning     = "running"
	BatchStatusCompleted   = "completed"
	BatchStatusCancelled   = "cancelled"
	BatchStatusInterrupted = "interrupted" // process died mid-run (reconciled on startup)
)

// BatchJob persists a list-level batch auto-annotation job so its progress
// survives restarts, is visible across instances, and can be cancelled
// cross-instance (the runner polls Status). One active job per dataset.
type BatchJob struct {
	ID         uint      `gorm:"primaryKey" json:"-"`
	JobID      string    `gorm:"size:32;uniqueIndex;not null" json:"job_id"`
	DatasetID  uint      `gorm:"index;not null" json:"dataset_id"`
	Capability string    `gorm:"size:64" json:"capability"`
	Model      string    `gorm:"size:128" json:"model"`
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	Failed     int       `json:"failed"`
	Status     string    `gorm:"size:16;index" json:"status"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName overrides the default plural.
func (BatchJob) TableName() string { return "batch_jobs" }
