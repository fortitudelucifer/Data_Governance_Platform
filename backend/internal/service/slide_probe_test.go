package service

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

// C1.1a WSI 探针。
//
// 真实样本(CMU-1-Small-Region.svs)是**经典 TIFF**,而真实的大切片几乎都是
// **BigTIFF**——所以最危险的那条路径恰恰是样本覆盖不到的:用经典 TIFF 的 4 字节
// 偏移去读 BigTIFF,会读出"看起来完全合理但是错的"尺寸,不报任何错。
// 因此这里**自己造** BigTIFF / 大端 / 非瓦片页 等结构来测,而不是只跑真文件。

// ── 最小 TIFF 构造器 ────────────────────────────────────────────────────────
// 只造探针会读的东西:头 + 若干 IFD(每个带 width/height/tile/description)。

type tiffEntry struct {
	tag  uint16
	typ  uint16 // 3=SHORT 4=LONG 2=ASCII
	val  uint64
	data []byte // ASCII 时的外置数据
}

type tiffPage struct {
	w, h, tw, th int
	desc         string
}

// buildTIFF 造一个可解析的 TIFF。big=BigTIFF，be=大端。
func buildTIFF(t *testing.T, pages []tiffPage, big, be bool) []byte {
	t.Helper()
	var bo binary.ByteOrder = binary.LittleEndian
	order := "II"
	if be {
		bo, order = binary.BigEndian, "MM"
	}

	// 先算布局：头 → 各 IFD → 各 description 数据。
	hdrLen := 8
	if big {
		hdrLen = 16
	}
	entrySize, cntSize, offSize := 12, 2, 4
	if big {
		entrySize, cntSize, offSize = 20, 8, 8
	}

	// 每页 5 个 tag（width/height/tilew/tileh/desc）
	nEntries := 5
	ifdLen := cntSize + nEntries*entrySize + offSize

	ifdOff := make([]int, len(pages))
	pos := hdrLen
	for i := range pages {
		ifdOff[i] = pos
		pos += ifdLen
	}
	descOff := make([]int, len(pages))
	for i, p := range pages {
		descOff[i] = pos
		pos += len(p.desc)
	}

	buf := make([]byte, pos)
	copy(buf, order)
	if big {
		bo.PutUint16(buf[2:], 43)
		bo.PutUint16(buf[4:], 8) // offset size
		bo.PutUint16(buf[6:], 0)
		bo.PutUint64(buf[8:], uint64(ifdOff[0]))
	} else {
		bo.PutUint16(buf[2:], 42)
		bo.PutUint32(buf[4:], uint32(ifdOff[0]))
	}

	putEntry := func(b []byte, e tiffEntry, count uint64) {
		bo.PutUint16(b[0:], e.tag)
		bo.PutUint16(b[2:], e.typ)
		if big {
			bo.PutUint64(b[4:], count)
			bo.PutUint64(b[12:], e.val)
		} else {
			bo.PutUint32(b[4:], uint32(count))
			if e.typ == 3 {
				// SHORT 内联在值域**前两字节**（经典 TIFF 的对齐规则）
				bo.PutUint16(b[8:], uint16(e.val))
				bo.PutUint16(b[10:], 0)
			} else {
				bo.PutUint32(b[8:], uint32(e.val))
			}
		}
	}

	for i, p := range pages {
		b := buf[ifdOff[i]:]
		if big {
			bo.PutUint64(b, uint64(nEntries))
		} else {
			bo.PutUint16(b, uint16(nEntries))
		}
		e := b[cntSize:]
		putEntry(e[0*entrySize:], tiffEntry{tag: tagImageWidth, typ: 4, val: uint64(p.w)}, 1)
		putEntry(e[1*entrySize:], tiffEntry{tag: tagImageLength, typ: 4, val: uint64(p.h)}, 1)
		putEntry(e[2*entrySize:], tiffEntry{tag: tagTileWidth, typ: 4, val: uint64(p.tw)}, 1)
		putEntry(e[3*entrySize:], tiffEntry{tag: tagTileLength, typ: 4, val: uint64(p.th)}, 1)
		putEntry(e[4*entrySize:], tiffEntry{tag: tagImageDesc, typ: 2, val: uint64(descOff[i])}, uint64(len(p.desc)))
		copy(buf[descOff[i]:], p.desc)

		// next IFD
		nxt := 0
		if i+1 < len(pages) {
			nxt = ifdOff[i+1]
		}
		tail := b[cntSize+nEntries*entrySize:]
		if big {
			bo.PutUint64(tail, uint64(nxt))
		} else {
			bo.PutUint32(tail, uint32(nxt))
		}
	}
	return buf
}

const aperioDesc = "Aperio Image Library v11.2.1 \r\n46000x32914 [0,0 2220x2967] (240x240) JPEG/RGB Q=30|AppMag = 20|MPP = 0.4990|ScanScope ID = X"

func TestSlideProbe_ClassicTIFF(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{{w: 2220, h: 2967, tw: 240, th: 240, desc: aperioDesc}}, false, false)
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Width != 2220 || m.Height != 2967 {
		t.Errorf("尺寸 = %dx%d, want 2220x2967", m.Width, m.Height)
	}
	if m.MPP != 0.4990 || m.Magnification != 20 {
		t.Errorf("mpp=%v mag=%v, want 0.499 / 20", m.MPP, m.Magnification)
	}
	if len(m.Levels) != 1 || m.Levels[0].TileWidth != 240 {
		t.Errorf("层信息不对: %+v", m.Levels)
	}
}

// **这条是核心**：真实大切片是 BigTIFF，而样本是经典 TIFF。用经典偏移读
// BigTIFF 会产出"看起来合理但错的"尺寸且不报错。两种必须给出同一结果。
func TestSlideProbe_BigTIFFSameAsClassic(t *testing.T) {
	pages := []tiffPage{
		{w: 46000, h: 32914, tw: 256, th: 256, desc: aperioDesc},
		{w: 11500, h: 8228, tw: 256, th: 256, desc: "level 1"},
	}
	classic, err := ParseSlideMeta(bytes.NewReader(buildTIFF(t, pages, false, false)))
	if err != nil {
		t.Fatalf("classic: %v", err)
	}
	bigt, err := ParseSlideMeta(bytes.NewReader(buildTIFF(t, pages, true, false)))
	if err != nil {
		t.Fatalf("bigtiff: %v", err)
	}
	if classic.Width != bigt.Width || classic.Height != bigt.Height {
		t.Fatalf("BigTIFF 与经典 TIFF 结果不同: %dx%d vs %dx%d（BigTIFF 偏移没按 8 字节读？）",
			classic.Width, classic.Height, bigt.Width, bigt.Height)
	}
	if len(bigt.Levels) != 2 {
		t.Fatalf("BigTIFF 应解出 2 层，得 %d", len(bigt.Levels))
	}
	if got := bigt.Levels[1].Downsample; got < 3.9 || got > 4.1 {
		t.Errorf("第 1 层 downsample = %.2f, want ≈4", got)
	}
}

// 大端 BigTIFF：**这条专治"首个 IFD 偏移只读了 4 字节"**。
// 小端下 uint64(16) 的低 4 字节就是 16，读 4 或 8 字节结果相同，变异测不出来；
// 大端下高位在前，读 4 字节会得到 0 —— 于是差别立刻显形。
// 真实风险不是这个小文件，而是 10GB 切片把 IFD0 放在 4GB 之后时会读出垃圾。
func TestSlideProbe_BigTIFFOffsetIsFullEightBytes(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{{w: 4096, h: 4096, tw: 256, th: 256, desc: aperioDesc}}, true, true)
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("大端 BigTIFF 解析失败（首 IFD 偏移是不是只读了 4 字节？）: %v", err)
	}
	if m.Width != 4096 || m.Height != 4096 {
		t.Errorf("尺寸 %dx%d, want 4096x4096", m.Width, m.Height)
	}
}

// 只有 label、没有 macro 的切片，PHI 标志同样必须置上。
// （原测试同时放了 label 和 macro，于是"label 分支不置标志"这个变异被 macro
// 分支掩盖了——变异测试逮到的空档。）
func TestSlideProbe_LabelOnlyStillFlagsPHI(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{
		{w: 2220, h: 2967, tw: 240, th: 240, desc: aperioDesc},
		{w: 387, h: 463, tw: 0, th: 0, desc: "Aperio label 387x463"},
	}, false, false)
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !m.HasLabelImage {
		t.Error("只有 label（无 macro）时 PHI 标志也必须置上——标签上印着病人标识")
	}
}

func TestSlideProbe_BigEndian(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{{w: 1024, h: 768, tw: 256, th: 256, desc: aperioDesc}}, false, true)
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Width != 1024 || m.Height != 768 {
		t.Errorf("大端解析错: %dx%d", m.Width, m.Height)
	}
}

// 非瓦片页 = 附属图（缩略图/标签/宏观），不能当成金字塔层。
// label/macro 上印着病人标识（PS3.15 烧录像素文字）→ 必须标记出来。
func TestSlideProbe_AssociatedImagesAndPHIFlag(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{
		{w: 2220, h: 2967, tw: 240, th: 240, desc: aperioDesc},
		{w: 574, h: 768, tw: 0, th: 0, desc: "Aperio Image Library\r\n2220x2967 -> 574x768"},
		{w: 387, h: 463, tw: 0, th: 0, desc: "Aperio Image Library\r\nlabel 387x463"},
		{w: 1280, h: 431, tw: 0, th: 0, desc: "Aperio Image Library\r\nmacro 1280x431"},
	}, false, false)
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Levels) != 1 {
		t.Errorf("只有 1 个瓦片页，却解出 %d 层——附属图被当成金字塔层了", len(m.Levels))
	}
	if !m.HasLabelImage {
		t.Error("有 label/macro 必须置 HasLabelImage（临床切片标签上印着病人标识）")
	}
	want := map[string]bool{"thumbnail": true, "label": true, "macro": true}
	for _, a := range m.Associated {
		delete(want, a)
	}
	if len(want) != 0 {
		t.Errorf("附属图识别不全，缺 %v（实际 %v）", want, m.Associated)
	}
}

// **mpp 缺失必须是 0=未知，绝不能默认 1.0**。
// 默认 1.0 会让每一次物理测量都错一个扫描倍率，而读数看起来完全正常——
// 这正是 C3.25 在 3D 测量里反复防的那类失败。
func TestSlideProbe_MissingMPPIsUnknownNotOne(t *testing.T) {
	raw := buildTIFF(t, []tiffPage{
		{w: 1000, h: 1000, tw: 256, th: 256, desc: "Generic Scanner v1\r\nno vendor keys here at all"},
	}, false, false)
	m, err := ParseSlideMeta(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.MPP != 0 {
		t.Errorf("缺 MPP 时应为 0（未知），得到 %v——默认成非零会让测量静默出错", m.MPP)
	}
	if m.Magnification != 0 {
		t.Errorf("缺 AppMag 时应为 0，得到 %v", m.Magnification)
	}
}

func TestSlideProbe_RejectsNonTIFF(t *testing.T) {
	if _, err := ParseSlideMeta(bytes.NewReader([]byte("not a tiff at all........"))); err == nil {
		t.Fatal("非 TIFF 必须报错")
	}
}

func TestSlideProbe_RejectsNoPyramid(t *testing.T) {
	// 全是非瓦片页 → 不是 WSI，必须终止式拒绝而不是返回一个空壳。
	raw := buildTIFF(t, []tiffPage{{w: 100, h: 100, tw: 0, th: 0, desc: "just a picture"}}, false, false)
	if _, err := ParseSlideMeta(bytes.NewReader(raw)); err == nil {
		t.Fatal("没有瓦片金字塔层必须报错")
	}
}

// 真文件交叉验证：值由 Python tifffile 独立读出（2220x2967 / 240 / 0.4990 / 20）。
// 文件不入库（1.9MB）；设 MM_TEST_WSI_SMALL 指向它才跑——不硬编码机器路径
// （公开仓库不留 E:\ 之类的本机路径）。CI 无此文件，跳过是预期。
func TestSlideProbe_RealSVSCrossCheck(t *testing.T) {
	p := os.Getenv("MM_TEST_WSI_SMALL")
	if p == "" {
		t.Skip("设 MM_TEST_WSI_SMALL 指向 CMU-1-Small-Region.svs 才跑（下载：openslide.cs.cmu.edu/download/openslide-testdata/Aperio/）。CI 无此文件，跳过是预期，不是绿。")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Skipf("打不开 %s：%v", p, err)
	}
	defer f.Close()
	m, err := ParseSlideMeta(f)
	if err != nil {
		t.Fatalf("parse real svs: %v", err)
	}
	if m.Width != 2220 || m.Height != 2967 {
		t.Errorf("尺寸 %dx%d, tifffile 独立读出的是 2220x2967", m.Width, m.Height)
	}
	if m.MPP != 0.4990 {
		t.Errorf("MPP %v, tifffile 读出 0.4990", m.MPP)
	}
	if m.Magnification != 20 {
		t.Errorf("AppMag %v, want 20", m.Magnification)
	}
	if m.Levels[0].TileWidth != 240 {
		t.Errorf("瓦片 %d, want 240", m.Levels[0].TileWidth)
	}
	if !m.HasLabelImage {
		t.Error("该文件含 label/macro，应置 PHI 标记")
	}
}
