package relational

import (
	"time"
)

// AnnotationLog stores detailed tracking of annotation actions, including LLM Refinement.
type AnnotationLog struct {
	LogID      uint      `gorm:"primaryKey;autoIncrement" json:"log_id"`
	DocID      string    `gorm:"size:64;not null;index" json:"doc_id"`
	UserID     uint      `gorm:"not null;index" json:"user_id"`
	Action     string    `gorm:"size:32;not null" json:"action"`
	FromStatus string    `gorm:"size:32;not null" json:"from_status"`
	ToStatus   string    `gorm:"size:32;not null" json:"to_status"`
	Score      *int      `gorm:"type:smallint" json:"score,omitempty"`
	Details    string    `gorm:"type:json" json:"details,omitempty"`
	Reasoning  string    `gorm:"type:text" json:"reasoning,omitempty"`
	CreatedAt  time.Time `gorm:"autoCreateTime;index" json:"created_at"`
}
