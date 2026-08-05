package relational

import "time"

// Upload session status enum (plan_v2 T0.2).
const (
	UploadPending   = "pending"
	UploadCompleted = "completed"
	UploadAborted   = "aborted"
	UploadExpired   = "expired"
	UploadFailed    = "failed"
)

// UploadSession tracks one resumable multipart upload. The bytes go straight
// from the browser to the object store via presigned part URLs; this row is the
// control-plane record (owner, dataset, temp/final keys, lifecycle). See
// plan_v2 执行方案-00 T0.2 "上传会话契约".
type UploadSession struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	SessionID      string     `gorm:"size:40;uniqueIndex;not null" json:"session_id"`
	UploadID       string     `gorm:"size:256;not null" json:"upload_id"` // object-store multipart id
	UserID         uint       `gorm:"index;not null" json:"user_id"`
	DatasetID      uint       `gorm:"index;not null" json:"dataset_id"`
	Modality       string     `gorm:"size:16" json:"modality"`
	Filename       string     `gorm:"size:512" json:"filename"`
	ContentType    string     `gorm:"size:128" json:"content_type"`
	SizeBytes      int64      `json:"size_bytes"`
	PartSize       int64      `json:"part_size"`
	TempObjectKey  string     `gorm:"size:512;not null" json:"temp_object_key"`
	FinalObjectKey string     `gorm:"size:512" json:"final_object_key"`
	ClientSHA256   string     `gorm:"size:64" json:"client_sha256"`
	ServerSHA256   string     `gorm:"size:64" json:"server_sha256"`
	Status         string     `gorm:"size:16;not null;default:'pending';index" json:"status"`
	Error          string     `gorm:"type:text" json:"error,omitempty"`
	AssetID        *uint      `json:"asset_id,omitempty"`
	ExpiresAt      time.Time  `gorm:"index" json:"expires_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName overrides the default plural.
func (UploadSession) TableName() string { return "upload_sessions" }
