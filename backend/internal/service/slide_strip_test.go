package service

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// D-1b 剥离测试。分两层:
//  ① 自造的极小金字塔 TIFF —— CI 可跑、确定性,把"改链 + 抹像素 + 不碰金字塔"钉死
//     （真实切片在 CI 上不可达,样本覆盖不到的路径必须自己造）。
//  ② 真 CMU-1.svs（有则跑）—— 真实世界的 label/macro,且用 ExtractTile 逐字节对拍。

// buildSyntheticSlide 造一个 classic 小端 TIFF:一个 16×16 tiled 层 + 一个 4×4
// striped "label" 页(像素 = 0xAB 冒充烧录 PHI)。返回文件字节 + 几个关键偏移。
func buildSyntheticSlide() (data []byte, tileOff, labelOff, labelLen int) {
	const (
		tOff  = 8
		tData = "TILEDATA"
		lOff  = 16
		lLen  = 16
		dOff  = 32 // "label\0"
		ifd0  = 38
		ifd1  = 116
		total = 182
	)
	b := make([]byte, total)
	le := binary.LittleEndian
	copy(b[0:2], "II")
	le.PutUint16(b[2:4], 42)
	le.PutUint32(b[4:8], ifd0) // 第一个 IFD
	copy(b[tOff:tOff+8], tData)
	for i := lOff; i < lOff+lLen; i++ {
		b[i] = 0xAB // "PHI" 像素
	}
	copy(b[dOff:dOff+6], "label\x00")

	entry := func(pos int, tag, typ uint16, count, val uint32) {
		le.PutUint16(b[pos:], tag)
		le.PutUint16(b[pos+2:], typ)
		le.PutUint32(b[pos+4:], count)
		le.PutUint32(b[pos+8:], val)
	}
	// IFD0(tiled 层):256/257/322/323/324/325
	le.PutUint16(b[ifd0:], 6)
	entry(ifd0+2+0*12, 256, 3, 1, 16)
	entry(ifd0+2+1*12, 257, 3, 1, 16)
	entry(ifd0+2+2*12, 322, 3, 1, 16)
	entry(ifd0+2+3*12, 323, 3, 1, 16)
	entry(ifd0+2+4*12, 324, 4, 1, tOff)
	entry(ifd0+2+5*12, 325, 4, 1, 8)
	le.PutUint32(b[ifd0+2+6*12:], ifd1) // next → IFD1
	// IFD1(label 页):256/257/270/273/279
	le.PutUint16(b[ifd1:], 5)
	entry(ifd1+2+0*12, 256, 3, 1, 4)
	entry(ifd1+2+1*12, 257, 3, 1, 4)
	entry(ifd1+2+2*12, 270, 2, 6, dOff) // ImageDescription "label\0"
	entry(ifd1+2+3*12, 273, 4, 1, lOff) // StripOffsets
	entry(ifd1+2+4*12, 279, 4, 1, lLen) // StripByteCounts
	le.PutUint32(b[ifd1+2+5*12:], 0) // next → 0
	return b, tOff, lOff, lLen
}

func TestStripAssociatedImages_Synthetic(t *testing.T) {
	data, tileOff, labelOff, labelLen := buildSyntheticSlide()

	// 前提:原文件确实被识别成"有 label 页"（否则测的是空气）。
	pre, err := ParseSlideMeta(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("原文件应可解析: %v", err)
	}
	if !pre.HasLabelImage {
		t.Fatalf("夹具应带 label 页(HasLabelImage=true),否则本测试无意义")
	}

	var out bytes.Buffer
	removed, err := StripAssociatedImages(bytes.NewReader(data), int64(len(data)), &out)
	if err != nil {
		t.Fatalf("StripAssociatedImages: %v", err)
	}
	stripped := out.Bytes()

	if len(removed) != 1 || removed[0] != "label" {
		t.Fatalf("应剥掉 [label],得到 %v", removed)
	}
	if len(stripped) != len(data) {
		t.Fatalf("剥离是原地改链+抹像素,大小应不变: %d vs %d", len(stripped), len(data))
	}
	// ① 重探针:label 页已从链上消失。
	post, err := ParseSlideMeta(bytes.NewReader(stripped))
	if err != nil {
		t.Fatalf("剥离后仍应可解析: %v", err)
	}
	if post.HasLabelImage {
		t.Fatalf("剥离后 HasLabelImage 仍为 true(改链没生效?)")
	}
	if len(post.Levels) != len(pre.Levels) {
		t.Fatalf("金字塔层数变了: %d → %d(误删了层?)", len(pre.Levels), len(post.Levels))
	}
	// ② 金字塔瓦片数据逐字节不动。
	if string(stripped[tileOff:tileOff+8]) != "TILEDATA" {
		t.Fatalf("瓦片数据被动过了: %q", stripped[tileOff:tileOff+8])
	}
	// ③ label 像素被物理抹 0(PHI 不只是隐藏,是消失)。
	for i := labelOff; i < labelOff+labelLen; i++ {
		if stripped[i] != 0 {
			t.Fatalf("label 像素第 %d 字节未抹 0: 0x%02x", i-labelOff, stripped[i])
		}
	}
}

// 真实 CMU-1.svs:有 label+macro,且能用 ExtractTile 逐字节对拍(金字塔真没被碰)。
func TestStripAssociatedImages_RealSlide(t *testing.T) {
	path := os.Getenv("MM_TEST_WSI")
	if path == "" {
		t.Skip("设 MM_TEST_WSI 指向一张带 label/macro 的真实 SVS(如 CMU-1.svs)才跑——不硬编码机器路径(公开仓库不留 E:\\ 之类本机路径)。CI 无此文件,跳过是预期,不是绿。")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("打不开 %s:%v", path, err)
	}
	defer f.Close()
	fi, _ := f.Stat()

	pre, err := ParseSlideMeta(f)
	if err != nil {
		t.Fatalf("探针: %v", err)
	}
	if !pre.HasLabelImage {
		t.Skipf("跳过:%s 没有 label/macro,测不了剥离", path)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "stripped-*.svs")
	if err != nil {
		t.Fatal(err)
	}
	defer tmp.Close()
	removed, err := StripAssociatedImages(f, fi.Size(), tmp)
	if err != nil {
		t.Fatalf("StripAssociatedImages: %v", err)
	}
	t.Logf("剥掉: %v", removed)
	if len(removed) == 0 {
		t.Fatalf("应剥掉至少一张附属图")
	}

	// 重探针:label/macro 都没了,金字塔与标尺不变。
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	post, err := ParseSlideMeta(tmp)
	if err != nil {
		t.Fatalf("剥离后探针: %v", err)
	}
	if post.HasLabelImage {
		t.Fatalf("剥离后仍报 HasLabelImage")
	}
	if len(post.Levels) != len(pre.Levels) {
		t.Fatalf("层数变了 %d → %d", len(pre.Levels), len(post.Levels))
	}
	if post.MPP != pre.MPP {
		t.Fatalf("MPP 变了 %v → %v", pre.MPP, post.MPP)
	}

	// 金字塔瓦片逐字节对拍(最强证据:剥离没碰金字塔)。取每层左上角几块。
	for lvl := 0; lvl < len(pre.Levels); lvl++ {
		for _, cr := range [][2]int{{0, 0}, {1, 0}, {0, 1}} {
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			a, ea := ExtractTile(f, lvl, cr[0], cr[1])
			if _, err := tmp.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			b, eb := ExtractTile(tmp, lvl, cr[0], cr[1])
			if (ea == nil) != (eb == nil) {
				t.Fatalf("层 %d 瓦片(%d,%d) 取瓦片错况不一致: %v vs %v", lvl, cr[0], cr[1], ea, eb)
			}
			if ea == nil && !bytes.Equal(a, b) {
				t.Fatalf("层 %d 瓦片(%d,%d) 剥离前后不一致(金字塔被动过!)", lvl, cr[0], cr[1])
			}
		}
	}
}

// buildSyntheticBigTIFFInlineLabel 造一个 BigTIFF(8 字节偏移):一个 16×16 tiled 层 +
// 一个 4×4 striped "label" 页,label 的 ImageDescription **内联**在 8 字节值域里
// ("label\0" 6 字节 ≤ 8)。这是 #7a 的确切形态:合法 BigTIFF 完全可以这么存,而经典
// TIFF(值域 4 字节)塞不下 "label"——所以这个洞只在 BigTIFF 上出现,真实临床切片正是 BigTIFF。
func buildSyntheticBigTIFFInlineLabel() []byte {
	const (
		tileOff  = 16
		labelOff = 24
		labelLen = 16
		ifd0     = 40
		ifd1     = 176
		total    = 292
	)
	b := make([]byte, total)
	le := binary.LittleEndian
	copy(b[0:2], "II")
	le.PutUint16(b[2:4], 43) // BigTIFF magic
	le.PutUint16(b[4:6], 8)  // offset size
	le.PutUint16(b[6:8], 0)  // reserved
	le.PutUint64(b[8:16], ifd0)
	copy(b[tileOff:tileOff+8], "TILEDAT8")
	for i := labelOff; i < labelOff+labelLen; i++ {
		b[i] = 0xAB // "PHI" 像素
	}
	// BigTIFF entry: [tag u16][type u16][count u64][value 8B];返回那 8 字节值域切片去填。
	entry := func(pos int, tag, typ uint16, count uint64) []byte {
		le.PutUint16(b[pos:], tag)
		le.PutUint16(b[pos+2:], typ)
		le.PutUint64(b[pos+4:], count)
		return b[pos+12 : pos+20]
	}
	// IFD0:tiled 层(6 项)—— 保留。
	le.PutUint64(b[ifd0:], 6)
	e := ifd0 + 8
	le.PutUint16(entry(e+0*20, 256, 3, 1), 16)
	le.PutUint16(entry(e+1*20, 257, 3, 1), 16)
	le.PutUint16(entry(e+2*20, 322, 3, 1), 16)
	le.PutUint16(entry(e+3*20, 323, 3, 1), 16)
	le.PutUint32(entry(e+4*20, 324, 4, 1), tileOff)
	le.PutUint32(entry(e+5*20, 325, 4, 1), 8)
	le.PutUint64(b[e+6*20:], ifd1) // next → IFD1
	// IFD1:label 页(5 项),ImageDescription 内联 "label\0"(6B ≤ 8B 值域)。
	le.PutUint64(b[ifd1:], 5)
	f := ifd1 + 8
	le.PutUint16(entry(f+0*20, 256, 3, 1), 4)
	le.PutUint16(entry(f+1*20, 257, 3, 1), 4)
	copy(entry(f+2*20, 270, 2, 6), "label\x00") // 内联描述
	le.PutUint32(entry(f+3*20, 273, 4, 1), labelOff) // StripOffsets
	le.PutUint32(entry(f+4*20, 279, 4, 1), labelLen) // StripByteCounts
	le.PutUint64(b[f+5*20:], 0) // next → 0
	return b
}

// #7a:内联 desc 的 label 必须被识别并剥掉。修复前 readTiffDesc 对内联描述返回空 →
// 这张 label 分类不出来、被当缩略图保留、PHI 泄露且不报错。变异:把 readTiffDesc 的
// 内联分支改回返回 "",本测试立刻红(removed 变空)。
func TestStripAssociatedImages_InlineDescLabel(t *testing.T) {
	data := buildSyntheticBigTIFFInlineLabel()
	var out bytes.Buffer
	removed, err := StripAssociatedImages(bytes.NewReader(data), int64(len(data)), &out)
	if err != nil {
		t.Fatalf("StripAssociatedImages: %v", err)
	}
	if len(removed) != 1 || removed[0] != "label" {
		t.Fatalf("内联 desc 的 label 应被剥掉,得到 %v(修复前这里是空——PHI 漏网)", removed)
	}
	b := out.Bytes()
	for i := 24; i < 24+16; i++ {
		if b[i] != 0 {
			t.Fatalf("label 像素第 %d 字节未抹 0: 0x%02x", i-24, b[i])
		}
	}
}

// #8:剥离是 PHI 移除,任何解析异常都必须 fail-closed(报错),绝不能"看着成功、其实把
// PHI 留在文件里"。每个子例都构造一种损坏:修复前它们要么静默返回 removed=[label] 却没
// 抹像素、要么拿走到一半的链当成功、要么按越界长度分配天量内存。
func TestStripAssociatedImages_FailClosedOnCorrupt(t *testing.T) {
	le := binary.LittleEndian
	const ifd1 = 116 // 与 buildSyntheticSlide 的 classic 布局一致

	t.Run("strip 数组数量不等→拒绝", func(t *testing.T) {
		data, _, _, _ := buildSyntheticSlide()
		// label 的 StripByteCounts(279)count:1 → 2,使 soCount≠sbCount。
		// 修复前:soCount==sbCount 不成立 → 整段 strip 读取被跳过 → 像素没抹,却仍
		// 返回 removed=[label] 报成功(PHI 物理残留、可取证恢复)。
		le.PutUint32(data[ifd1+2+4*12+4:], 2)
		var out bytes.Buffer
		if _, err := StripAssociatedImages(bytes.NewReader(data), int64(len(data)), &out); err == nil {
			t.Fatal("strip 数量不等必须报错(fail-closed),不能静默成功把 PHI 留下")
		}
	})

	t.Run("越界 ByteCount→拒绝且不 OOM", func(t *testing.T) {
		data, _, _, _ := buildSyntheticSlide()
		// StripOffsets 挪到接近文件尾(170)、StripByteCounts 设成远超剩余长度(5000):
		// strip 范围越界 EOF。放到链尾之后是刻意的——这样"越界判定"是唯一防线,不会被
		// 改链补丁的重叠检测顺手挡掉,本条真正咬的是 bounds 守卫本身(变异该守卫即变红)。
		// 越界的危害有二:① 修复前 make([]byte, 5000)(极端值可达 GB)按越界长度分配→OOM;
		// ② 把抹零补丁写到文件范围外。两者都由这条 bounds 检查在建补丁前挡住。
		le.PutUint32(data[ifd1+2+3*12+8:], 170)  // StripOffsets → 170(近尾)
		le.PutUint32(data[ifd1+2+4*12+8:], 5000) // StripByteCounts → 5000(越界)
		var out bytes.Buffer
		if _, err := StripAssociatedImages(bytes.NewReader(data), int64(len(data)), &out); err == nil {
			t.Fatal("strip 越界必须报错,不能按越界长度去抹/分配")
		}
	})

	t.Run("链在 next 指针处截断→拒绝", func(t *testing.T) {
		data, _, _, _ := buildSyntheticSlide()
		// 砍到 label IFD 的 entries 刚好读完、"下一个 IFD 偏移"字段(178..182)缺失。
		// 修复前:读 next 指针 EOF → `return out, nil` 且**不 append 当前 IFD** →
		// label 根本没进处理列表 → removed=[]、不抹像素、报成功。PHI 整张留下。
		trunc := data[:178]
		var out bytes.Buffer
		if _, err := StripAssociatedImages(bytes.NewReader(trunc), int64(len(trunc)), &out); err == nil {
			t.Fatal("链截断必须报错,不能拿走到一半的 IFD 链当成功")
		}
	})
}
