package payload

import "time"

// CollReviewComment holds reviewer comments anchored to a point in the asset.
const CollReviewComment = "mm_review_comments"

// Review comment status values.
const (
	ReviewCommentOpen     = "open"
	ReviewCommentResolved = "resolved"
)

// ReviewComment is a reviewer's note pinned to a specific place in the asset,
// so a rejected task tells the annotator *where* the problem is rather than
// just that there is one (执行方案-02 B3.1).
//
// The anchor fields are all optional and interpreted per modality:
//   - video: Frame (+ TrackID when the problem is one object)
//   - audio: TimeMs
//   - image: TrackID doubles as the shape index
//
// A comment with no anchor is a whole-task remark. Resolution is the
// annotator's action — the reviewer files it, the annotator closes it — so a
// task can be re-submitted with every comment accounted for.
type ReviewComment struct {
	ID        string `json:"id"`
	TaskID    uint   `json:"task_id"`
	DatasetID uint   `json:"dataset_id"`
	AssetID   uint   `json:"asset_id"`

	Frame   *int     `json:"frame,omitempty"`
	TrackID *int     `json:"track_id,omitempty"`
	TimeMs  *float64 `json:"time_ms,omitempty"`

	Body string `json:"body"`

	Status     string     `json:"status"` // open | resolved
	ResolvedBy *uint      `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	AuthorID   uint   `json:"author_id"`
	AuthorName string `json:"author_name,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
