package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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

// ErrSelectedTasksMissing:显式 ?task_ids= 里有任务没有 FINALIZED 快照(仍在 QA /
// 从未定稿 / 属于别的数据集)。导出必须 **fail-closed**(handler 映射 409 并列出缺失),
// 绝不能返回一个"少了几个任务却仍是 200、文件名照写 selected2"的文件 —— 请求方会以为
// 导全了(#3)。
var ErrSelectedTasksMissing = errors.New("selected tasks have no finalized annotation")

// requireAllTasks fails closed when an explicit selection asked for tasks that
// produced no finalized data (#3). requested==nil (no filter) always passes.
func requireAllTasks(requested []uint, found map[uint]bool) error {
	if len(requested) == 0 {
		return nil
	}
	var missing []uint
	for _, id := range requested {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w：任务 %v（无 FINALIZED 快照 / 仍在 QA / 不属于本数据集）", ErrSelectedTasksMissing, missing)
	}
	return nil
}

// gather loads finals (optionally since / taskIDs), their assets, and a
// deterministic category index built from shape labels.
func (s *ImageExportService) gather(ctx context.Context, datasetID uint, since *time.Time, taskIDs []uint) (*exportBundle, error) {
	b := &exportBundle{
		assets:   map[uint]*dbmodel.Asset{},
		catIndex: map[string]int{},
	}
	labelSet := map[string]struct{}{}
	foundTasks := map[uint]bool{}
	_, err := s.payload.StreamFinalAnnotationsByDataset(ctx, datasetID, since, taskIDs, func(fa *paymodel.FinalAnnotation) error {
		foundTasks[fa.TaskID] = true
		b.finals = append(b.finals, *fa)
		if _, ok := b.assets[fa.AssetID]; !ok {
			// #5 资产查询错误 / 尺寸非法 **fail-closed**,绝不吞掉。旧代码 `if err == nil`
			// 直接吞——资产查不到就从 map 里缺席,下游 dims() 返回 0×0:COCO 写 width:0
			// height:0、YOLO 静默跳过整张标签,导出仍是 200。几何一律存"旋转已应用的显示
			// 像素空间"(坐标系纪律),尺寸未回填就无法归一化 → 宁可中止并指名资产,也不
			// 产出坐标全错却"看着正常"的文件。
			a, aerr := s.db.FindAssetByID(ctx, fa.AssetID)
			if aerr != nil {
				return fmt.Errorf("导出中止:资产 %d 查询失败(拒绝产出 0×0 几何/跳标的静默错误): %w", fa.AssetID, aerr)
			}
			if a.Width <= 0 || a.Height <= 0 {
				return fmt.Errorf("导出中止:资产 %d 显示尺寸非法(%d×%d)——旋转后 width/height 未回填?"+
					"几何无法归一化;请修好资产尺寸再导出(而不是导出坐标全错的文件)", fa.AssetID, a.Width, a.Height)
			}
			b.assets[fa.AssetID] = a
		}
		for _, sh := range fa.Shapes {
			labelSet[labelOf(sh)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// #3 显式选择的任务必须都有终稿;since 增量导出会合法地排除一些,故只在非增量时校验。
	if since == nil {
		if merr := requireAllTasks(taskIDs, foundTasks); merr != nil {
			return nil, merr
		}
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
	pw, ph := bounds.Dx(), bounds.Dy()
	if imgW <= 0 {
		imgW = pw
	}
	if imgH <= 0 {
		imgH = ph
	}
	// #10 掩膜 PNG 尺寸必须与资产显示尺寸一致,不一致 **fail-closed**。旧代码直接按
	// imgW×imgH 遍历,img.At 越界读到透明:较小的 PNG 被"透明补齐"、较大的被裁剪 —— 掩膜
	// 悄悄平移/缺角却仍产出(旋转后资产尺寸没对上就是这个症状)。宁可报错也不产出错位掩膜。
	if pw != imgW || ph != imgH {
		return nil, fmt.Errorf("掩膜 PNG 尺寸 %d×%d 与资产显示尺寸 %d×%d 不符(错位的根源，拒绝导出)", pw, ph, imgW, imgH)
	}

	counts := make([]int, 0, 64)
	cur := 0 // start counting zeros
	count := 0
	for x := 0; x < imgW; x++ {
		for y := 0; y < imgH; y++ {
			r32, _, _, a32 := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
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

// rleForegroundArea counts foreground pixels in a COCO uncompressed RLE dict
// (#12). maskToCocoRLE starts counting zeros, so counts alternate bg,fg,bg,fg…
// — foreground is the sum of the odd-indexed runs.
func rleForegroundArea(rle map[string]interface{}) float64 {
	counts, ok := rle["counts"].([]int)
	if !ok {
		return 0
	}
	fg := 0
	for i := 1; i < len(counts); i += 2 {
		fg += counts[i]
	}
	return float64(fg)
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
			area := bw * bh // bbox 兜底面积
			// #12 iscrowd 是显式业务语义(crowd 区域),不是"用了 RLE"。恒 0 —— 设成 1 会
			// 改变 COCO 评估口径(iscrowd=1 走宽松匹配、且被 area 分桶排除),污染 mAP。
			iscrowd := 0
			if sh.Kind == "mask" {
				if pngB64 := maskPngB64FromAttrs(sh.Attrs); pngB64 != "" {
					// #10 掩膜声明了 PNG 就必须解得出:解不出(base64 坏 / 尺寸不符)一律
					// **fail-closed**,绝不退化成 bbox 矩形——"声明是掩膜、导出却是方框"正是
					// 那类"看着正常、内容错"的静默降级。
					rle, rerr := maskToCocoRLE(pngB64, w, h)
					if rerr != nil {
						return nil, fmt.Errorf("导出中止:资产 %d 的掩膜无法编码为 RLE: %w", fa.AssetID, rerr)
					}
					segmentation = rle
					area = rleForegroundArea(rle) // #12 掩膜面积 = 前景像素数,不是 bbox 面积
				}
			}
			if segmentation == nil {
				poly := shapePolygonFlat(sh)
				segmentation = [][]float64{poly}
				if sh.Kind == "polygon" && len(poly) >= 6 {
					area = polygonArea(poly) // #12 多边形面积用鞋带公式,不是 bbox 面积
				}
			}

			annotations = append(annotations, map[string]interface{}{
				"id": annID, "image_id": imgID,
				"category_id":  b.catIndex[labelOf(sh)] + 1,
				"bbox":         []float64{bx, by, bw, bh},
				"area":         area,
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
		stem := safeStem(stripExt(b.fileName(fa.AssetID))) // #14 zip-slip:OriginalName 不可信
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
