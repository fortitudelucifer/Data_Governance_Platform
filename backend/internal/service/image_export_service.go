package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/png"
	"sort"
	"time"

	paymodel "text-annotation-platform/internal/model/payload"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository"
)

// ImageExportService converts a dataset's FinalAnnotations into training
// formats: COCO (segmentation), YOLOv8-seg (per-image txt), and W3C Web
// Annotation JSON-LD. It reads FinalAnnotation.Shapes (kind=bbox or polygon)
// and joins relational assets for image dimensions / filenames.
//
// Per plan §7 #5 this is deliberately separate from V1 text ExportService —
// the schemas don't overlap.
type ImageExportService struct {
	db *repository.DB
	payload *repository.DB
}

// NewImageExportService composes the dependencies.
func NewImageExportService(dbRepo *repository.DB, payloadRepo *repository.DB) *ImageExportService {
	return &ImageExportService{db: dbRepo, payload: payloadRepo}
}

// exportBundle is the gathered, in-memory view used by all three formatters.
type exportBundle struct {
	finals     []paymodel.FinalAnnotation
	assets     map[uint]*dbmodel.Asset
	categories []string        // ordered; index is the 0-based class id
	catIndex   map[string]int  // label -> 0-based index
}

const unlabeledCategory = "unlabeled"

// gather loads finals (optionally since / taskIDs), their assets, and a
// deterministic category index built from shape labels.
func (s *ImageExportService) gather(ctx context.Context, datasetID uint, since *time.Time, taskIDs []uint) (*exportBundle, error) {
	b := &exportBundle{
		assets:   map[uint]*dbmodel.Asset{},
		catIndex: map[string]int{},
	}
	labelSet := map[string]struct{}{}
	_, err := s.payload.StreamFinalAnnotationsByDataset(ctx, datasetID, since, taskIDs, func(fa *paymodel.FinalAnnotation) error {
		b.finals = append(b.finals, *fa)
		if _, ok := b.assets[fa.AssetID]; !ok {
			if a, err := s.db.FindAssetByID(ctx, fa.AssetID); err == nil {
				b.assets[fa.AssetID] = a
			}
		}
		for _, sh := range fa.Shapes {
			labelSet[labelOf(sh)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	cats := make([]string, 0, len(labelSet))
	for l := range labelSet {
		cats = append(cats, l)
	}
	sort.Strings(cats)
	for i, c := range cats {
		b.catIndex[c] = i
	}
	b.categories = cats
	return b, nil
}

func labelOf(sh paymodel.Shape) string {
	if sh.Label == "" {
		return unlabeledCategory
	}
	return sh.Label
}

// shapeBBox returns [x,y,w,h] in absolute pixels for either a 2-point bbox
// shape or a polygon shape's bounding rect.
// For mask shapes, reads attrs.mask_bbox ([x,y,w,h]) if present.
func shapeBBox(sh paymodel.Shape) (x, y, w, h float64) {
	if sh.Kind == "mask" {
		if bb, ok := maskBBoxFromAttrs(sh.Attrs); ok {
			return bb[0], bb[1], bb[2], bb[3]
		}
		// fallback: use points TL/BR
	}
	if len(sh.Points) == 0 {
		return 0, 0, 0, 0
	}
	if (sh.Kind == "bbox" || sh.Kind == "mask") && len(sh.Points) >= 2 {
		x0, y0 := sh.Points[0][0], sh.Points[0][1]
		x1, y1 := sh.Points[1][0], sh.Points[1][1]
		return minf(x0, x1), minf(y0, y1), absf(x1 - x0), absf(y1 - y0)
	}
	// polygon / polyline: bounding rect of all vertices
	minX, minY := sh.Points[0][0], sh.Points[0][1]
	maxX, maxY := minX, minY
	for _, p := range sh.Points {
		if len(p) < 2 {
			continue
		}
		minX, minY = minf(minX, p[0]), minf(minY, p[1])
		maxX, maxY = maxf(maxX, p[0]), maxf(maxY, p[1])
	}
	return minX, minY, maxX - minX, maxY - minY
}

// shapePolygonFlat returns a flattened [x1,y1,x2,y2,...] polygon in absolute
// pixels. A bbox or mask shape becomes its 4 corners (clockwise).
func shapePolygonFlat(sh paymodel.Shape) []float64 {
	if sh.Kind == "bbox" || sh.Kind == "mask" || len(sh.Points) < 3 {
		x, y, w, h := shapeBBox(sh)
		return []float64{x, y, x + w, y, x + w, y + h, x, y + h}
	}
	out := make([]float64, 0, len(sh.Points)*2)
	for _, p := range sh.Points {
		if len(p) >= 2 {
			out = append(out, p[0], p[1])
		}
	}
	return out
}

// maskBBoxFromAttrs extracts [x,y,w,h] from Shape.Attrs["mask_bbox"].
func maskBBoxFromAttrs(attrs map[string]interface{}) ([4]float64, bool) {
	if attrs == nil {
		return [4]float64{}, false
	}
	raw, ok := attrs["mask_bbox"]
	if !ok {
		return [4]float64{}, false
	}
	switch v := raw.(type) {
	case []interface{}:
		if len(v) < 4 {
			return [4]float64{}, false
		}
		var bb [4]float64
		for i := 0; i < 4; i++ {
			switch n := v[i].(type) {
			case float64:
				bb[i] = n
			case int:
				bb[i] = float64(n)
			case int64:
				bb[i] = float64(n)
			default:
				return [4]float64{}, false
			}
		}
		return bb, true
	case []float64:
		if len(v) < 4 {
			return [4]float64{}, false
		}
		return [4]float64{v[0], v[1], v[2], v[3]}, true
	}
	return [4]float64{}, false
}

// maskPngB64FromAttrs extracts the base64-encoded full-image mask PNG from
// Shape.Attrs["mask_png_b64"]. Returns "" if absent or wrong type.
func maskPngB64FromAttrs(attrs map[string]interface{}) string {
	if attrs == nil {
		return ""
	}
	v, ok := attrs["mask_png_b64"]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// maskToCocoRLE decodes a full-image-size base64 PNG mask and returns a COCO
// uncompressed RLE dict {"size":[H,W],"counts":[...]}.
// Column-major traversal (x outer, y inner) matches COCO convention.
// A pixel is "on" (label=1) when its alpha and red channels are both non-zero,
// matching the offscreen-canvas brush paint done in the frontend.
// If imgW/imgH are 0 the image bounds are used.
func maskToCocoRLE(pngB64 string, imgW, imgH int) (map[string]interface{}, error) {
	raw, err := base64.StdEncoding.DecodeString(pngB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode mask: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("png decode mask: %w", err)
	}
	bounds := img.Bounds()
	if imgW <= 0 {
		imgW = bounds.Max.X - bounds.Min.X
	}
	if imgH <= 0 {
		imgH = bounds.Max.Y - bounds.Min.Y
	}

	counts := make([]int, 0, 64)
	cur := 0 // start counting zeros
	count := 0
	for x := 0; x < imgW; x++ {
		for y := 0; y < imgH; y++ {
			r32, _, _, a32 := img.At(x, y).RGBA()
			label := 0
			if a32 > 0 && r32 > 0 {
				label = 1
			}
			if label == cur {
				count++
			} else {
				counts = append(counts, count)
				cur = label
				count = 1
			}
		}
	}
	counts = append(counts, count)

	return map[string]interface{}{
		"size":   []int{imgH, imgW},
		"counts": counts,
	}, nil
}

// maskToPolygonFlat decodes a full-image-size base64 PNG mask and returns a flat
// [x1,y1,...] polygon (absolute pixels) tracing the top and bottom silhouette of
// the painted region. Returns nil if no pixels are on or decoding fails.
// The polygon is suitable for YOLO seg and W3C SVG selectors.
func maskToPolygonFlat(pngB64 string, imgW, imgH int) []float64 {
	raw, err := base64.StdEncoding.DecodeString(pngB64)
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	if imgW <= 0 {
		imgW = bounds.Max.X - bounds.Min.X
	}
	if imgH <= 0 {
		imgH = bounds.Max.Y - bounds.Min.Y
	}

	type colSpan struct{ x, minY, maxY int }
	spans := make([]colSpan, 0)
	for x := 0; x < imgW; x++ {
		minY, maxY := -1, -1
		for y := 0; y < imgH; y++ {
			r32, _, _, a32 := img.At(x, y).RGBA()
			if a32 > 0 && r32 > 0 {
				if minY == -1 {
					minY = y
				}
				maxY = y
			}
		}
		if minY != -1 {
			spans = append(spans, colSpan{x, minY, maxY})
		}
	}
	if len(spans) == 0 {
		return nil
	}

	flat := make([]float64, 0, len(spans)*4)
	for _, s := range spans {
		flat = append(flat, float64(s.x)+0.5, float64(s.minY)+0.5)
	}
	for i := len(spans) - 1; i >= 0; i-- {
		flat = append(flat, float64(spans[i].x)+0.5, float64(spans[i].maxY)+0.5)
	}
	return flat
}

func (b *exportBundle) fileName(assetID uint) string {
	if a, ok := b.assets[assetID]; ok && a.OriginalName != "" {
		return a.OriginalName
	}
	return fmt.Sprintf("asset-%d.jpg", assetID)
}

func (b *exportBundle) dims(assetID uint) (int, int) {
	if a, ok := b.assets[assetID]; ok {
		return a.Width, a.Height
	}
	return 0, 0
}

// ---- COCO -------------------------------------------------------------

// BuildCOCO returns a COCO detection+segmentation dict.
// taskIDs (non-empty) restricts to those tasks only; nil/empty = full dataset.
func (s *ImageExportService) BuildCOCO(ctx context.Context, datasetID uint, since *time.Time, taskIDs []uint) (map[string]interface{}, error) {
	b, err := s.gather(ctx, datasetID, since, taskIDs)
	if err != nil {
		return nil, err
	}
	images := make([]map[string]interface{}, 0, len(b.finals))
	annotations := make([]map[string]interface{}, 0)
	categories := make([]map[string]interface{}, 0, len(b.categories))
	for i, c := range b.categories {
		categories = append(categories, map[string]interface{}{"id": i + 1, "name": c})
	}
	annID := 1
	for imgIdx, fa := range b.finals {
		imgID := imgIdx + 1
		w, h := b.dims(fa.AssetID)
		images = append(images, map[string]interface{}{
			"id": imgID, "file_name": b.fileName(fa.AssetID),
			"width": w, "height": h,
			"asset_id": fa.AssetID, "task_id": fa.TaskID,
		})
		for _, sh := range fa.Shapes {
			bx, by, bw, bh := shapeBBox(sh)

			var segmentation interface{}
			iscrowd := 0
			if sh.Kind == "mask" {
				if pngB64 := maskPngB64FromAttrs(sh.Attrs); pngB64 != "" {
					if rle, err := maskToCocoRLE(pngB64, w, h); err == nil {
						segmentation = rle
						iscrowd = 1
					}
				}
			}
			if segmentation == nil {
				segmentation = [][]float64{shapePolygonFlat(sh)}
			}

			annotations = append(annotations, map[string]interface{}{
				"id": annID, "image_id": imgID,
				"category_id":  b.catIndex[labelOf(sh)] + 1,
				"bbox":         []float64{bx, by, bw, bh},
				"area":         bw * bh,
				"segmentation": segmentation,
				"iscrowd":      iscrowd,
			})
			annID++
		}
	}
	return map[string]interface{}{
		"info": map[string]interface{}{
			"description":  fmt.Sprintf("dataset %d export", datasetID),
			"date_created": time.Now().Format(time.RFC3339),
		},
		"images":      images,
		"annotations": annotations,
		"categories":  categories,
	}, nil
}

// ---- YOLOv8-seg -------------------------------------------------------

// YOLOSegExport carries the per-image label files plus the data.yaml.
type YOLOSegExport struct {
	Files    map[string]string // "labels/<stem>.txt" -> content
	DataYAML string
	Classes  []string
}

// BuildYOLOSeg returns YOLOv8-seg label files (normalized polygons) + data.yaml.
// taskIDs (non-empty) restricts to those tasks only; nil/empty = full dataset.
func (s *ImageExportService) BuildYOLOSeg(ctx context.Context, datasetID uint, since *time.Time, taskIDs []uint) (*YOLOSegExport, error) {
	b, err := s.gather(ctx, datasetID, since, taskIDs)
	if err != nil {
		return nil, err
	}
	out := &YOLOSegExport{Files: map[string]string{}, Classes: b.categories}
	for _, fa := range b.finals {
		w, h := b.dims(fa.AssetID)
		if w <= 0 || h <= 0 {
			continue // cannot normalize without dimensions
		}
		stem := stripExt(b.fileName(fa.AssetID))
		var lines string
		for _, sh := range fa.Shapes {
			var flat []float64
			if sh.Kind == "mask" {
				if pngB64 := maskPngB64FromAttrs(sh.Attrs); pngB64 != "" {
					flat = maskToPolygonFlat(pngB64, w, h)
				}
			}
			if flat == nil {
				flat = shapePolygonFlat(sh)
			}
			if len(flat) < 6 {
				continue
			}
			cls := b.catIndex[labelOf(sh)]
			line := fmt.Sprintf("%d", cls)
			for i := 0; i+1 < len(flat); i += 2 {
				nx := clamp01(flat[i] / float64(w))
				ny := clamp01(flat[i+1] / float64(h))
				line += fmt.Sprintf(" %.6f %.6f", nx, ny)
			}
			lines += line + "\n"
		}
		out.Files["labels/"+stem+".txt"] = lines
	}
	yaml := "# YOLOv8-seg dataset\nnames:\n"
	for i, c := range b.categories {
		yaml += fmt.Sprintf("  %d: %s\n", i, c)
	}
	out.DataYAML = yaml
	return out, nil
}

// ---- W3C JSON-LD ------------------------------------------------------

// BuildJSONLD returns a W3C Web Annotation AnnotationCollection.
// taskIDs (non-empty) restricts to those tasks only; nil/empty = full dataset.
func (s *ImageExportService) BuildJSONLD(ctx context.Context, datasetID uint, since *time.Time, taskIDs []uint) (map[string]interface{}, error) {
	b, err := s.gather(ctx, datasetID, since, taskIDs)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]interface{}, 0)
	for _, fa := range b.finals {
		fileName := b.fileName(fa.AssetID)
		for _, sh := range fa.Shapes {
			bx, by, bw, bh := shapeBBox(sh)
			selector := map[string]interface{}{
				"type":  "FragmentSelector",
				"conformsTo": "http://www.w3.org/TR/media-frags/",
				"value": fmt.Sprintf("xywh=pixel:%g,%g,%g,%g", bx, by, bw, bh),
			}
			if sh.Kind != "bbox" && len(sh.Points) >= 3 {
				selector = map[string]interface{}{
					"type":  "SvgSelector",
					"value": svgPolygon(shapePolygonFlat(sh)),
				}
			}
			items = append(items, map[string]interface{}{
				"type": "Annotation",
				"target": map[string]interface{}{
					"source":   fileName,
					"selector": selector,
				},
				"body": []map[string]interface{}{{
					"type": "TextualBody", "purpose": "tagging",
					"value": labelOf(sh),
				}},
				"generated": fa.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return map[string]interface{}{
		"@context": "http://www.w3.org/ns/anno.jsonld",
		"type":     "AnnotationCollection",
		"label":    fmt.Sprintf("dataset %d", datasetID),
		"total":    len(items),
		"items":    items,
	}, nil
}

// ---- small helpers ----------------------------------------------------

func minf(a, b float64) float64 { if a < b { return a }; return b }
func maxf(a, b float64) float64 { if a > b { return a }; return b }
func absf(a float64) float64    { if a < 0 { return -a }; return a }
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func stripExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
		if name[i] == '/' || name[i] == '\\' {
			break
		}
	}
	return name
}

func svgPolygon(flat []float64) string {
	pts := ""
	for i := 0; i+1 < len(flat); i += 2 {
		if pts != "" {
			pts += " "
		}
		pts += fmt.Sprintf("%g,%g", flat[i], flat[i+1])
	}
	return fmt.Sprintf("<svg><polygon points=\"%s\"/></svg>", pts)
}
