package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"text-annotation-platform/internal/cache"
	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// grayZoneReason is the sentinel reason string placed in RoutingResult.Reasons
// when L1 falls through to the gray-zone default. Route() checks this to decide
// whether to invoke the L2 SigLIP2 probe.
const grayZoneReason = "gray zone fallback"

// RoutingDefaults captures the configurable thresholds for the L1 rule layer
// (plan_v1/01 §3.2). All defaults are conservative; they should be tuned
// against the 04 baseline before P0 release.
type RoutingDefaults struct {
	BoxCountOCRThreshold   int     // box_count >= N triggers OCR
	TextAreaRatioThreshold float64 // text_area_ratio >= R triggers OCR
	BoxCountVLMUpperBound  int     // box_count <= N favours VLM
	TextAreaRatioLow       float64 // text_area_ratio < R favours VLM
	LongImageBoxFloor      int     // long-image OCR floor
	GrayDefaultStrategy    string  // OCR_FIRST or HUMAN_ONLY for GRAY samples in P0
}

// DefaultRoutingDefaults returns the v0.5 tuned baseline.
//
// Tuning method: 300-image baseline (validation_set/baseline_report_300.jsonl)
// run on 2026-05-20, with `tools/tune_router_thresholds.py` grid-searching
// 384 combos and reporting the top by expected-vs-actual mismatch count.
// See `validation_set/router_tuning_report.json` for the full search.
//
// Changes from v0.3:
//   - BoxCountOCRThreshold:   8 → 5    (primary OCR catches more docs)
//   - TextAreaRatioThreshold: 0.15 → 0.10 (same)
//   - GrayDefaultStrategy:    OCR_FIRST → VLM_FIRST  (gray falls to VLM, the
//                                                     dominant misroute on
//                                                     street scenes)
//
// Effect on 300-image baseline: 50 mismatches → 2 (99.3% accuracy).
// Both residual mismatches are MANUAL pages with box>5 but ratio<0.10
// (figure-heavy with little text). Those cases need either L2 (SigLIP-2)
// or operator ad-hoc invoke (POST /tasks/:id/invoke).
func DefaultRoutingDefaults() RoutingDefaults {
	return RoutingDefaults{
		BoxCountOCRThreshold:   5,
		TextAreaRatioThreshold: 0.10,
		BoxCountVLMUpperBound:  2,
		TextAreaRatioLow:       0.05,
		LongImageBoxFloor:      20,
		GrayDefaultStrategy:    dbmodel.RouteVLMFirst,
	}
}

// RouterService encapsulates the L1 rule routing and feature extraction
// pipeline. L1 rules run first; when they fall into the gray zone and a
// SigLIPProbe is wired, L2 zero-shot classification resolves ambiguous images.
type RouterService struct {
	payloadRepo *repository.DB
	cap       *CapabilityService
	defaults  RoutingDefaults
	probe     OCRDetProbe
	siglip    SigLIPProbe
	cache     *cache.Cache // nil = no Redis
}

// WithCache injects the Redis cache; call from main.go after construction.
func (r *RouterService) WithCache(c *cache.Cache) *RouterService {
	r.cache = c
	return r
}

// NewRouterService composes the dependencies for the router. The capability
// service is used (P1) to invoke the OCR detection adapter for L1 features;
// in P0 the worker may pass pre-computed features via Asset.Features and
// bypass the detection call.
func NewRouterService(payloadRepo *repository.DB, cap *CapabilityService, defaults RoutingDefaults) *RouterService {
	if defaults.GrayDefaultStrategy == "" {
		defaults = DefaultRoutingDefaults()
	}
	return &RouterService{payloadRepo: payloadRepo, cap: cap, defaults: defaults}
}

// WithOCRDetProbe enables the Phase 1.5 L1 OCR detection probe. When set,
// featuresFromAsset will call the probe to populate box_count and
// text_area_ratio; without it, those fields stay at zero and the router
// effectively routes everything to VLM_FIRST (the known gap from plan_v1/06
// §11.0). Returns the same router for chained wiring.
func (r *RouterService) WithOCRDetProbe(probe OCRDetProbe) *RouterService {
	r.probe = probe
	return r
}

// WithSigLIPProbe enables the L2 SigLIP2 zero-shot semantic probe. When set,
// the router calls it for images that fall into the L1 gray zone to resolve
// ambiguous cases before applying the default fallback strategy. Returns the
// same router for chained wiring.
func (r *RouterService) WithSigLIPProbe(probe SigLIPProbe) *RouterService {
	r.siglip = probe
	return r
}

// L1Features is the strict-typed feature bag consumed by the L1 rule decision
// table. Callers may build it from Asset.Features (set during QC) or from a
// real OCR detection call.
type L1Features struct {
	BoxCount        int     `json:"box_count"`
	TextAreaRatio   float64 `json:"text_area_ratio"`
	AvgBoxHeight    float64 `json:"avg_box_height"`
	AspectRatio     float64 `json:"aspect_ratio"`
	IsLongImage     bool    `json:"is_long_image"`
	IsDecodeAnomaly bool    `json:"is_decode_anomaly"`
	NeedsRotation   bool    `json:"needs_rotation"`
	IsLowQuality    bool    `json:"is_low_quality"`
}

// Decide runs the L1 decision table and returns the recommended strategy
// (plan_v1/01 §3.2 default decision table).
func (r *RouterService) Decide(features L1Features) (strategy string, reasons []string) {
	switch {
	case features.IsDecodeAnomaly:
		return dbmodel.RouteHumanOnly, []string{"decode anomaly"}
	case features.IsLowQuality:
		return dbmodel.RouteHumanOnly, []string{"low quality"}
	case features.BoxCount >= r.defaults.BoxCountOCRThreshold && features.TextAreaRatio >= r.defaults.TextAreaRatioThreshold:
		return dbmodel.RouteOCRFirst, []string{"box_count >= threshold and text_area_ratio >= threshold"}
	case features.IsLongImage && features.BoxCount >= r.defaults.LongImageBoxFloor:
		return dbmodel.RouteOCRFirst, []string{"long image with sufficient text boxes"}
	case features.BoxCount <= r.defaults.BoxCountVLMUpperBound && features.TextAreaRatio < r.defaults.TextAreaRatioLow:
		return dbmodel.RouteVLMFirst, []string{"few text boxes and low text area ratio"}
	}
	// GRAY zone — falls back to a configurable default. When L2 (SigLIP 2) is
	// wired it will attempt to resolve this from Route().
	return r.defaults.GrayDefaultStrategy, []string{grayZoneReason}
}

// Route runs the full routing pipeline for a single task. L1 rules run first;
// when they land in the gray zone and a SigLIPProbe is wired, L2 zero-shot
// classification is attempted to resolve the ambiguity. The resulting
// RoutingResult is persisted immutably (ai_results, kind=routing).
func (r *RouterService) Route(ctx context.Context, task *dbmodel.AnnotationTask, asset *dbmodel.Asset, features *L1Features) (*paymodel.RoutingResult, error) {
	if features == nil {
		f := r.featuresFromAsset(ctx, asset)
		features = &f
	}
	strategy, reasons := r.Decide(*features)

	featuresBag := map[string]interface{}{
		"box_count":         features.BoxCount,
		"text_area_ratio":   features.TextAreaRatio,
		"avg_box_height":    features.AvgBoxHeight,
		"aspect_ratio":      features.AspectRatio,
		"is_long_image":     features.IsLongImage,
		"is_decode_anomaly": features.IsDecodeAnomaly,
		"needs_rotation":    features.NeedsRotation,
		"is_low_quality":    features.IsLowQuality,
	}

	// L2: when L1 fell into the gray zone and a SigLIPProbe is available,
	// attempt zero-shot semantic classification to resolve the ambiguity.
	// We check the reason rather than the strategy so that an explicit L1
	// VLM_FIRST decision (box_count <= threshold) is never overridden even
	// when GrayDefaultStrategy happens to equal VLM_FIRST.
	isGrayZone := len(reasons) > 0 && reasons[len(reasons)-1] == grayZoneReason
	if isGrayZone && r.siglip != nil {
		topCat, scores, err := r.siglip.Probe(ctx, asset)
		if err != nil {
			slog.Warn("router: siglip2 probe failed, keeping L1 gray default", "asset_id", asset.ID, "error", err)
		} else {
			topScore := scores[topCat]
			featuresBag["l2_top_category"] = topCat
			featuresBag["l2_top_score"] = topScore
			featuresBag["l2_scores"] = scores
			if l2strategy := SigLIP2StrategyFromCategory(topCat, topScore); l2strategy != "" {
				strategy = l2strategy
				reasons = append(reasons, fmt.Sprintf("l2_siglip2: %s (score=%.2f)", topCat, topScore))
			} else {
				reasons = append(reasons, fmt.Sprintf("l2_siglip2 inconclusive: %s (score=%.2f)", topCat, topScore))
			}
		}
	}

	rr := &paymodel.RoutingResult{
		AssetID:         asset.ID,
		TaskID:          task.ID,
		TraceID:         task.TraceID,
		Version:         task.Version,
		NeedOCR:         strategy == dbmodel.RouteOCRFirst,
		NeedCaption:     strategy == dbmodel.RouteVLMFirst,
		OCRPriority:     boolWeight(strategy == dbmodel.RouteOCRFirst),
		CaptionPriority: boolWeight(strategy == dbmodel.RouteVLMFirst),
		Strategy:        strategy,
		Reasons:         reasons,
		Features:        featuresBag,
		CreatedAt:       time.Now(),
	}
	if r.payloadRepo != nil {
		if err := r.payloadRepo.InsertRoutingResult(ctx, rr); err != nil {
			return nil, fmt.Errorf("persist routing result: %w", err)
		}
		if r.cache != nil {
			r.cache.SetJSON(ctx, fmt.Sprintf("routing:latest:%d", rr.TaskID), rr, aiResultTTL)
		}
	}
	return rr, nil
}

// featuresFromAsset derives L1 features from asset metadata and, if an
// OCRDetProbe is wired (Phase 1.5), augments box_count + text_area_ratio
// from a real cheap OCR-detection call. Probe failure is logged but does
// not block routing — the worker still gets a decision based on whatever
// features were derivable.
func (r *RouterService) featuresFromAsset(ctx context.Context, asset *dbmodel.Asset) L1Features {
	f := L1Features{}
	if asset == nil || asset.Width == 0 || asset.Height == 0 {
		return f
	}
	if asset.Width >= asset.Height {
		f.AspectRatio = float64(asset.Width) / float64(asset.Height)
	} else {
		f.AspectRatio = float64(asset.Height) / float64(asset.Width)
	}
	if f.AspectRatio >= 8 {
		f.IsLongImage = true
	}
	if asset.QCStatus == dbmodel.QCStatusFailed {
		f.IsLowQuality = true
	}
	if r.probe != nil && asset.QCStatus == dbmodel.QCStatusPassed {
		bc, ratio, err := r.probe.Probe(ctx, asset)
		if err != nil {
			slog.Warn("router: OCR det probe failed, falling back to zero features", "asset_id", asset.ID, "error", err)
		} else {
			f.BoxCount = bc
			f.TextAreaRatio = ratio
		}
	}
	return f
}

func boolWeight(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
