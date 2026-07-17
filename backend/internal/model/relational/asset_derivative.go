package relational

import "time"

// Derived-artifact kinds produced by the media-worker (plan_v2 T0.3).
const (
	DerivativeWaveform   = "waveform"    // audio peaks JSON (Peaks.js compatible)
	DerivativeFrameIndex = "frame_index" // video frame→pts_ms map JSON
	DerivativeThumbnail  = "thumbnail"   // video keyframe thumbnail (sprite or first frame)
	DerivativePlayback   = "playback_mp4" // browser-playable H.264/AAC transcode (for HEVC/AV1/… sources)
)

// Preprocess status enum for Asset.PreprocessStatus.
const (
	PreprocessPending  = "pending"
	PreprocessRunning  = "running"
	PreprocessReady    = "ready"
	PreprocessFailed   = "failed"   // transient failure — eligible for retry
	PreprocessRejected = "rejected" // terminal (e.g. unsupported codec) — never retried
)

// AssetDerivative records one derived artifact for an asset (waveform peaks,
// frame index, or keyframe thumbnail). One row per (asset_id, kind); re-runs
// upsert. The bytes live in the object store at StorageURI. See plan_v2 T0.3.
type AssetDerivative struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	AssetID    uint      `gorm:"uniqueIndex:idx_deriv_asset_kind;not null" json:"asset_id"`
	Kind       string    `gorm:"size:24;uniqueIndex:idx_deriv_asset_kind;not null" json:"kind"`
	Version    int       `gorm:"not null;default:1" json:"version"`
	ParamsHash string    `gorm:"size:32" json:"params_hash"`
	StorageURI string    `gorm:"size:512;not null" json:"storage_uri"`
	Status     string    `gorm:"size:16;not null;default:'ready'" json:"status"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `gorm:"size:64" json:"sha256"`
	Error      string    `gorm:"type:text" json:"error,omitempty"`
	CreatedAt  time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

// TableName overrides the default plural for clarity.
func (AssetDerivative) TableName() string { return "asset_derivatives" }
