package relational

import "time"

// DatasetFunction defines a dataset usage category (e.g. pre-training, fine-tuning)
// that determines the annotation workflow and UI layout.
type DatasetFunction struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Name           string    `gorm:"size:100;uniqueIndex;not null" json:"name"`
	Description    string    `gorm:"type:text" json:"description"`
	WorkflowConfig string    `gorm:"type:json;not null" json:"workflow_config"`
	LayoutConfig   *string   `gorm:"type:json" json:"layout_config,omitempty"`
	SortOrder      int       `gorm:"not null;default:0" json:"sort_order"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
