package relational

import "time"

// SystemPrompt stores case-type-specific LLM prompt templates.
type SystemPrompt struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CaseType  string    `gorm:"size:50;uniqueIndex;not null" json:"case_type"`
	Name      string    `gorm:"size:100;not null" json:"name"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
