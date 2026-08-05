package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// newTestRouter returns a RouterService wired with default thresholds and no
// storage / capability dependencies. This is enough to exercise Decide() and
// featuresFromAsset().
func newTestRouter(grayDefault string) *RouterService {
	d := DefaultRoutingDefaults()
	if grayDefault != "" {
		d.GrayDefaultStrategy = grayDefault
	}
	return NewRouterService(nil, nil, d)
}

func TestRouter_Decide_DecisionTable(t *testing.T) {
	r := newTestRouter("")

	cases := []struct {
		name    string
		feats   L1Features
		want    string
		mention string // substring expected in reasons
	}{
		{
			name:    "decode anomaly forces HUMAN_ONLY",
			feats:   L1Features{IsDecodeAnomaly: true, BoxCount: 50, TextAreaRatio: 0.9},
			want:    dbmodel.RouteHumanOnly,
			mention: "decode",
		},
		{
			name:    "low quality forces HUMAN_ONLY",
			feats:   L1Features{IsLowQuality: true, BoxCount: 50, TextAreaRatio: 0.9},
			want:    dbmodel.RouteHumanOnly,
			mention: "low quality",
		},
		{
			name:    "many boxes + dense text → OCR_FIRST",
			feats:   L1Features{BoxCount: 20, TextAreaRatio: 0.4},
			want:    dbmodel.RouteOCRFirst,
			mention: "box_count",
		},
		{
			name:    "long image with sufficient boxes → OCR_FIRST",
			feats:   L1Features{IsLongImage: true, BoxCount: 25, TextAreaRatio: 0.05},
			want:    dbmodel.RouteOCRFirst,
			mention: "long image",
		},
		{
			name:    "few boxes + sparse text → VLM_FIRST",
			feats:   L1Features{BoxCount: 1, TextAreaRatio: 0.01},
			want:    dbmodel.RouteVLMFirst,
			mention: "few text boxes",
		},
		{
			// v0.5: with BC_ocr=5/TAR_t=0.10, (5, 0.10) now hits primary OCR
			// instead of gray. Pick (3, 0.08) — too few boxes for primary OCR,
			// too many for primary VLM. Falls to gray default (now VLM_FIRST).
			name:    "ambiguous gray zone → default VLM_FIRST (v0.5 tuned)",
			feats:   L1Features{BoxCount: 3, TextAreaRatio: 0.08},
			want:    dbmodel.RouteVLMFirst,
			mention: "gray zone",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			strategy, reasons := r.Decide(tc.feats)
			if strategy != tc.want {
				t.Fatalf("strategy = %s, want %s; reasons=%v", strategy, tc.want, reasons)
			}
			joined := strings.Join(reasons, "|")
			if tc.mention != "" && !strings.Contains(joined, tc.mention) {
				t.Fatalf("reasons %v should mention %q", reasons, tc.mention)
			}
		})
	}
}

func TestRouter_Decide_GrayZoneCustomDefault(t *testing.T) {
	r := newTestRouter(dbmodel.RouteHumanOnly)
	// v0.5: pick features that genuinely fall into the gray zone with the
	// tuned BC_ocr=5/TAR_t=0.10 primary OCR rule (BC=3, TAR=0.08).
	strategy, _ := r.Decide(L1Features{BoxCount: 3, TextAreaRatio: 0.08})
	if strategy != dbmodel.RouteHumanOnly {
		t.Fatalf("expected gray fallback HUMAN_ONLY, got %s", strategy)
	}
}

func TestRouter_FeaturesFromAsset(t *testing.T) {
	r := newTestRouter("")

	t.Run("nil asset → zero features", func(t *testing.T) {
		f := r.featuresFromAsset(context.Background(),nil)
		if f.AspectRatio != 0 || f.IsLongImage || f.IsLowQuality {
			t.Fatalf("expected zero features, got %+v", f)
		}
	})

	t.Run("regular landscape image", func(t *testing.T) {
		a := &dbmodel.Asset{Width: 800, Height: 600, QCStatus: dbmodel.QCStatusPassed}
		f := r.featuresFromAsset(context.Background(),a)
		if f.AspectRatio == 0 || f.IsLongImage {
			t.Fatalf("expected aspect>0 long=false, got %+v", f)
		}
		if f.IsLowQuality {
			t.Fatalf("passed QC must not be low quality")
		}
	})

	t.Run("portrait long image", func(t *testing.T) {
		a := &dbmodel.Asset{Width: 200, Height: 2000, QCStatus: dbmodel.QCStatusPassed}
		f := r.featuresFromAsset(context.Background(),a)
		if !f.IsLongImage {
			t.Fatalf("portrait 200x2000 should be IsLongImage")
		}
		if f.AspectRatio < 8 {
			t.Fatalf("aspect ratio should be >= 8, got %v", f.AspectRatio)
		}
	})

	t.Run("failed QC marks low quality", func(t *testing.T) {
		a := &dbmodel.Asset{Width: 100, Height: 100, QCStatus: dbmodel.QCStatusFailed}
		f := r.featuresFromAsset(context.Background(),a)
		if !f.IsLowQuality {
			t.Fatalf("QC failed should imply IsLowQuality")
		}
	})
}

func TestRouter_NilDefaultsFallsBackToBaseline(t *testing.T) {
	// Passing an empty RoutingDefaults should hit the GrayDefaultStrategy=="" branch
	// and substitute the v0.3 baseline.
	r := NewRouterService(nil, nil, RoutingDefaults{})
	if r.defaults.BoxCountOCRThreshold != DefaultRoutingDefaults().BoxCountOCRThreshold {
		t.Fatalf("expected baseline thresholds when defaults left zero; got %+v", r.defaults)
	}
}

// fakeProbe lets tests inject deterministic box_count / text_area_ratio
// values without an HTTP server.
type fakeProbe struct {
	boxCount int
	ratio    float64
	err      error
	called   int
}

func (p *fakeProbe) Probe(ctx context.Context, asset *dbmodel.Asset) (int, float64, error) {
	p.called++
	if p.err != nil {
		return 0, 0, p.err
	}
	return p.boxCount, p.ratio, nil
}

func TestRouter_FeaturesFromAsset_WithProbe(t *testing.T) {
	asset := &dbmodel.Asset{ID: 7, Width: 1000, Height: 800, QCStatus: dbmodel.QCStatusPassed, StorageURI: "obj://x"}

	t.Run("probe success populates box_count and text_area_ratio", func(t *testing.T) {
		r := newTestRouter("")
		probe := &fakeProbe{boxCount: 17, ratio: 0.42}
		r.WithOCRDetProbe(probe)
		f := r.featuresFromAsset(context.Background(), asset)
		if f.BoxCount != 17 || f.TextAreaRatio != 0.42 {
			t.Fatalf("probe result not propagated: %+v", f)
		}
		if probe.called != 1 {
			t.Fatalf("probe should be called exactly once, got %d", probe.called)
		}
	})

	t.Run("probe error falls back to zero features without aborting routing", func(t *testing.T) {
		r := newTestRouter("")
		probe := &fakeProbe{err: errors.New("ocr-server down")}
		r.WithOCRDetProbe(probe)
		f := r.featuresFromAsset(context.Background(), asset)
		if f.BoxCount != 0 || f.TextAreaRatio != 0 {
			t.Fatalf("probe error should leave both fields zero, got %+v", f)
		}
		if !f.IsLongImage && f.AspectRatio == 0 {
			t.Fatalf("baseline metadata features should still be computed: %+v", f)
		}
	})

	t.Run("probe is skipped when QC failed", func(t *testing.T) {
		r := newTestRouter("")
		probe := &fakeProbe{boxCount: 99}
		r.WithOCRDetProbe(probe)
		failed := *asset
		failed.QCStatus = dbmodel.QCStatusFailed
		f := r.featuresFromAsset(context.Background(), &failed)
		if probe.called != 0 {
			t.Fatalf("probe should not be called on QC-failed asset")
		}
		if !f.IsLowQuality {
			t.Fatalf("expected IsLowQuality on QC failed asset")
		}
	})

	t.Run("probe is skipped when no probe is wired", func(t *testing.T) {
		r := newTestRouter("")
		f := r.featuresFromAsset(context.Background(), asset)
		if f.BoxCount != 0 || f.TextAreaRatio != 0 {
			t.Fatalf("no probe → zero box_count/ratio, got %+v", f)
		}
	})
}
