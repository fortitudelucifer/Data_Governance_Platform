package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// 一条 track：frame 0 → frame 2，bbox 沿 x 从 0 线性移到 20（fps=30）。
func motFixture() vidDoc {
	return vidDoc{
		AssetID: 1, Name: "clip.mp4", Fps: 30, W: 640, H: 480,
		Tracks: []vidTrack{{
			TrackID: 7, Label: "car", Kind: "bbox",
			Keyframes: []paymodel.Keyframe{
				{Frame: 0, TsMs: 0, Bbox: []float64{0, 0, 10, 10}},
				{Frame: 2, TsMs: 2000.0 / 30.0, Bbox: []float64{20, 0, 10, 10}},
			},
		}},
	}
}

// MOT 逐帧展开：首末关键帧之间每帧一行，帧号 1-indexed，中间帧线性插值。
func TestWriteMOT_ExpandsAndInterpolates(t *testing.T) {
	var b bytes.Buffer
	writeMOT(&b, motFixture())
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")

	if len(lines) != 3 {
		t.Fatalf("expected 3 rows (frames 0..2), got %d: %q", len(lines), lines)
	}
	// MOT 帧号 1-indexed；track id 7；中间帧 x 应插值到 10。
	if !strings.HasPrefix(lines[0], "1,7,0.00,0.00,10.00,10.00,") {
		t.Errorf("row0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "2,7,10.00,0.00,10.00,10.00,") {
		t.Errorf("row1 (插值) = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "3,7,20.00,0.00,10.00,10.00,") {
		t.Errorf("row2 = %q", lines[2])
	}
}

// 流式写出必须与旧的整体拼装逐字节一致（重构不改变行为）。
func TestWriteMOT_MatchesBuildMOT(t *testing.T) {
	d := motFixture()
	var b bytes.Buffer
	writeMOT(&b, d)
	if got, want := b.String(), buildMOT(d); got != want {
		t.Fatalf("streamed != buffered\n streamed=%q\n buffered=%q", got, want)
	}
}

func TestWriteCVATXML_MatchesBuildCVATXML(t *testing.T) {
	d := motFixture()
	var b bytes.Buffer
	writeCVATXML(&b, d)
	got := b.String()
	if got != buildCVATXML(d) {
		t.Fatal("streamed CVAT-XML != buffered")
	}
	if !strings.Contains(got, `<track id="7" label="car">`) || !strings.Contains(got, `<box frame="0"`) {
		t.Errorf("unexpected CVAT-XML: %s", got)
	}
}

// outside 关键帧起停止产帧（与《插值规范》一致），不外推。
func TestWriteMOT_OutsideStopsRows(t *testing.T) {
	d := motFixture()
	d.Tracks[0].Keyframes[1].Outside = true
	var b bytes.Buffer
	writeMOT(&b, d)
	rows := strings.Count(strings.TrimRight(b.String(), "\n"), "\n") + 1
	if b.Len() == 0 {
		rows = 0
	}
	if rows >= 3 {
		t.Fatalf("outside 关键帧应停止产帧，却仍有 %d 行: %q", rows, b.String())
	}
}

// --- COCO / YOLO / Datumaro ---

// COCO：逐帧检测。images/annotations/categories 三段必须是合法 JSON，
// track 身份不在 COCO schema 里，放进 attributes 以免往返丢失。
func TestWriteCOCO(t *testing.T) {
	var b bytes.Buffer
	writeCOCO(&b, []vidDoc{motFixture()})

	var out struct {
		Images []struct {
			ID       int    `json:"id"`
			FileName string `json:"file_name"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Frame    int    `json:"frame"`
		} `json:"images"`
		Annotations []struct {
			ID         int       `json:"id"`
			ImageID    int       `json:"image_id"`
			CategoryID int       `json:"category_id"`
			Bbox       []float64 `json:"bbox"`
			Area       float64   `json:"area"`
			Attributes struct {
				TrackID int `json:"track_id"`
			} `json:"attributes"`
		} `json:"annotations"`
		Categories []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("COCO 不是合法 JSON: %v\n%s", err, b.String())
	}
	if len(out.Images) != 3 || len(out.Annotations) != 3 {
		t.Fatalf("expected 3 images / 3 anns, got %d/%d", len(out.Images), len(out.Annotations))
	}
	if out.Images[0].FileName != "clip/000000.jpg" || out.Images[0].Frame != 0 ||
		out.Images[0].Width != 640 || out.Images[0].Height != 480 {
		t.Errorf("image0 = %+v", out.Images[0])
	}
	if out.Annotations[0].CategoryID != 1 {
		t.Errorf("category_id = %d, want 1 (COCO 1-indexed)", out.Annotations[0].CategoryID)
	}
	// 中间帧插值：x 应为 10；area = w*h = 100。
	if got := out.Annotations[1].Bbox; got[0] != 10 {
		t.Errorf("ann1 bbox 插值 x 应为 10, got %v", got)
	}
	if out.Annotations[1].Area != 100 {
		t.Errorf("ann1 area = %v, want 100", out.Annotations[1].Area)
	}
	if out.Annotations[0].Attributes.TrackID != 7 {
		t.Errorf("track_id 未保留: %+v", out.Annotations[0])
	}
	// image_id 必须真的指向 images 里存在的 id。
	ids := map[int]bool{}
	for _, im := range out.Images {
		ids[im.ID] = true
	}
	for _, a := range out.Annotations {
		if !ids[a.ImageID] {
			t.Errorf("annotation %d 指向不存在的 image_id %d", a.ID, a.ImageID)
		}
	}
	if len(out.Categories) != 1 || out.Categories[0].Name != "car" || out.Categories[0].ID != 1 {
		t.Errorf("categories = %+v (COCO 类别 1-indexed)", out.Categories)
	}
}

// 多视频导出：image id 不能跨视频撞车。
func TestWriteCOCO_ImageIDsUniqueAcrossVideos(t *testing.T) {
	a, b2 := motFixture(), motFixture()
	b2.AssetID, b2.Name = 2, "other.mp4"
	var buf bytes.Buffer
	writeCOCO(&buf, []vidDoc{a, b2})

	var out struct {
		Images []struct {
			ID int `json:"id"`
		} `json:"images"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	seen := map[int]bool{}
	for _, im := range out.Images {
		if seen[im.ID] {
			t.Fatalf("image id %d 重复（跨视频撞车）", im.ID)
		}
		seen[im.ID] = true
	}
	if len(out.Images) != 6 {
		t.Fatalf("expected 6 images across 2 videos, got %d", len(out.Images))
	}
}

// Datumaro：保留 track_id + keyframe 标记（相对 COCO 的无损优势）。
func TestWriteDatumaro(t *testing.T) {
	var b bytes.Buffer
	writeDatumaro(&b, []vidDoc{motFixture()})

	var out struct {
		Categories struct {
			Label struct {
				Labels []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"label"`
		} `json:"categories"`
		Items []struct {
			ID          string `json:"id"`
			Annotations []struct {
				Type       string    `json:"type"`
				LabelID    int       `json:"label_id"`
				Bbox       []float64 `json:"bbox"`
				Attributes struct {
					TrackID  int  `json:"track_id"`
					Keyframe bool `json:"keyframe"`
				} `json:"attributes"`
			} `json:"annotations"`
			Attr struct {
				Frame int `json:"frame"`
			} `json:"attr"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("Datumaro 不是合法 JSON: %v\n%s", err, b.String())
	}
	if len(out.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out.Items))
	}
	if out.Categories.Label.Labels[0].Name != "car" {
		t.Errorf("labels = %+v", out.Categories.Label.Labels)
	}
	if out.Items[0].ID != "clip/000000" {
		t.Errorf("item id = %q", out.Items[0].ID)
	}
	// keyframe 标记只在真关键帧（frame 0 / 2）上为 true，插值出的 frame 1 为 false。
	want := []bool{true, false, true}
	for i, it := range out.Items {
		if len(it.Annotations) != 1 {
			t.Fatalf("item %d: %d anns", i, len(it.Annotations))
		}
		a := it.Annotations[0]
		if a.Attributes.Keyframe != want[i] {
			t.Errorf("frame %d keyframe=%v, want %v", it.Attr.Frame, a.Attributes.Keyframe, want[i])
		}
		if a.Attributes.TrackID != 7 || a.Type != "bbox" || a.LabelID != 0 {
			t.Errorf("item %d ann = %+v", i, a)
		}
	}
}

// YOLO：每个有标注的帧一个 txt + 数据集级 classes.txt；坐标归一化 cx cy w h。
func TestWriteYOLOZip(t *testing.T) {
	files := map[string]*bytes.Buffer{}
	addFile := func(name string) (io.Writer, error) {
		if _, dup := files[name]; dup {
			return nil, fmt.Errorf("重复条目 %s", name)
		}
		files[name] = &bytes.Buffer{}
		return files[name], nil
	}
	if err := writeYOLOZip([]vidDoc{motFixture()}, addFile); err != nil {
		t.Fatalf("writeYOLOZip: %v", err)
	}

	if got := files["classes.txt"].String(); got != "car\n" {
		t.Errorf("classes.txt = %q", got)
	}
	for _, f := range []string{"labels/clip/000000.txt", "labels/clip/000001.txt", "labels/clip/000002.txt"} {
		if _, ok := files[f]; !ok {
			t.Fatalf("缺少 %s；实际条目 %v", f, keysOf(files))
		}
	}
	if len(files) != 4 {
		t.Errorf("expected classes.txt + 3 frames, got %v", keysOf(files))
	}
	// frame 0: bbox [0,0,10,10] @ 640x480 → cls cx cy w h（归一化）
	// cx = 5/640 = 0.0078125 恰在中点，%.6f 采用银行家舍入 → 0.007812
	want := "0 0.007812 0.010417 0.015625 0.020833\n"
	if got := files["labels/clip/000000.txt"].String(); got != want {
		t.Errorf("frame0 = %q, want %q", got, want)
	}
}

// 无宽高（探测失败）时不能产出 0 除的坐标 —— 整段跳过。
func TestWriteYOLOZip_SkipsDocWithoutDimensions(t *testing.T) {
	d := motFixture()
	d.W, d.H = 0, 0
	files := map[string]*bytes.Buffer{}
	err := writeYOLOZip([]vidDoc{d}, func(name string) (io.Writer, error) {
		files[name] = &bytes.Buffer{}
		return files[name], nil
	})
	if err != nil {
		t.Fatalf("writeYOLOZip: %v", err)
	}
	if len(files) != 1 { // 只有 classes.txt
		t.Fatalf("无宽高的视频不应产出标签文件，得到 %v", keysOf(files))
	}
}

func keysOf(m map[string]*bytes.Buffer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// zip 条目重名去重：同名视频导出不互相覆盖。
func TestZipEntryName_Dedupes(t *testing.T) {
	taken := map[string]string{}
	for _, want := range []string{"clip.txt", "clip-1.txt", "clip-2.txt"} {
		got := zipEntryName(taken, "clip", "mot")
		if got != want {
			t.Fatalf("expected %q, got %q", want, got)
		}
		taken[got] = ""
	}
	if got := zipEntryName(map[string]string{}, "clip", "cvat"); got != "clip.xml" {
		t.Fatalf("cvat ext: got %q", got)
	}
}

// --- polygon / mask 轨迹（SAM2 传播的产物）---

// SAM2 传播写的关键帧只有 points、没有 bbox。逐帧导出器若只认 bbox，
// 整条 mask 轨迹会**静默消失**在 MOT/COCO/YOLO/Datumaro 里。
func maskFixture() vidDoc {
	tri := func(dx float64) []float64 { return []float64{dx, 0, dx + 20, 0, dx + 10, 20} }
	return vidDoc{
		AssetID: 1, Name: "clip.mp4", Fps: 30, W: 640, H: 480,
		Tracks: []vidTrack{{
			TrackID: 3, Label: "cow", Kind: "polygon",
			Keyframes: []paymodel.Keyframe{
				{Frame: 0, TsMs: 0, Points: tri(0)},
				{Frame: 2, TsMs: 2000.0 / 30.0, Points: tri(40)},
			},
		}},
	}
}

func TestWriteMOT_PolygonTrackNotDropped(t *testing.T) {
	var b bytes.Buffer
	writeMOT(&b, maskFixture())
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	if b.Len() == 0 {
		t.Fatal("polygon 轨迹被整条丢掉了（MOT 空输出）")
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 rows, got %d: %q", len(lines), lines)
	}
	// frame 0 三角形 (0,0)-(20,0)-(10,20) → 外接框 x=0 y=0 w=20 h=20
	if !strings.HasPrefix(lines[0], "1,3,0.00,0.00,20.00,20.00,") {
		t.Errorf("row0 应为多边形外接框, got %q", lines[0])
	}
	// frame 1 插值到 dx=20 → x=20
	if !strings.HasPrefix(lines[1], "2,3,20.00,0.00,20.00,20.00,") {
		t.Errorf("row1 应为插值后的外接框, got %q", lines[1])
	}
}

func TestWriteCOCO_PolygonTrackCarriesSegmentation(t *testing.T) {
	var b bytes.Buffer
	writeCOCO(&b, []vidDoc{maskFixture()})
	var out struct {
		Images      []struct{ ID int } `json:"images"`
		Annotations []struct {
			Bbox         []float64   `json:"bbox"`
			Segmentation [][]float64 `json:"segmentation"`
			Area         float64     `json:"area"`
		} `json:"annotations"`
	}
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("COCO 不是合法 JSON: %v\n%s", err, b.String())
	}
	if len(out.Annotations) != 3 {
		t.Fatalf("polygon 轨迹应产出 3 条标注, got %d", len(out.Annotations))
	}
	a := out.Annotations[0]
	if len(a.Segmentation) != 1 || len(a.Segmentation[0]) != 6 {
		t.Fatalf("COCO 应带 segmentation 多边形, got %+v", a.Segmentation)
	}
	if !reflect.DeepEqual(a.Bbox, []float64{0, 0, 20, 20}) {
		t.Errorf("bbox 应由多边形导出, got %v", a.Bbox)
	}
}

func TestWriteYOLOZip_PolygonTrackNotDropped(t *testing.T) {
	files := map[string]*bytes.Buffer{}
	if err := writeYOLOZip([]vidDoc{maskFixture()}, func(name string) (io.Writer, error) {
		files[name] = &bytes.Buffer{}
		return files[name], nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := files["labels/clip/000000.txt"]; !ok {
		t.Fatalf("polygon 轨迹在 YOLO 里被丢掉了，条目=%v", keysOf(files))
	}
}
