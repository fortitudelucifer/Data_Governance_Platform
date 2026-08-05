package service

// track_interpolation.go — the single-source-of-truth keyframe interpolation
// used by every per-frame consumer (export: MOT/COCO; and mirrored by the
// frontend canvas in TS). Both implementations are locked by the shared golden
// fixtures under repo-root testdata/interpolation/ (see 执行方案-02 §插值规范).
//
// Rules (CVAT-aligned):
//   - Interpolate on ts_ms (NOT frame). CFR ⇔ VFR: ts_ms is authoritative.
//   - No extrapolation: before the first / after the last keyframe → not present.
//   - outside:true keyframe starts a gap: it produces nothing from that keyframe
//     until the next outside:false keyframe. No linear transition across an
//     outside boundary — a visible→outside segment HOLDS the visible geometry
//     (constant), the outside keyframe itself is not present.
//   - occluded is passthrough (does not affect geometry).
//   - Dense masks do NOT interpolate (00《稠密几何存储契约》规则 5 / 04 §4.3):
//     a segment touching an RLE-bearing keyframe HOLDS the start keyframe
//     wholesale — bbox included, because the RLE is bbox-local and must stay
//     aligned with its own anchor box.

import paymodel "text-annotation-platform/internal/model/payload"

// TrackKeyframe is one keyframe's geometry + state. Bbox is [x,y,w,h] in the
// rotation-applied original pixel space; Points carries polygon/keypoints when
// the track geometry is not a bbox (interpolated component-wise the same way);
// RLE carries the bbox-local dense mask (voxel_mask, Phase C — never lerped).
type TrackKeyframe struct {
	Frame  int               `json:"frame"`
	TsMs   float64           `json:"ts_ms"`
	Bbox   []float64         `json:"bbox,omitempty"`
	Points []float64         `json:"points,omitempty"`
	RLE    *paymodel.MaskRLE `json:"rle,omitempty"`
	// Instances carries a TrackKindCells keyframe's cells (病理 C2). Like RLE it
	// never lerps — an instances-bearing keyframe HOLDS wholesale. It lives here
	// (not only in the payload model) because the interpolation contract dispatches
	// on geometry PRESENCE, not on kind: cells' RLE is nested inside instances so
	// the top-level RLE is nil, and without an explicit instances guard a
	// multi-keyframe cells track would fall into lerpGeom and silently drop every
	// instance (CLAUDE.md #6 sparse-geometry-dropped, pathology edition).
	Instances []paymodel.CellInstance `json:"instances,omitempty"`
	Outside   bool                    `json:"outside"`
	Occluded  bool                    `json:"occluded"`
}

// InterpolatedGeom is the result of a query at a given ts.
type InterpolatedGeom struct {
	Bbox      []float64
	Points    []float64
	RLE       *paymodel.MaskRLE
	Instances []paymodel.CellInstance
	Occluded  bool
}

// InterpolateAt returns the geometry at tsMs, or present=false when the object
// is not shown at that time (gap / outside / out of range). kfs must be sorted
// ascending by TsMs.
func InterpolateAt(kfs []TrackKeyframe, tsMs float64) (InterpolatedGeom, bool) {
	n := len(kfs)
	if n == 0 {
		return InterpolatedGeom{}, false
	}
	first, last := kfs[0], kfs[n-1]
	// No extrapolation outside the keyframe span.
	if tsMs < first.TsMs || tsMs > last.TsMs {
		return InterpolatedGeom{}, false
	}
	// At or beyond the last keyframe's ts (only == reaches here).
	if tsMs >= last.TsMs {
		if last.Outside {
			return InterpolatedGeom{}, false
		}
		return geomOf(last), true
	}
	// Locate the segment [kfs[i], kfs[i+1]) that contains tsMs.
	for i := 0; i < n-1; i++ {
		lo, hi := kfs[i], kfs[i+1]
		if tsMs < lo.TsMs || tsMs >= hi.TsMs {
			continue
		}
		if lo.Outside {
			// Segment starting at an outside keyframe is a gap.
			return InterpolatedGeom{}, false
		}
		if tsMs == lo.TsMs {
			return geomOf(lo), true
		}
		if hi.Outside {
			// Visible → outside: hold the visible geometry, no transition.
			return geomOf(lo), true
		}
		if lo.RLE != nil || hi.RLE != nil || len(lo.Instances) > 0 || len(hi.Instances) > 0 {
			// Dense masks (voxel_mask.RLE) and cell sets (cells.Instances) do not
			// interpolate: hold the start keyframe wholesale (bbox included — the
			// RLE is anchored to its own bbox; lerping the box while holding the
			// mask would silently misalign them). Instances dispatch is by presence,
			// not kind: without this len(Instances)>0 guard, cells' nil top-level
			// RLE/bbox would fall through to lerpGeom and drop every cell silently.
			return geomOf(lo), true
		}
		t := (tsMs - lo.TsMs) / (hi.TsMs - lo.TsMs)
		return lerpGeom(lo, hi, t), true
	}
	return InterpolatedGeom{}, false
}

// FrameRef identifies a frame to expand (frame number + its ts).
type FrameRef struct {
	Frame int
	TsMs  float64
}

// ExpandedFrame is one produced per-frame geometry.
type ExpandedFrame struct {
	Frame int
	InterpolatedGeom
}

// Expand produces per-frame geometry for the given frames, skipping frames where
// the object is not present. Used by the streaming exporters (MOT/COCO).
func Expand(kfs []TrackKeyframe, frames []FrameRef) []ExpandedFrame {
	out := make([]ExpandedFrame, 0, len(frames))
	for _, fr := range frames {
		if g, ok := InterpolateAt(kfs, fr.TsMs); ok {
			out = append(out, ExpandedFrame{Frame: fr.Frame, InterpolatedGeom: g})
		}
	}
	return out
}

func geomOf(k TrackKeyframe) InterpolatedGeom {
	return InterpolatedGeom{
		Bbox:      cloneF(k.Bbox),
		Points:    cloneF(k.Points),
		RLE:       cloneRLE(k.RLE),
		Instances: cloneInstances(k.Instances),
		Occluded:  k.Occluded,
	}
}

func cloneRLE(r *paymodel.MaskRLE) *paymodel.MaskRLE {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// cloneInstances deep-copies a cells keyframe's instances. geomOf MUST clone
// them — a harness that adds InterpolatedGeom.Instances but forgets to clone
// here returns instances=nil even for a present single keyframe (fixture red, or
// worse, silently unlocked). Each instance's RLE is a pointer → clone it too.
func cloneInstances(in []paymodel.CellInstance) []paymodel.CellInstance {
	if len(in) == 0 {
		return nil
	}
	out := make([]paymodel.CellInstance, len(in))
	for i, c := range in {
		c.Bbox = cloneF(c.Bbox)
		c.RLE = cloneRLE(c.RLE)
		out[i] = c
	}
	return out
}

func lerpGeom(a, b TrackKeyframe, t float64) InterpolatedGeom {
	return InterpolatedGeom{
		Bbox:   lerpSlice(a.Bbox, b.Bbox, t),
		Points: lerpSlice(a.Points, b.Points, t),
		// occluded is taken from the segment's start keyframe (state holds until
		// the next keyframe), matching CVAT.
		Occluded: a.Occluded,
	}
}

// lerpSlice linearly interpolates two equal-length coordinate slices. If lengths
// differ or either is empty, it returns a clone of a (degenerate — the caller's
// data model guarantees matching shapes for a given track).
func lerpSlice(a, b []float64, t float64) []float64 {
	if len(a) == 0 || len(a) != len(b) {
		return cloneF(a)
	}
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] + (b[i]-a[i])*t
	}
	return out
}

func cloneF(s []float64) []float64 {
	if len(s) == 0 {
		return nil
	}
	out := make([]float64, len(s))
	copy(out, s)
	return out
}
