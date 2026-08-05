package service

import (
	"encoding/json"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// C1.4 GeoJSON 导出。锁 QuPath 兼容的结构 + level-0 像素坐标 + 环闭合。

func decodeFC(t *testing.T, blob []byte) geoFeatureCollection {
	t.Helper()
	var fc geoFeatureCollection
	if err := json.Unmarshal(blob, &fc); err != nil {
		t.Fatalf("导出的不是合法 JSON: %v", err)
	}
	if fc.Type != "FeatureCollection" {
		t.Fatalf("顶层 type 应为 FeatureCollection, 得到 %q", fc.Type)
	}
	return fc
}

func TestBuildSlideGeoJSON_Polygon(t *testing.T) {
	tracks := []paymodel.Track{{
		Kind:  paymodel.TrackKindPolygon,
		Label: "Tumor",
		Color: "#ef4444",
		Keyframes: []paymodel.Keyframe{{
			Frame:  0,
			Points: []float64{100, 200, 400, 200, 400, 500}, // 三角形(未闭合)
		}},
	}}
	fc := decodeFC(t, mustBuild(t, tracks))
	if len(fc.Features) != 1 {
		t.Fatalf("应有 1 个 feature, 得到 %d", len(fc.Features))
	}
	f := fc.Features[0]
	if f.Type != "Feature" || f.Geometry.Type != "Polygon" || f.Properties.ObjectType != "annotation" {
		t.Fatalf("QuPath 结构不符: %+v", f)
	}
	ring := f.Geometry.Coordinates[0]
	// 环必须闭合:首尾点相同(3 顶点 → 4 个点)。
	if len(ring) != 4 {
		t.Fatalf("三角形闭合后应 4 个点, 得到 %d: %v", len(ring), ring)
	}
	if ring[0][0] != ring[3][0] || ring[0][1] != ring[3][1] {
		t.Fatalf("环未闭合: 首 %v 尾 %v", ring[0], ring[3])
	}
	// 坐标是 level-0 像素, 原样搬(不做任何换算)。
	if ring[0][0] != 100 || ring[0][1] != 200 || ring[1][0] != 400 {
		t.Fatalf("坐标被改动了: %v", ring)
	}
	// classification = 标签 + 解析出的 RGB。
	if f.Properties.Classification == nil || f.Properties.Classification.Name != "Tumor" {
		t.Fatalf("classification 缺失或标签错: %+v", f.Properties.Classification)
	}
	if got := f.Properties.Classification.Color; got != [3]int{239, 68, 68} {
		t.Fatalf("#ef4444 应解析成 [239 68 68], 得到 %v", got)
	}
}

func TestBuildSlideGeoJSON_Bbox(t *testing.T) {
	tracks := []paymodel.Track{{
		Kind:      paymodel.TrackKindBBox,
		Label:     "ROI",
		Color:     "#22c55e",
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{10, 20, 30, 40}}}, // x,y,w,h
	}}
	fc := decodeFC(t, mustBuild(t, tracks))
	ring := fc.Features[0].Geometry.Coordinates[0]
	// 矩形 → 4 角 + 闭合点 = 5。
	want := [][]float64{{10, 20}, {40, 20}, {40, 60}, {10, 60}, {10, 20}}
	if len(ring) != 5 {
		t.Fatalf("矩形闭合后应 5 个点, 得到 %d", len(ring))
	}
	for i := range want {
		if ring[i][0] != want[i][0] || ring[i][1] != want[i][1] {
			t.Fatalf("角点 %d 错: 得到 %v 期望 %v (bbox 转角算错?)", i, ring[i], want[i])
		}
	}
}

func TestBuildSlideGeoJSON_SkipsNonRegion(t *testing.T) {
	tracks := []paymodel.Track{
		{Kind: paymodel.TrackKindVoxelMask, Label: "肝", Keyframes: []paymodel.Keyframe{{Frame: 0, RLE: &paymodel.MaskRLE{Size: [2]int{4, 4}, Counts: "x"}}}},
		{Kind: paymodel.TrackKindKeypoints, Label: "点", Keyframes: []paymodel.Keyframe{{Frame: 0, Points: []float64{1, 2}}}},
	}
	fc := decodeFC(t, mustBuild(t, tracks))
	if len(fc.Features) != 0 {
		t.Fatalf("voxel_mask/keypoints 不是 2D 区域, 应跳过, 却导出了 %d 个", len(fc.Features))
	}
}

func TestHexToRGB(t *testing.T) {
	cases := map[string][3]int{
		"#ef4444": {239, 68, 68},
		"22c55e":  {34, 197, 94},
		"#000000": {0, 0, 0},
		"#ffffff": {255, 255, 255},
		"":        {200, 200, 200}, // 解析不了 → 中性灰,不编鲜艳色
		"#zzz":    {200, 200, 200},
	}
	for hex, want := range cases {
		if got := hexToRGB(hex); got != want {
			t.Fatalf("hexToRGB(%q)=%v 期望 %v", hex, got, want)
		}
	}
}

func mustBuild(t *testing.T, tracks []paymodel.Track) []byte {
	t.Helper()
	b, err := BuildSlideGeoJSON(tracks)
	if err != nil {
		t.Fatalf("BuildSlideGeoJSON: %v", err)
	}
	return b
}
