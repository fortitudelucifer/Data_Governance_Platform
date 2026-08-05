package service

// COCO RLE 编解码的语义锁。掩膜编码错了不会报错——画面上是一片"看着像器官"
// 的形状,位置/形状却是错的。所以往返、已知向量、坏输入三条都钉死,
// 并与 Python 独立实现交叉验证(见 coco_rle_python_test.go 的说明)。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	paymodel "text-annotation-platform/internal/model/payload"
)

// maskFromRows 用可视化的字符行造掩膜('#'=1),行优先。
func maskFromRows(rows []string) ([]byte, int, int) {
	h := len(rows)
	w := len(rows[0])
	m := make([]byte, h*w)
	for y, r := range rows {
		for x := 0; x < w; x++ {
			if r[x] == '#' {
				m[y*w+x] = 1
			}
		}
	}
	return m, h, w
}

func rowsFromMask(m []byte, h, w int) []string {
	out := make([]string, h)
	for y := 0; y < h; y++ {
		b := make([]byte, w)
		for x := 0; x < w; x++ {
			if m[y*w+x] != 0 {
				b[x] = '#'
			} else {
				b[x] = '.'
			}
		}
		out[y] = string(b)
	}
	return out
}

var rleTestShapes = map[string][]string{
	"实心矩形": {
		"........",
		"..####..",
		"..####..",
		"..####..",
		"........",
		"........",
	},
	"对角线": {
		"#.......",
		".#......",
		"..#.....",
		"...#....",
		"....#...",
		".....#..",
	},
	"空掩膜": {
		"........",
		"........",
		"........",
		"........",
		"........",
		"........",
	},
	"全满": {
		"########",
		"########",
		"########",
		"########",
		"########",
		"########",
	},
	"分裂两块": {
		"##....##",
		"##....##",
		"........",
		"........",
		"##....##",
		"##....##",
	},
}

func TestCOCORLE_RoundTrip(t *testing.T) {
	for name, rows := range rleTestShapes {
		t.Run(name, func(t *testing.T) {
			mask, h, w := maskFromRows(rows)
			rle, err := EncodeCOCORLE(mask, h, w, true)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if rle.Size != [2]int{h, w} {
				t.Fatalf("size = %v want [%d %d]", rle.Size, h, w)
			}
			got, gh, gw, err := DecodeCOCORLE(rle)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if gh != h || gw != w {
				t.Fatalf("decoded size %dx%d want %dx%d", gh, gw, h, w)
			}
			gotRows := rowsFromMask(got, h, w)
			for y := range rows {
				if gotRows[y] != rows[y] {
					t.Fatalf("row %d: %q want %q\n完整还原:\n%s", y, gotRows[y], rows[y], strings.Join(gotRows, "\n"))
				}
			}
		})
	}
}

// column-major 展平顺序是 COCO 的硬约定。用一个**非对称**图形验证:若误按
// row-major 展平,counts 会不同(转置的掩膜解出来也是"像模像样"的形状)。
func TestCOCORLE_ColumnMajorOrder(t *testing.T) {
	// 只有 (0,1) 一个点亮:row-major 展平下它在 index 1;column-major 下在 index h。
	rows := []string{
		".#..",
		"....",
		"....",
	}
	mask, h, w := maskFromRows(rows)
	rle, err := EncodeCOCORLE(mask, h, w, true)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	counts, err := rleFromString(rle.Counts)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	// column-major: 先 3 个 0(第 0 列全空),再 1 个 1,再补满剩下。
	if counts[0] != h {
		t.Fatalf("首个 0 游程 = %d,want %d —— 展平顺序不是 column-major", counts[0], h)
	}
	if counts[1] != 1 {
		t.Fatalf("第二个游程 = %d,want 1", counts[1])
	}
}

func TestCOCORLE_RejectsCorruptInput(t *testing.T) {
	// size 与游程和对不上 → 必须报错,绝不铺一张歪掩膜。
	bad := &paymodel.MaskRLE{Size: [2]int{4, 4}, Counts: rleToString([]int{3, 2})} // sum=5 ≠ 16
	if _, _, _, err := DecodeCOCORLE(bad); err == nil {
		t.Fatal("游程和与 size 不符必须报错(歪掩膜最难发现)")
	}
	if _, _, _, err := DecodeCOCORLE(nil); err == nil {
		t.Fatal("nil RLE 必须报错")
	}
	if _, _, _, err := DecodeCOCORLE(&paymodel.MaskRLE{Size: [2]int{0, 4}, Counts: ""}); err == nil {
		t.Fatal("非法 size 必须报错")
	}
	if _, err := EncodeCOCORLE(make([]byte, 5), 2, 3, true); err == nil {
		t.Fatal("掩膜长度与 size 不符必须报错")
	}
	// #11 短 counts 声明巨型 size(h*w=1e12):修复前 make([]byte, 1e12) OOM;修复后按
	// 像素预算在解码前拒。变异:去掉 DecodeCOCORLE 的 maxRLEPixels 检查,本断言红(或 OOM)。
	if _, _, _, err := DecodeCOCORLE(&paymodel.MaskRLE{Size: [2]int{1000000, 1000000}, Counts: "0"}); err == nil {
		t.Fatal("巨型 RLE size 必须在解码前被拒(防 TB 级分配 OOM)")
	}
}

// counts 位串的 delta 编码在第 3 个游程起生效,是最容易写错的一段。
// 用一个游程数 > 3 的图形验证 string↔ints 往返。
func TestCOCORLE_CountsStringRoundTrip(t *testing.T) {
	for _, counts := range [][]int{
		{5, 3, 7, 2, 9, 1},
		{0, 16},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{1000, 2000, 3000, 40000},
	} {
		s := rleToString(counts)
		got, err := rleFromString(s)
		if err != nil {
			t.Fatalf("counts %v -> %q: %v", counts, s, err)
		}
		if len(got) != len(counts) {
			t.Fatalf("counts %v -> %v (长度不符)", counts, got)
		}
		for i := range counts {
			if got[i] != counts[i] {
				t.Fatalf("counts %v -> %v (第 %d 项)", counts, got, i)
			}
		}
	}
}

// **权威交叉验证**:testdata/rle/shapes.json 的 counts 位串由参考实现
// pycocotools 产出并核验(2026-07-19 实跑)。往返测试只能证明"自洽",
// 证明不了"标准"——一个自洽但非标的编码器照样能往返成功,直到导出的掩膜
// 拿去 3D Slicer/QuPath 打开才发现全错。这条把 Go 侧钉在参考实现上;
// frontend/src/lib/cocoRLE.test.ts 用同一份夹具钉 TS 侧,三方一致。
func TestCOCORLE_MatchesPycocotoolsFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "rle", "shapes.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v (三方锁的一半，不能缺)", path, err)
	}
	var fx struct {
		Shapes []struct {
			Name   string   `json:"name"`
			H      int      `json:"h"`
			W      int      `json:"w"`
			Rows   []string `json:"rows"`
			Counts string   `json:"counts"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fx.Shapes) == 0 {
		t.Fatal("fixture 里一个形状都没有")
	}
	for _, s := range fx.Shapes {
		t.Run(s.Name, func(t *testing.T) {
			mask, h, w := maskFromRows(s.Rows)
			if h != s.H || w != s.W {
				t.Fatalf("fixture 自身不一致: rows 是 %dx%d, 声明 %dx%d", h, w, s.H, s.W)
			}
			rle, err := EncodeCOCORLE(mask, h, w, true)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if rle.Counts != s.Counts {
				t.Fatalf("counts = %q,参考实现(pycocotools)是 %q —— 编码器偏离标准，"+
					"导出的掩膜到 3D Slicer 里会是错的", rle.Counts, s.Counts)
			}
			// 反向:解参考位串必须还原同一张掩膜。
			got, _, _, err := DecodeCOCORLE(&paymodel.MaskRLE{Size: [2]int{s.H, s.W}, Counts: s.Counts})
			if err != nil {
				t.Fatalf("decode reference counts: %v", err)
			}
			gotRows := rowsFromMask(got, h, w)
			for y := range s.Rows {
				if gotRows[y] != s.Rows[y] {
					t.Fatalf("解参考位串第 %d 行 = %q want %q", y, gotRows[y], s.Rows[y])
				}
			}
		})
	}
}

// 护栏阈值必须与契约一致(00 规则 1),且量级判断与 C0.3 实测吻合:
// 真实脑分割单切片 3.4KB,离 64KB 护栏很远。
func TestCOCORLE_GuardrailConstants(t *testing.T) {
	if MaxKeyframeRLEBytes != 64*1024 {
		t.Fatalf("单关键帧护栏 = %d,契约写的是 64KB", MaxKeyframeRLEBytes)
	}
	if MaxTrackGeomBytes != 4*1024*1024 {
		t.Fatalf("整 track 护栏 = %d,契约写的是 4MB", MaxTrackGeomBytes)
	}
}
