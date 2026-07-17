package payload

import "time"

// Collections for the video track pipeline (执行方案-02 §数据模型). Tracks live
// in their own collection (not HumanAnnotation.Fields blob) so they can be
// indexed for cross-task/dataset analysis and never hit the 16MB document cap.
const (
	CollTrack         = "mm_tracks"
	CollTrackSnapshot = "mm_track_snapshots"
)

// Track sources.
const (
	TrackSourceAI    = "ai"
	TrackSourceHuman = "human"
)

// Per-track review verdicts (B3.1). Empty string = not yet judged.
const (
	TrackReviewPassed   = "passed"
	TrackReviewRejected = "rejected"
)

// Track geometry kinds. TrackKindMask is dense per-frame segmentation, stored as
// a polygon outline in Keyframe.Points — SAM2's propagation output. True RLE /
// label volumes are Phase C (执行方案-02 §Phase C 硬约束: 稠密 mask 存储不同于
// 稀疏 shape). Exporters treat mask and polygon alike.
const (
	TrackKindBBox      = "bbox"
	TrackKindPolygon   = "polygon"
	TrackKindMask      = "mask"
	TrackKindPolyline  = "polyline"
	TrackKindKeypoints = "keypoints"
)

// Keyframe is one keyframe of a track: geometry + per-frame state. Only
// keyframes are stored; intermediate frames are linearly interpolated on ts_ms
// by the shared interpolation contract (see service.InterpolateAt + the
// testdata/interpolation golden fixtures). Geometry is in the rotation-applied
// original pixel space.
type Keyframe struct {
	Frame    int       `json:"frame"`
	TsMs     float64   `json:"ts_ms"`
	Bbox     []float64 `json:"bbox,omitempty"`     // [x,y,w,h]
	Points   []float64 `json:"points,omitempty"` // polygon/polyline/keypoints (flat)
	Outside  bool      `json:"outside"`
	Occluded bool      `json:"occluded"`
	Source   string    `json:"source,omitempty"` // human | ai (per-keyframe origin)
	// Attrs holds per-keyframe ontology attributes (scope="keyframe"). Invariant
	// attrs stay on the Track; mutable per-frame state lives here (CVAT model).
	Attrs map[string]interface{} `json:"attrs,omitempty"`
}

// Track is one tracked object across a video. Invariant attributes live on the
// track; mutable state lives per-keyframe (CVAT/Datumaro decoupling).
type Track struct {
	ID        string `json:"id"` // string hex ObjectID (clean round-trip)
	TaskID    uint   `json:"task_id"`
	DatasetID uint   `json:"dataset_id"`
	AssetID   uint   `json:"asset_id"`

	TrackID   int                    `json:"track_id"` // per-task logical track number (stable across adopt)
	Label     string                 `json:"label"`
	Kind      string                 `json:"kind,omitempty"` // bbox | polygon | polyline | keypoints
	Color     string                 `json:"color,omitempty"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"` // invariant attrs on the track
	Keyframes []Keyframe             `json:"keyframes"`

	Source      string  `json:"source"`                                 // ai | human
	AdoptedFrom *string `json:"adopted_from,omitempty"` // human track adopted from this archived AI track's _id

	// Per-track review verdict (B3.1). Empty = not yet judged. A task cannot pass
	// QA while any active track is still rejected.
	ReviewStatus string     `json:"review_status,omitempty"` // "" | passed | rejected
	ReviewNote   string     `json:"review_note,omitempty"`
	ReviewedBy   *uint      `json:"reviewed_by,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`

	Version   int       `json:"version"`     // optimistic-lock version
	IsActive  bool      `json:"is_active"` // false = archived (adopt/delete)
	CreatedBy uint      `json:"created_by"`
	UpdatedBy uint      `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TrackSnapshot is the FINALIZED, drift-safe copy of an active human Track,
// written on QA pass. Export reads ONLY snapshots, never live tracks.
type TrackSnapshot struct {
	ID        string `json:"id"`
	TaskID    uint   `json:"task_id"`
	DatasetID uint   `json:"dataset_id"`
	AssetID   uint   `json:"asset_id"`

	FinalAnnotationID   string `json:"final_annotation_id"`
	TrackID             int    `json:"track_id"`
	SourceTrackObjectID string `json:"source_track_object_id"`
	SourceTrackVersion  int    `json:"source_track_version"`

	Label     string                 `json:"label"`
	Kind      string                 `json:"kind,omitempty"`
	Color     string                 `json:"color,omitempty"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"`
	Keyframes []Keyframe             `json:"keyframes"`

	FinalizedBy uint      `json:"finalized_by"`
	FinalizedAt time.Time `json:"finalized_at"`
}
