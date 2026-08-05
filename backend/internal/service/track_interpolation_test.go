package service

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// interpFixture mirrors the shared golden fixtures under repo-root
// testdata/interpolation/. frontend/src/lib/trackInterpolation.test.ts
// (vitest, run in CI) consumes the SAME files, so a change to the interpolation
// contract is locked by both ends.
type interpFixture struct {
	Name string `json:"name"`
	// GeometryKinds declares which geometry kinds this fixture locks. The
	// meta-test (TestEveryTrackKindHasFixture) enforces that every kind in
	// paymodel.AllTrackKinds is declared by at least one fixture.
	GeometryKinds []string        `json:"geometry_kinds"`
	Keyframes     []TrackKeyframe `json:"keyframes"`
	Queries       []struct {
		Frame   int       `json:"frame"`
		TsMs    float64   `json:"ts_ms"`
		Present bool      `json:"present"`
		Bbox    []float64 `json:"bbox"`
		// Points covers polygon / mask tracks (SAM2 propagation writes these).
		// Without it the polygon fixtures would parse fine and assert nothing.
		Points []float64 `json:"points"`
		// RLE covers voxel_mask tracks (Phase C): dense masks hold, never lerp.
		// Same lesson — an unasserted field is an unlocked field.
		RLE *paymodel.MaskRLE `json:"rle"`
		// Instances covers cells tracks (病理 C2): a keyframe's whole cell set holds,
		// never lerps. Without asserting it, a cells fixture would parse and lock
		// nothing (same unasserted-field-is-unlocked lesson).
		Instances []paymodel.CellInstance `json:"instances"`
		Occluded  *bool                   `json:"occluded"`
	} `json:"queries"`
}

func TestInterpolationGoldenFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "testdata", "interpolation")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no interpolation fixtures found in %s", dir)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var fx interpFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		name := fx.Name
		if name == "" {
			name = filepath.Base(f)
		}
		t.Run(name, func(t *testing.T) {
			for i, q := range fx.Queries {
				g, ok := InterpolateAt(fx.Keyframes, q.TsMs)
				if ok != q.Present {
					t.Errorf("query[%d] frame=%d ts=%.1f: present=%v want %v", i, q.Frame, q.TsMs, ok, q.Present)
					continue
				}
				if !q.Present {
					continue
				}
				if q.Bbox != nil && !almostEqualSlice(g.Bbox, q.Bbox) {
					t.Errorf("query[%d] frame=%d ts=%.1f: bbox=%v want %v", i, q.Frame, q.TsMs, g.Bbox, q.Bbox)
				}
				if q.Points != nil && !almostEqualSlice(g.Points, q.Points) {
					t.Errorf("query[%d] frame=%d ts=%.1f: points=%v want %v", i, q.Frame, q.TsMs, g.Points, q.Points)
				}
				if q.RLE != nil {
					if g.RLE == nil || g.RLE.Size != q.RLE.Size || g.RLE.Counts != q.RLE.Counts {
						t.Errorf("query[%d] frame=%d ts=%.1f: rle=%+v want %+v", i, q.Frame, q.TsMs, g.RLE, q.RLE)
					}
				}
				if q.Instances != nil && !reflect.DeepEqual(g.Instances, q.Instances) {
					t.Errorf("query[%d] frame=%d ts=%.1f: instances=%+v want %+v", i, q.Frame, q.TsMs, g.Instances, q.Instances)
				}
				if q.Bbox == nil && q.Points == nil && q.RLE == nil && q.Instances == nil {
					t.Errorf("query[%d] frame=%d: present=true 却没给 bbox/points/rle/instances 任何一个，等于什么都没断言", i, q.Frame)
				}
				if q.Occluded != nil && g.Occluded != *q.Occluded {
					t.Errorf("query[%d] frame=%d ts=%.1f: occluded=%v want %v", i, q.Frame, q.TsMs, g.Occluded, *q.Occluded)
				}
			}
		})
	}
}

// TestEveryTrackKindHasFixture is the machine-enforced version of "new kind must
// come with a golden fixture" (00《稠密几何存储契约》规则 5 · 机器强制). It
// enumerates paymodel.AllTrackKinds (the code's source of truth) and asserts
// each is declared by at least one fixture's "geometry_kinds". Without this, a
// new geometry kind can silently ship with no interpolation coverage on either
// end — the "假注释" lesson's fix is: don't rely on anyone remembering.
//
// Mutation check: add a fake kind to paymodel.AllTrackKinds with no fixture →
// this test must go red.
func TestEveryTrackKindHasFixture(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "testdata", "interpolation")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no interpolation fixtures found in %s (err=%v)", dir, err)
	}
	covered := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var fx interpFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, k := range fx.GeometryKinds {
			covered[k] = true
		}
	}
	for _, kind := range paymodel.AllTrackKinds {
		if !covered[kind] {
			t.Errorf("geometry kind %q has NO golden fixture — add one to testdata/interpolation/ declaring it in geometry_kinds, or that kind is silently unlocked on both Go and TS", kind)
		}
	}
}

func TestInterpolateAt_EmptyAndDegenerate(t *testing.T) {
	if _, ok := InterpolateAt(nil, 0); ok {
		t.Errorf("empty keyframes should not be present")
	}
	if _, ok := InterpolateAt([]TrackKeyframe{}, 100); ok {
		t.Errorf("empty keyframes should not be present")
	}
}

func TestExpand_SkipsGaps(t *testing.T) {
	kfs := []TrackKeyframe{
		{Frame: 0, TsMs: 0, Bbox: []float64{0, 0, 10, 10}},
		{Frame: 10, TsMs: 1000, Bbox: []float64{100, 0, 10, 10}},
	}
	frames := []FrameRef{
		{Frame: -1, TsMs: -100}, // before span → skipped
		{Frame: 0, TsMs: 0},
		{Frame: 5, TsMs: 500},
		{Frame: 10, TsMs: 1000},
		{Frame: 11, TsMs: 1100}, // after span → skipped
	}
	got := Expand(kfs, frames)
	if len(got) != 3 {
		t.Fatalf("expected 3 produced frames, got %d (%+v)", len(got), got)
	}
	if got[1].Frame != 5 || !almostEqualSlice(got[1].Bbox, []float64{50, 0, 10, 10}) {
		t.Errorf("mid frame wrong: %+v", got[1])
	}
}

func almostEqualSlice(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-6 {
			return false
		}
	}
	return true
}
