package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	paymodel "text-annotation-platform/internal/model/payload"
)

// C1.4 — 病理区域导出为 **GeoJSON**(QuPath 的原生标注交换格式)。
//
// # 为什么是 GeoJSON 而不是 COCO/自定义
//
// 病理下游主力是 QuPath / QuPath 生态,它导入导出 annotation 用的就是 GeoJSON
// FeatureCollection,坐标在**全分辨率(level-0)像素**——正好是我们几何一直存的空间
// (区域轨迹的 Keyframe.Points/Bbox 就是 level-0 像素),导出**零坐标换算**。
//
// # 会静默出错的点(与 C3.4 NIfTI 同类)
//
//   · **环必须闭合**:GeoJSON 多边形的每个 ring 首尾点必须相同。不闭合时有的解析器
//     容忍、有的把它当折线丢面积——"看着有形状、面积却是 0"。这里统一补上闭合点。
//   · **坐标是 [x, y] 且是 level-0 像素**:QuPath 按全分辨率像素摆标注;写成显示像素
//     或缩略图像素会整体错位而不报错。
//   · **口味相同的第三方等于没验**(CLAUDE.md 5d):真正的裁判是 **QuPath 本体**,
//     不是随便一个 GeoJSON 解析库。shapely 只能验"是合法几何",验不了 QuPath 的
//     classification/objectType 语义——那条留给 exports/verify_in_qupath.groovy。

// geoFeatureCollection 是 QuPath 认的顶层结构。
type geoFeatureCollection struct {
	Type     string       `json:"type"` // "FeatureCollection"
	Features []geoFeature `json:"features"`
}

type geoFeature struct {
	Type       string        `json:"type"` // "Feature"
	Geometry   geoGeometry   `json:"geometry"`
	Properties geoProperties `json:"properties"`
}

type geoGeometry struct {
	Type        string          `json:"type"` // "Polygon"
	Coordinates [][][]float64   `json:"coordinates"`
}

type geoProperties struct {
	ObjectType     string             `json:"objectType"` // "annotation"
	Name           string             `json:"name,omitempty"`
	Classification *geoClassification `json:"classification,omitempty"`
	IsLocked       bool               `json:"isLocked"`
}

// geoClassification 是 QuPath 的类别:名字 + RGB(0-255)。
type geoClassification struct {
	Name  string `json:"name"`
	Color [3]int `json:"color"`
}

// BuildSlideGeoJSON 把区域轨迹(polygon/bbox,单关键帧 frame=0)转成 QuPath GeoJSON。
//
// 只取每条轨迹的**首个关键帧**几何(WSI 无时间维,区域就一个关键帧);voxel_mask 等
// 其它 kind 直接跳过(它们不是 2D 区域)。返回的 JSON 缩进过,便于人工核对与 diff。
func BuildSlideGeoJSON(tracks []paymodel.Track) ([]byte, error) {
	fc := geoFeatureCollection{Type: "FeatureCollection", Features: []geoFeature{}}
	for _, t := range tracks {
		if len(t.Keyframes) == 0 {
			continue
		}
		ring, ok := ringForTrack(t)
		if !ok {
			continue // 非 2D 区域(voxel_mask/关键点等),或几何不足
		}
		props := geoProperties{ObjectType: "annotation", Name: t.Label}
		if t.Label != "" {
			props.Classification = &geoClassification{Name: t.Label, Color: hexToRGB(t.Color)}
		}
		fc.Features = append(fc.Features, geoFeature{
			Type:       "Feature",
			Geometry:   geoGeometry{Type: "Polygon", Coordinates: [][][]float64{ring}},
			Properties: props,
		})
	}
	return json.MarshalIndent(fc, "", "  ")
}

// ringForTrack 取轨迹首关键帧的几何,返回**闭合**的外环([point][x,y])。
// bbox → 四角矩形;polygon → 顶点序列。都在 level-0 像素空间。
func ringForTrack(t paymodel.Track) ([][]float64, bool) {
	kf := t.Keyframes[0]
	switch t.Kind {
	case paymodel.TrackKindBBox:
		if len(kf.Bbox) != 4 {
			return nil, false
		}
		x, y, w, h := kf.Bbox[0], kf.Bbox[1], kf.Bbox[2], kf.Bbox[3]
		return [][]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}, {x, y}}, true
	case paymodel.TrackKindPolygon, paymodel.TrackKindMask:
		// Points 是扁平 [x,y,x,y,...];至少 3 个点(6 个数)才是面。
		if len(kf.Points) < 6 {
			return nil, false
		}
		ring := make([][]float64, 0, len(kf.Points)/2+1)
		for i := 0; i+1 < len(kf.Points); i += 2 {
			ring = append(ring, []float64{kf.Points[i], kf.Points[i+1]})
		}
		return closeRing(ring), true
	case paymodel.TrackKindCells:
		// cells(病理 C2)是**检测/实例分割**,不是区域多边形——这份 GeoJSON 只导
		// 区域(C1.4)。cells 有意跳过,等 C2.4 走专用的 detection 导出(COCO detection /
		// QuPath objectType=detection,每实例 bbox-local RLE 按各自 bbox 平移回全图)。
		// 显式列出而非落进 default,免得日后当成漏导的 bug(#6 稀疏几何静默丢弃那类)。
		return nil, false
	default:
		return nil, false
	}
}

// closeRing 保证首尾点相同(GeoJSON 多边形环的硬要求)。已闭合则原样返回。
func closeRing(ring [][]float64) [][]float64 {
	if len(ring) < 3 {
		return ring
	}
	first, last := ring[0], ring[len(ring)-1]
	if first[0] != last[0] || first[1] != last[1] {
		ring = append(ring, []float64{first[0], first[1]})
	}
	return ring
}

// hexToRGB 解析 "#rrggbb"(或 "rrggbb")为 [r,g,b]。解析不了 → 中性灰,
// 绝不为了"有颜色"编一个鲜艳色误导下游按语义配色。
func hexToRGB(hex string) [3]int {
	s := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(s) != 6 {
		return [3]int{200, 200, 200}
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return [3]int{200, 200, 200}
	}
	return [3]int{int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff}
}

// SlideGeoJSONFilename 生成下载文件名(草稿显式带 _draft)。
func SlideGeoJSONFilename(taskID uint, draft bool) string {
	if draft {
		return fmt.Sprintf("task_%d_regions_draft.geojson", taskID)
	}
	return fmt.Sprintf("task_%d_regions.geojson", taskID)
}
