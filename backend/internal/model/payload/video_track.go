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
// a polygon outline in Keyframe.Points — SAM2's propagation output. Exporters
// treat mask and polygon alike. TrackKindVoxelMask (Phase C) is a stack of
// per-slice bbox-local RLEs: keyframe.frame is the z index, geometry lives in
// Keyframe.RLE — sparse and inline per 00《稠密几何存储契约》规则 1 (the dense
// whole-volume voxel_label kind is external storage and never lands here).
// TrackKindCells (病理 C2) is many detected cells in ONE keyframe: geometry lives
// in Keyframe.Instances (each = bbox + bbox-local RLE + class + score). One ROI =
// one cells track = one keyframe holding all its cells (C-Q4). Like voxel_mask it
// does NOT interpolate; unlike it, the RLE is per-instance, not per-keyframe.
const (
	TrackKindBBox      = "bbox"
	TrackKindPolygon   = "polygon"
	TrackKindMask      = "mask"
	TrackKindPolyline  = "polyline"
	TrackKindKeypoints = "keypoints"
	TrackKindVoxelMask = "voxel_mask"
	TrackKindCells     = "cells"
)

// AllTrackKinds is the canonical enumeration of geometry kinds. It is the
// source of truth for the interpolation meta-test (both Go and TS): every kind
// listed here MUST have at least one golden fixture in testdata/interpolation/
// declaring it in "geometry_kinds". Adding a kind constant above without adding
// it here (and a fixture) is caught by TestEveryTrackKindHasFixture — a new
// kind that silently skips the interpolation contract is exactly the class of
// "no error anywhere, export quietly wrong" bug this project keeps paying for.
// Keep this list in sync with frontend ALL_TRACK_KINDS (trackInterpolation.ts).
var AllTrackKinds = []string{
	TrackKindBBox,
	TrackKindPolygon,
	TrackKindMask,
	TrackKindPolyline,
	TrackKindKeypoints,
	TrackKindVoxelMask,
	TrackKindCells,
}

// MaskRLE is a dense single-object mask in COCO compressed run-length form,
// relative to the keyframe's OWN bbox — deliberately not whole-image like
// standard COCO (a WSI is 100k×100k px; bbox-local is what keeps it inline-
// sized). Exporters must translate back to image coordinates via the bbox.
type MaskRLE struct {
	Size   [2]int `json:"size"`   // [h, w] of the bbox-local mask grid
	Counts string `json:"counts"` // COCO compressed RLE string
}

// CellInstance is one detected/annotated cell inside a TrackKindCells keyframe
// (病理 C2). Many instances live in one keyframe — one ROI's whole cell set is a
// single cells track (C-Q4: never one-cell-one-track, which would explode to
// ~67k rows/slide). Geometry = a bbox + a **bbox-local** COCO RLE, the SAME
// encoding as voxel_mask (coco_rle.go + the pycocotools lock apply verbatim);
// exporters must translate each RLE back to level-0 image coords via its own
// bbox (forgetting the shift = mask silently offset). RLE may be nil for a
// detection-only instance (box, no segmentation).
type CellInstance struct {
	Bbox  []float64 `json:"bbox"`            // [x,y,w,h] in level-0 px; the RLE's anchor — never omitempty
	RLE   *MaskRLE  `json:"rle,omitempty"`   // bbox-local mask; nil = detection box only
	Class string    `json:"class,omitempty"` // cell type; "" = unclassified
	Score float64   `json:"score"`           // detector confidence; NOT omitempty (0 is a valid low score)
}

// Keyframe is one keyframe of a track: geometry + per-frame state. Only
// keyframes are stored; intermediate frames are linearly interpolated on ts_ms
// by the shared interpolation contract (see service.InterpolateAt + the
// testdata/interpolation golden fixtures). Geometry is in the rotation-applied
// original pixel space.
type Keyframe struct {
	Frame  int       `json:"frame"`
	TsMs   float64   `json:"ts_ms"`
	Bbox   []float64 `json:"bbox,omitempty"`   // [x,y,w,h]
	Points []float64 `json:"points,omitempty"` // polygon/polyline/keypoints (flat)
	// RLE is the bbox-local dense mask (voxel_mask tracks, Phase C). Keyframes
	// carrying RLE never interpolate — the shared contract holds the previous
	// keyframe wholesale (bbox included: the RLE is anchored to its own bbox).
	RLE *MaskRLE `json:"rle,omitempty"`
	// Instances holds many cells for a TrackKindCells keyframe (C2). For cells the
	// top-level Bbox/Points/RLE are empty — geometry lives here. Like RLE, an
	// instances-bearing keyframe never interpolates (holds wholesale); the shared
	// interpolation contract must guard on it or lerp would silently drop cells.
	Instances []CellInstance `json:"instances,omitempty"`
	Outside   bool           `json:"outside"`
	Occluded  bool           `json:"occluded"`
	Source    string         `json:"source,omitempty"` // human | ai (per-keyframe origin)
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

	Source      string  `json:"source"`                 // ai | human
	AdoptedFrom *string `json:"adopted_from,omitempty"` // human track adopted from this archived AI track's _id

	// Per-track review verdict (B3.1). Empty = not yet judged. A task cannot pass
	// QA while any active track is still rejected.
	ReviewStatus string     `json:"review_status,omitempty"` // "" | passed | rejected
	ReviewNote   string     `json:"review_note,omitempty"`
	ReviewedBy   *uint      `json:"reviewed_by,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`

	Version   int       `json:"version"`   // optimistic-lock version
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
