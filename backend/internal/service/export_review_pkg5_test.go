package service

// 包5(导出器)对抗式 review 的回归锁:把最容易"看着正常、内容错"的几条钉成纯函数
// 断言。跑不需要 DB。

import (
	"strings"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// #14 zip-slip:untrusted OriginalName 不能变成 zip 路径穿越条目。
func TestSafeStem_NeutralizesTraversal(t *testing.T) {
	// 含分隔符 / .. 的都被压成单段、无分隔符;纯点/空 → unnamed。
	for _, in := range []string{"../../outside", `..\..\win`, "/abs/x", "a/b/c", "..", ".", ""} {
		got := safeStem(in)
		if strings.ContainsAny(got, `/\`) {
			t.Fatalf("safeStem(%q)=%q 仍含路径分隔符(zip-slip 未挡住)", in, got)
		}
		if got == "" || strings.Trim(got, ".") == "" {
			// 纯点/空 → unnamed
			if got != "unnamed" {
				t.Fatalf("safeStem(%q)=%q,纯点/空应为 unnamed", in, got)
			}
		}
	}
	// 干净名原样通过(不误伤既有文件名)。
	if got := safeStem("scan01"); got != "scan01" {
		t.Fatalf("safeStem(scan01)=%q,干净名应原样", got)
	}
}

// #8 空标签必须映到显式 unlabeled 类别,不落到 category 0(第一个真类别)。
func TestLabelIndex_EmptyLabelToUnlabeled(t *testing.T) {
	docs := []vidDoc{{Tracks: []vidTrack{
		{Label: "car"}, {Label: ""}, {Label: "person"},
	}}}
	names, idx := labelIndex(docs)
	// unlabeled 必须在类别表里。
	found := false
	for _, n := range names {
		if n == unlabeledCategory {
			found = true
		}
	}
	if !found {
		t.Fatalf("类别表 %v 缺 unlabeled", names)
	}
	// 空标签的 idx 必须指向 unlabeled,而不是 0(car/person 之一)。
	if idx[""] != idx[unlabeledCategory] {
		t.Fatalf("空标签 idx=%d 应等于 unlabeled idx=%d(否则被导成第一个类别)", idx[""], idx[unlabeledCategory])
	}
	if names[idx[""]] == "car" || names[idx[""]] == "person" {
		t.Fatalf("#8 回归:空标签落到了真类别 %q", names[idx[""]])
	}
}

// #23 slide:outside 关键帧跳过、奇数坐标 fail-closed。
func TestRingForTrack_OutsideAndOddCoords(t *testing.T) {
	// outside 关键帧即便残留 points 也不产几何(跳过,不报错)。
	tOut := paymodel.Track{Kind: paymodel.TrackKindPolygon, Keyframes: []paymodel.Keyframe{
		{Outside: true, Points: []float64{0, 0, 10, 0, 10, 10}},
	}}
	if _, ok, err := ringForTrack(tOut); ok || err != nil {
		t.Fatalf("#23 回归:outside 应跳过(ok=false,err=nil),得到 ok=%v err=%v", ok, err)
	}
	// 奇数坐标 = 数据损坏 → 报错(不静默丢掉最后一维)。
	tOdd := paymodel.Track{Kind: paymodel.TrackKindPolygon, Keyframes: []paymodel.Keyframe{
		{Points: []float64{0, 0, 10, 0, 10, 10, 5}}, // 7 个数
	}}
	if _, _, err := ringForTrack(tOdd); err == nil {
		t.Fatalf("#23 回归:奇数坐标应 fail-closed 报错,却通过了")
	}
	// 正常多边形照常闭合导出。
	tOK := paymodel.Track{Kind: paymodel.TrackKindPolygon, Keyframes: []paymodel.Keyframe{
		{Points: []float64{0, 0, 10, 0, 10, 10}},
	}}
	ring, ok, err := ringForTrack(tOK)
	if !ok || err != nil || len(ring) < 4 {
		t.Fatalf("正常多边形应正常导出,得到 ok=%v err=%v ring=%v", ok, err, ring)
	}
	if ring[0][0] != ring[len(ring)-1][0] || ring[0][1] != ring[len(ring)-1][1] {
		t.Fatalf("环必须闭合(首尾同点),得到 %v", ring)
	}
}

// #12 COCO 掩膜面积 = 前景像素数,不是 bbox 面积。
func TestRleForegroundArea(t *testing.T) {
	// counts 从背景开始交替:bg,fg,bg,fg… 前景=奇数位之和=5+7=12。
	rle := map[string]interface{}{"counts": []int{3, 5, 2, 7}}
	if got := rleForegroundArea(rle); got != 12 {
		t.Fatalf("rleForegroundArea=%v,want 12(前景 run 之和)", got)
	}
}
