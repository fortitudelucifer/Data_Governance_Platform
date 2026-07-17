package relational

import "time"

// AuditLog records a system audit event stored in the relational database.
type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Action     string    `gorm:"size:50;not null;index" json:"action"`
	TargetType string    `gorm:"size:50;not null" json:"target_type"`
	TargetID   string    `gorm:"size:100" json:"target_id"`
	UserID     uint      `gorm:"index;default:1" json:"user_id"`
	Result     string    `gorm:"size:20;not null" json:"result"`
	Detail     string    `gorm:"type:text" json:"detail"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}
