package service

import (
	"math"
	"sort"

	paymodel "text-annotation-platform/internal/model/payload"
)

// geomEpsilon: coordinates are float64 pixels that survive a JSON round-trip, so
// exact equality would report phantom edits. Half a pixel is far below what an
// annotator can express by dragging.
const geomEpsilon = 0.5

// KeyframeDelta lists the frame numbers touched between two rounds of one track.
type KeyframeDelta struct {
	Added   []int `json:"added"`   // frames that gained a keyframe
	Removed []int `json:"removed"` // frames whose keyframe was deleted
	Moved   []int `json:"moved"`   // frames kept, but geometry/flags changed
}

func (d KeyframeDelta) empty() bool { return len(d.Added)+len(d.Removed)+len(d.Moved) == 0 }

// TrackChange is one track that exists in both rounds but differs.
type TrackChange struct {
	TrackID   int           `json:"track_id"`
	Label     string        `json:"label"` // label in the newer round (for display)
	Fields    []string      `json:"fields"`
	Keyframes KeyframeDelta `json:"keyframes"`
	// FirstFrame is where the reviewer should jump to see the change.
	FirstFrame *int `json:"first_frame,omitempty"`
}

// TrackDiff answers "what did the annotator actually change this round?".
type TrackDiff struct {
	FromRound int           `json:"from_round"`
	ToRound   int           `json:"to_round"`
	Added     []int         `json:"added"`   // track_ids only in the newer round
	Removed   []int         `json:"removed"` // track_ids only in the older round
	Changed   []TrackChange `json:"changed"`
}

// Empty reports whether the two rounds are identical.
func (d TrackDiff) Empty() bool { return len(d.Added)+len(d.Removed)+len(d.Changed) == 0 }

func sameGeom(a, b paymodel.Keyframe) bool {
	if !floatsEqual(a.Bbox, b.Bbox) || !floatsEqual(a.Points, b.Points) {
		return false
	}
	return a.Outside == b.Outside && a.Occluded == b.Occluded
}

func floatsEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > geomEpsilon {
			return false
		}
	}
	return true
}

func keyframesByFrame(kfs []paymodel.Keyframe) map[int]paymodel.Keyframe {
	m := make(map[int]paymodel.Keyframe, len(kfs))
	for _, k := range kfs {
		m[k.Frame] = k
	}
	return m
}

func diffKeyframes(prev, cur []paymodel.Keyframe) KeyframeDelta {
	p, c := keyframesByFrame(prev), keyframesByFrame(cur)
	d := KeyframeDelta{Added: []int{}, Removed: []int{}, Moved: []int{}}
	for f, ck := range c {
		pk, ok := p[f]
		if !ok {
			d.Added = append(d.Added, f)
		} else if !sameGeom(pk, ck) {
			d.Moved = append(d.Moved, f)
		}
	}
	for f := range p {
		if _, ok := c[f]; !ok {
			d.Removed = append(d.Removed, f)
		}
	}
	sort.Ints(d.Added)
	sort.Ints(d.Removed)
	sort.Ints(d.Moved)
	return d
}

// changedFields lists the invariant track-level properties that differ.
func changedFields(prev, cur paymodel.Track) []string {
	var out []string
	if prev.Label != cur.Label {
		out = append(out, "label")
	}
	if prev.Color != cur.Color {
		out = append(out, "color")
	}
	if prev.Kind != cur.Kind {
		out = append(out, "kind")
	}
	if !attrsEqual(prev.Attrs, cur.Attrs) {
		out = append(out, "attrs")
	}
	return out
}

func attrsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !sameScalar(av, bv) {
			return false
		}
	}
	return true
}

// sameScalar compares attr values. They arrive from decoded JSON as
// float64/string/bool, so a numeric 1 and 1.0 must compare equal.
func sameScalar(a, b interface{}) bool {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		return math.Abs(af-bf) < 1e-9
	}
	return a == b
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// firstTouchedFrame is the earliest frame the reviewer should look at.
func firstTouchedFrame(d KeyframeDelta) *int {
	best := math.MaxInt
	for _, set := range [][]int{d.Added, d.Removed, d.Moved} {
		if len(set) > 0 && set[0] < best {
			best = set[0]
		}
	}
	if best == math.MaxInt {
		return nil
	}
	return &best
}

// activeByTrackID indexes a round's tracks. Archived tracks (adopt/delete leave
// them behind) are not part of what the annotator submitted.
func activeByTrackID(tracks []paymodel.Track) map[int]paymodel.Track {
	m := make(map[int]paymodel.Track, len(tracks))
	for _, t := range tracks {
		if t.IsActive {
			m[t.TrackID] = t
		}
	}
	return m
}

// DiffTrackRounds compares two submission rounds. Pure — the whole point of
// capturing rounds is that this needs no database.
func DiffTrackRounds(prev, cur []paymodel.Track, fromRound, toRound int) TrackDiff {
	p, c := activeByTrackID(prev), activeByTrackID(cur)
	// Non-nil slices: these cross the wire as JSON, and `null` would blow up the
	// panel's .map() where `[]` renders as "no changes".
	d := TrackDiff{
		FromRound: fromRound, ToRound: toRound,
		Added: []int{}, Removed: []int{}, Changed: []TrackChange{},
	}

	for id, ct := range c {
		pt, ok := p[id]
		if !ok {
			d.Added = append(d.Added, id)
			continue
		}
		fields := changedFields(pt, ct)
		kf := diffKeyframes(pt.Keyframes, ct.Keyframes)
		if len(fields) == 0 && kf.empty() {
			continue
		}
		d.Changed = append(d.Changed, TrackChange{
			TrackID: id, Label: ct.Label, Fields: fields,
			Keyframes: kf, FirstFrame: firstTouchedFrame(kf),
		})
	}
	for id := range p {
		if _, ok := c[id]; !ok {
			d.Removed = append(d.Removed, id)
		}
	}

	// Map iteration is random; a diff the reviewer reads must be stable.
	sort.Ints(d.Added)
	sort.Ints(d.Removed)
	sort.Slice(d.Changed, func(i, j int) bool { return d.Changed[i].TrackID < d.Changed[j].TrackID })
	return d
}
