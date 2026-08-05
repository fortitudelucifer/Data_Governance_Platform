package relational

import "time"

// AutoPromptTemplate stores system/user prompt pairs for text auto annotation.
type AutoPromptTemplate struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	Name               string    `gorm:"size:100;not null" json:"name"`
	CaseType           string    `gorm:"size:50;index;not null" json:"case_type"`
	TaskType           string    `gorm:"size:50;index;not null;default:'text_auto_qa'" json:"task_type"`
	SystemPrompt       string    `gorm:"type:text;not null" json:"system_prompt"`
	UserPromptTemplate string    `gorm:"type:text;not null" json:"user_prompt_template"`
	OutputSchema       string    `gorm:"type:text" json:"output_schema"`
	Guide              string    `gorm:"type:text" json:"guide"`
	Enabled            bool      `gorm:"not null;default:true;index" json:"enabled"`
	Version            int       `gorm:"not null;default:1" json:"version"`
	CreatedBy          uint      `gorm:"not null;default:0" json:"created_by"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
