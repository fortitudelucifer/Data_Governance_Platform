package relational

import (
	"time"
)

// QC status enum for Asset.QCStatus. See plan_v1/01 §2 / §11 R-06.
const (
	QCStatusPending = "pending"
	QCStatusRunning = "running"
	QCStatusPassed  = "passed"
	QCStatusFailed  = "failed"
)

// Asset represents a multi-modal resource (image, audio, video, ...) stored
// outside the database via an ObjectStore. The relational row keeps metadata only.
//
// P0 only persists image fields. The duration_ms / fps / sample_rate columns
// are reserved for audio/video and remain nullable per ADR C-04 (see plan_v1/01
// §10).
type Asset struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// (dataset_id, sha256) 上有**部分唯一索引** idx_assets_dataset_sha（仅
	// qc_status='passed'，M6）：去重的唯一性是数据库的属性，不是「记得先查后写」
	// 的属性。QC 失败行是给操作员看的拒收记录，同一个坏文件反复上传不该撞唯一键，
	// 所以谓词只盖 passed。schema 唯一真源 = goose 迁移（000001_init.sql）；
	// 单测夹具跑同一份迁移（testutil.DB），所以测试踩到的就是这个真约束。
	DatasetID     uint   `gorm:"index;not null" json:"dataset_id"`
	ParentAssetID *uint  `gorm:"index" json:"parent_asset_id"` // R-10: long doc / long image split, P1
	Modality      string `gorm:"size:16;not null;default:'image';index" json:"modality"`
	StorageURI    string `gorm:"size:512;not null" json:"storage_uri"`
	OriginalName  string `gorm:"size:512" json:"original_name"`
	MIME          string `gorm:"size:64" json:"mime"`
	SHA256        string `gorm:"size:64;index;not null" json:"sha256"`
	SizeBytes     int64  `gorm:"not null" json:"size_bytes"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`

	// Audio / video reservation. P0 leaves these zero / nil for images.
	DurationMs *int64   `json:"duration_ms"`
	FPS        *float64 `json:"fps"`
	SampleRate *int     `json:"sample_rate"`

	QCStatus string `gorm:"size:16;not null;default:'pending';index" json:"qc_status"`
	QCReport JSON   `gorm:"type:jsonb" json:"qc_report"`
	Features JSON   `gorm:"type:jsonb" json:"features"`

	// Derived-asset preprocessing state (T0.3). The media-worker computes
	// waveform peaks / frame index / thumbnails for audio/video. Empty for
	// image/text. Status: ""(n/a) | pending | running | ready | failed.
	PreprocessStatus        string     `gorm:"size:16;index" json:"preprocess_status"`
	PreprocessError         string     `gorm:"type:text" json:"preprocess_error,omitempty"`
	PreprocessAttempts      int        `gorm:"not null;default:0" json:"preprocess_attempts"`
	PreprocessLeaseUntil    *time.Time `json:"preprocess_lease_until,omitempty"`
	PreprocessNextAttemptAt *time.Time `gorm:"index" json:"preprocess_next_attempt_at,omitempty"`

	UploaderID uint `gorm:"index" json:"uploader_id"`

	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName overrides the default `assets` plural so dataset/audit/legacy code
// that may scan tables by name stays unambiguous.
func (Asset) TableName() string { return "assets" }
