package service

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// C3.4b 导出服务。核心风险是**位置**:掩膜落偏一格、或 bbox 的裁放搞反,
// 画面上依旧像模像样,只是长在错的地方(C3.3c-1 已栽过一次)。所以测试都盯
// "具体哪个体素被点亮",不测"有多少个体素"。

var expDims = [3]int{8, 6, 4}

// segTrack 造一条 voxel_mask 轨迹:在第 z 层、以 (bx,by) 为左上角放一块 h×w 的实心块。
func segTrack(t *testing.T, trackID int, label string, z, bx, by, h, w int) paymodel.Track {
	t.Helper()
	sub := make([]byte, h*w)
	for i := range sub {
		sub[i] = 1
	}
	rle, err := EncodeCOCORLE(sub, h, w, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return paymodel.Track{
		TrackID: trackID, Label: label, Kind: paymodel.TrackKindVoxelMask,
		Keyframes: []paymodel.Keyframe{{
			Frame: z, Bbox: []float64{float64(bx), float64(by), float64(w), float64(h)}, RLE: rle,
		}},
	}
}

func TestBuildVolumeSegmentsPlacesVoxelsExactly(t *testing.T) {
	// 一块 2×3(h×w)的实心块,左上角在 (bx=2, by=1),第 z=2 层。
	tr := segTrack(t, 1, "肿瘤", 2, 2, 1, 2, 3)
	segs, err := BuildVolumeSegments([]paymodel.Track{tr}, expDims)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	s := segs[0]
	if s.Voxels != 6 || s.Slices != 1 {
		t.Errorf("voxels=%d slices=%d, want 6/1", s.Voxels, s.Slices)
	}

	nx, ny := expDims[0], expDims[1]
	plane := nx * ny
	on := func(x, y, z int) bool { return s.mask[z*plane+y*nx+x] != 0 }

	// **逐点断言**:哪些亮、哪些不亮。只数个数的话,整体平移一格照样"6 个体素"。
	for y := 1; y <= 2; y++ {
		for x := 2; x <= 4; x++ {
			if !on(x, y, 2) {
				t.Errorf("体素 (%d,%d,2) 应被点亮", x, y)
			}
		}
	}
	// 紧邻的一圈必须是灭的——差一格的错就在这里显形。
	for _, p := range [][3]int{{1, 1, 2}, {5, 1, 2}, {2, 0, 2}, {2, 3, 2}, {2, 1, 1}, {2, 1, 3}} {
		if on(p[0], p[1], p[2]) {
			t.Errorf("体素 (%d,%d,%d) 不该被点亮(掩膜整体偏移了?)", p[0], p[1], p[2])
		}
	}
}

func TestBuildVolumeSegmentsStableLabelValues(t *testing.T) {
	// 标签值必须按 TrackID 稳定分配。若随输入顺序变,"标签 2"每次导出含义都不同,
	// 下游训练集会被静默污染。故意乱序传入。
	a := segTrack(t, 7, "肝", 1, 0, 0, 2, 2)
	b := segTrack(t, 3, "肿瘤", 1, 4, 2, 2, 2)
	segs, err := BuildVolumeSegments([]paymodel.Track{a, b}, expDims)
	if err != nil {
		t.Fatal(err)
	}
	if segs[0].Label != "肿瘤" || segs[0].Value != 1 {
		t.Errorf("第一段应是 TrackID 最小的「肿瘤」=1，得到 %q=%d", segs[0].Label, segs[0].Value)
	}
	if segs[1].Label != "肝" || segs[1].Value != 2 {
		t.Errorf("第二段应是「肝」=2，得到 %q=%d", segs[1].Label, segs[1].Value)
	}
}

func TestCombineReportsOverlapInsteadOfLosingItSilently(t *testing.T) {
	// 「肿瘤在肝内」——医学上的常态。单张标签图里一个体素只能有一个值,
	// 必然丢一部分;**丢多少必须说出来**(00 契约第一条:人工数据绝不丢失)。
	liver := segTrack(t, 1, "肝", 1, 0, 0, 4, 4)   // 4×4 = 16
	tumor := segTrack(t, 2, "肿瘤", 1, 1, 1, 2, 2) // 2×2 = 4，完全在肝内
	segs, err := BuildVolumeSegments([]paymodel.Track{liver, tumor}, expDims)
	if err != nil {
		t.Fatal(err)
	}
	labels, overlaps := CombineLabelVolume(segs, expDims)

	if len(overlaps) != 1 {
		t.Fatalf("want 1 overlap pair, got %d: %+v", len(overlaps), overlaps)
	}
	o := overlaps[0]
	if o.Voxels != 4 || o.LostVoxels != 4 || o.Loser != "肝" {
		t.Errorf("重叠报告不对: %+v（应为肝丢 4 个体素）", o)
	}
	if note := BuildOverlapNote(overlaps); note == "" {
		t.Error("有重叠时必须给出人话提示")
	}

	// 后者胜出：重叠处应是肿瘤的值。
	nx, ny := expDims[0], expDims[1]
	plane := nx * ny
	at := func(x, y, z int) uint16 { return labels[z*plane+y*nx+x] }
	if got := at(1, 1, 1); got != 2 {
		t.Errorf("重叠处标签 = %d，应为肿瘤 2", got)
	}
	if got := at(0, 0, 1); got != 1 {
		t.Errorf("仅肝处标签 = %d，应为肝 1", got)
	}

	// **逐段二值必须无损**——这正是 split 存在的理由。
	if n := countNonZero(SegmentBinaryLabels(segs[0])); n != 16 {
		t.Errorf("逐段二值里肝应有 16 个体素（无损），得到 %d", n)
	}
}

func TestCombineNoOverlapReportsNothing(t *testing.T) {
	a := segTrack(t, 1, "A", 1, 0, 0, 2, 2)
	b := segTrack(t, 2, "B", 1, 4, 3, 2, 2)
	segs, _ := BuildVolumeSegments([]paymodel.Track{a, b}, expDims)
	_, overlaps := CombineLabelVolume(segs, expDims)
	if len(overlaps) != 0 {
		t.Errorf("不相交的段不该报重叠: %+v", overlaps)
	}
	if BuildOverlapNote(overlaps) != "" {
		t.Error("无重叠时不该有提示文案（误报会让人忽略真警告）")
	}
}

func TestBuildVolumeSegmentsRejectsOutOfRangeSlice(t *testing.T) {
	// 关键帧落在体外 = 标签图会错得下游无从察觉。必须大声拒绝而不是跳过。
	tr := segTrack(t, 1, "x", 99, 0, 0, 2, 2)
	if _, err := BuildVolumeSegments([]paymodel.Track{tr}, expDims); err == nil {
		t.Fatal("层号越界必须报错")
	}
}

func TestBuildVolumeSegmentsRejectsRLEWithoutBbox(t *testing.T) {
	// RLE 是 **bbox-local** 的；没有 bbox 就无从还原位置，掩膜会落在 (0,0)——
	// 一张"看起来正常但位置全错"的图。
	tr := segTrack(t, 1, "x", 1, 0, 0, 2, 2)
	tr.Keyframes[0].Bbox = nil
	if _, err := BuildVolumeSegments([]paymodel.Track{tr}, expDims); err == nil {
		t.Fatal("有 RLE 无 bbox 必须报错")
	}
}

func TestBuildVolumeSegmentsSkipsOutsideKeyframes(t *testing.T) {
	tr := segTrack(t, 1, "x", 1, 0, 0, 2, 2)
	tr.Keyframes[0].Outside = true
	segs, err := BuildVolumeSegments([]paymodel.Track{tr}, expDims)
	if err != nil {
		t.Fatal(err)
	}
	if segs[0].Voxels != 0 {
		t.Errorf("outside 关键帧不该产帧，得到 %d 个体素", segs[0].Voxels)
	}
}

func TestBuildVolumeSegmentsIgnoresNonVoxelTracks(t *testing.T) {
	box := paymodel.Track{TrackID: 1, Label: "bbox 轨迹", Kind: paymodel.TrackKindBBox,
		Keyframes: []paymodel.Keyframe{{Frame: 0, Bbox: []float64{1, 1, 2, 2}}}}
	segs, err := BuildVolumeSegments([]paymodel.Track{box, segTrack(t, 2, "体素", 1, 0, 0, 2, 2)}, expDims)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].Label != "体素" {
		t.Errorf("只应导出 voxel_mask 轨迹，得到 %+v", segs)
	}
}

func TestWriteVolumeExportZipContents(t *testing.T) {
	segs, err := BuildVolumeSegments([]paymodel.Track{
		segTrack(t, 1, "肝", 1, 0, 0, 2, 2),
		segTrack(t, 2, "肿瘤/右叶", 1, 4, 3, 2, 2), // 名字里带 '/'：文件名必须能用
	}, expDims)
	if err != nil {
		t.Fatal(err)
	}
	man := VolumeExportManifest{Dims: expDims, Segments: segs, Convention: "RAS+"}
	aff := [3][4]float64{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}}
	raw, err := WriteVolumeExportZip(segs, man, expDims, aff, nil)
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == "labels.json" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			if !bytes.Contains(b, []byte("肝")) {
				t.Error("labels.json 里应保留中文标签")
			}
		}
	}
	if !names["labels.json"] {
		t.Error("缺 labels.json")
	}
	// 中文保留、斜杠被替换——不能把 CJK 标签洗成空串（那样 zip 就没法读了）。
	if !names["seg_02_肿瘤_右叶.nii.gz"] {
		t.Errorf("文件名不对，实际有: %v", zipNames(names))
	}
}

func TestSafeFileLabelKeepsCJK(t *testing.T) {
	if got := safeFileLabel("肝脏"); got != "肝脏" {
		t.Errorf("中文标签不该被洗掉: %q", got)
	}
	if got := safeFileLabel("a/b:c"); got != "a_b_c" {
		t.Errorf("非法字符应替换为下划线: %q", got)
	}
	if got := safeFileLabel(""); got != "unnamed" {
		t.Errorf("空标签应有兜底名: %q", got)
	}
}

func zipNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func countNonZero(v []uint16) int {
	n := 0
	for _, x := range v {
		if x != 0 {
			n++
		}
	}
	return n
}
