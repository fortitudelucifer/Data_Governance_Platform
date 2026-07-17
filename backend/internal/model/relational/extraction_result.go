package relational

import "time"

// ExtractionResult stores the outcome of a dataset document extraction operation.
type ExtractionResult struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	DatasetID    uint      `gorm:"index;not null" json:"dataset_id"`
	Name         string    `gorm:"size:200;not null" json:"name"`
	FilterConfig string    `gorm:"type:json;not null" json:"filter_config"`
	DocKeys      string    `gorm:"type:json;not null" json:"doc_keys"`
	MatchedCount int       `gorm:"not null" json:"matched_count"`
	TotalCount   int       `gorm:"not null" json:"total_count"`
	CreatedAt    time.Time `json:"created_at"`
}
