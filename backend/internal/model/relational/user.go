package relational

import "time"

// Role constants for the platform RBAC. The legacy "admin" / "annotator" /
// "reviewer" roles are kept for the existing text annotation track. The
// multi-modal tracks add modality-scoped annotator/reviewer aliases. Permission
// resolution still happens in
// middleware.RequireRole(...) — these constants only document the canonical
// names so handlers and tests stay in sync.
const (
	RoleAdmin          = "admin"
	RoleAnnotator      = "annotator"
	RoleReviewer       = "reviewer"
	RoleImageAnnotator = "image_annotator"
	RoleImageReviewer  = "image_reviewer"
	RoleAudioAnnotator = "audio_annotator"
	RoleAudioReviewer  = "audio_reviewer"
	RoleVideoAnnotator = "video_annotator"
	RoleVideoReviewer  = "video_reviewer"
)

// User represents a platform user stored in the relational database.
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string     `gorm:"size:255;not null" json:"-"`
	Role         string     `gorm:"size:20;not null;default:'annotator'" json:"role"`
	EmployeeID   *string    `gorm:"uniqueIndex;size:64" json:"employee_id"`
	Email        *string    `gorm:"uniqueIndex;size:128" json:"email"`
	SSOID        *string    `gorm:"uniqueIndex;size:128" json:"sso_id"`
	Status       string     `gorm:"not null;default:'active'" json:"status"` // "active" or "disabled"
	DisplayName  string     `json:"display_name"`
	LastLoginAt  *time.Time `gorm:"index" json:"last_login_at"`
	CreatedAt    time.Time  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
}
