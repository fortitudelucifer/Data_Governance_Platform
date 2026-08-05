package payload

import "time"

// CollTrackRound stores one document per submission round of a video task.
const CollTrackRound = "mm_track_rounds"

// TrackRound is the full active track list captured at the moment a task was
// submitted for review. mm_tracks is overwritten in place (UpdateTrackByVersion),
// so without this there is nothing to diff a rework against: the reviewer would
// have to re-check all 50 tracks to find the 2 the annotator actually touched.
//
// Distinct from mm_track_snapshots, which is written only on QA pass and is the
// export source of truth. Rounds are review scaffolding, not export data.
type TrackRound struct {
	ID     string `json:"id"`
	TaskID uint   `json:"task_id"`
	Round  int    `json:"round"` // 1-based, increments per submit

	Tracks []Track `json:"tracks"`
	// TrackCount is denormalised so the round picker can be served from a
	// projection that excludes the (large) tracks payload.
	TrackCount int `json:"track_count"`

	SubmittedBy uint      `json:"submitted_by"`
	SubmittedAt time.Time `json:"submitted_at"`
}
