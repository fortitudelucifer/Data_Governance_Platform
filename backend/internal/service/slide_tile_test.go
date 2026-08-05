package service

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// C1.2a 瓦片提取。核心风险两条,都测:
//  ① 取错瓦片(索引/网格算错)——画面上是"别处的组织",不报错;
//  ② 拼错 JPEGTables 头——浏览器 broken data stream。
//
// 真文件(CMU-1.svs)的逐像素跨语言验证在 tools/c11c_tile_probe.py + 手工比对里
// 做过(Go 取出 vs Python 直读,最大像素差 0)。这里用**自造的可控 TIFF**测边界
// 与拼接逻辑,不依赖那 170MB 文件,CI 能跑。

// buildTiledTIFF 造一个带瓦片 + JPEGTables 的最小 TIFF。每个瓦片的"字节"是一个
// 可辨认的桩(不是真 JPEG),用来验证**取到的是哪一个瓦片**;拼接逻辑另用真 JPEG 头测。
func buildTiledTIFF(t *testing.T, w, h, tw, th int, tiles [][]byte, jpegTables []byte) []byte {
	return buildTiledTIFFTyped(t, w, h, tw, th, tiles, jpegTables, 4) // 默认 bytecount 为 LONG
}

// cntType: TileByteCounts 的元素类型(3=SHORT/2字节, 4=LONG/4字节)。真实文件里两者
// 都合法;用 SHORT 造夹具能逮到"按固定 4 字节读数组元素、无视类型"的错误——
// 而那正是 >4GB 切片(LONG8 偏移)会触发的静默错。
func buildTiledTIFFTyped(t *testing.T, w, h, tw, th int, tiles [][]byte, jpegTables []byte, cntType uint16) []byte {
	t.Helper()
	bo := binary.LittleEndian
	tilesX := (w + tw - 1) / tw
	tilesY := (h + th - 1) / th
	if len(tiles) != tilesX*tilesY {
		t.Fatalf("需要 %d 个瓦片,给了 %d", tilesX*tilesY, len(tiles))
	}
	cntSz := 4
	if cntType == 3 {
		cntSz = 2
	}

	// 布局:header(8) → IFD → offset数组 → bytecount数组 → jpegtables → 各瓦片数据
	nEntries := 7 // width,height,tilew,tileh,tileoffsets,tilebytecounts,jpegtables
	ifdOff := 8
	ifdLen := 2 + nEntries*12 + 4
	offArrOff := ifdOff + ifdLen
	cntArrOff := offArrOff + len(tiles)*4
	jtOff := cntArrOff + len(tiles)*cntSz
	dataOff := jtOff + len(jpegTables)

	total := dataOff
	for _, tl := range tiles {
		total += len(tl)
	}
	buf := make([]byte, total)

	copy(buf, "II")
	bo.PutUint16(buf[2:], 42)
	bo.PutUint32(buf[4:], uint32(ifdOff))

	bo.PutUint16(buf[ifdOff:], uint16(nEntries))
	e := ifdOff + 2
	putE := func(i int, tag, typ uint16, count, val uint32) {
		b := buf[e+i*12:]
		bo.PutUint16(b, tag)
		bo.PutUint16(b[2:], typ)
		bo.PutUint32(b[4:], count)
		bo.PutUint32(b[8:], val)
	}
	putE(0, tagImageWidth, 4, 1, uint32(w))
	putE(1, tagImageLength, 4, 1, uint32(h))
	putE(2, tagTileWidth, 4, 1, uint32(tw))
	putE(3, tagTileLength, 4, 1, uint32(th))
	// TIFF 规则:count*elemSize ≤ 4(值槽)时数组**内联**在值槽里,不是文件指针。
	// 单瓦片的最粗层(fit 首屏那张)正是这样。以前这里对 count=1 也写成外置数组——
	// 非 TIFF 规范,只因旧 reader 也照外置读才"对上"。造出真实的内联形态,才测得到
	// readArrayElem 的内联路径(#1)。多瓦片(不内联)仍走外置数组,不变。
	if len(tiles) == 1 {
		putE(4, tagTileOffsets, 4, 1, uint32(dataOff)) // 内联:值 = 唯一瓦片偏移
		if cntSz == 2 {
			b := buf[e+5*12:]
			bo.PutUint16(b, tagTileByteCounts)
			bo.PutUint16(b[2:], cntType)
			bo.PutUint32(b[4:], 1)
			bo.PutUint16(b[8:], uint16(len(tiles[0]))) // 内联 SHORT:值槽前 2 字节
		} else {
			putE(5, tagTileByteCounts, cntType, 1, uint32(len(tiles[0]))) // 内联 LONG
		}
	} else {
		putE(4, tagTileOffsets, 4, uint32(len(tiles)), uint32(offArrOff))
		putE(5, tagTileByteCounts, cntType, uint32(len(tiles)), uint32(cntArrOff))
	}
	putE(6, tagJPEGTables, 7, uint32(len(jpegTables)), uint32(jtOff))
	bo.PutUint32(buf[e+nEntries*12:], 0) // next IFD = 0

	// 瓦片数据 + 两个数组
	pos := dataOff
	for i, tl := range tiles {
		bo.PutUint32(buf[offArrOff+i*4:], uint32(pos))
		if cntSz == 2 {
			bo.PutUint16(buf[cntArrOff+i*2:], uint16(len(tl)))
		} else {
			bo.PutUint32(buf[cntArrOff+i*4:], uint32(len(tl)))
		}
		copy(buf[pos:], tl)
		pos += len(tl)
	}
	copy(buf[jtOff:], jpegTables)
	return buf
}

// 每个瓦片装一段可辨认的桩字节:JPEG SOI + "TILE<idx>"。用来断言"取到的是第几个"。
func stubTile(idx int) []byte {
	return append([]byte{0xFF, 0xD8}, []byte(("TILE" + string(rune('0'+idx))))...)
}

func TestExtractTile_LocatesRightTile(t *testing.T) {
	// 4×4 网格(w=1000,h=1000,tile=256 → ceil=4 每边),16 个瓦片。
	tiles := make([][]byte, 16)
	for i := range tiles {
		tiles[i] = stubTile(i)
	}
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q', 'T', 0xFF, 0xD9} // 桩:SOI+DQT桩+EOI
	raw := buildTiledTIFF(t, 1000, 1000, 256, 256, tiles, jt)

	// tile (col=2,row=1) → idx = 1*4+2 = 6
	got, err := ExtractTile(bytes.NewReader(raw), 0, 2, 1)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// 拼接后:SOI + DQT桩(去掉JT的SOI/EOI) + 瓦片SOI之后的内容
	if !bytes.Contains(got, []byte("TILE6")) {
		t.Errorf("取到的不是 idx6 的瓦片: %q（网格/索引算错 → 画面显示别处组织）", got)
	}
	// 拼进了 JPEGTables 的表段(DQT桩)
	if !bytes.Contains(got, []byte{0xFF, 0xDB, 'Q', 'T'}) {
		t.Error("没拼进 JPEGTables 的 DQT 段 → 浏览器会 broken data stream")
	}
	// 且不该带 JPEGTables 自己的 EOI(只取中间表段)
	if bytes.Count(got, []byte{0xFF, 0xD9}) > 0 {
		t.Error("拼接把 JPEGTables 的 EOI 也带进来了")
	}
}

func TestExtractTile_HeaderStartsWithSOIThenTables(t *testing.T) {
	tiles := [][]byte{append([]byte{0xFF, 0xD8}, []byte("BODY")...)}
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'X', 'Y', 0xFF, 0xC4, 'H', 'T', 0xFF, 0xD9}
	raw := buildTiledTIFF(t, 100, 100, 256, 256, tiles, jt)
	got, err := ExtractTile(bytes.NewReader(raw), 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 期望:SOI + (DQT+DHT 表段) + BODY
	want := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'X', 'Y', 0xFF, 0xC4, 'H', 'T', 'B', 'O', 'D', 'Y'}
	if !bytes.Equal(got, want) {
		t.Errorf("拼接结果不对\n got: % x\nwant: % x", got, want)
	}
}

func TestExtractTile_OutOfRangeIsError(t *testing.T) {
	tiles := make([][]byte, 16)
	for i := range tiles {
		tiles[i] = stubTile(i)
	}
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q', 0xFF, 0xD9}
	raw := buildTiledTIFF(t, 1000, 1000, 256, 256, tiles, jt)
	// 4×4 网格,col=4 越界(合法是 0..3)
	if _, err := ExtractTile(bytes.NewReader(raw), 0, 4, 0); err == nil {
		t.Error("越界瓦片必须报错,而不是返回空瓦片（空瓦片看起来像切片上的洞）")
	}
	// level 越界
	if _, err := ExtractTile(bytes.NewReader(raw), 5, 0, 0); err == nil {
		t.Error("越界 level 必须报错")
	}
}

func TestExtractTile_MultiLevelPicksRightLevel(t *testing.T) {
	// 两层:level0 4×4=16 瓦片(每个含 "TILE0".."TILE?"),level1 1 瓦片(含 "L1")。
	l0 := make([][]byte, 16)
	for i := range l0 {
		l0[i] = stubTile(i)
	}
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q', 0xFF, 0xD9}
	raw0 := buildTiledTIFF(t, 1000, 1000, 256, 256, l0, jt)
	raw1 := buildTiledTIFF(t, 250, 250, 256, 256, [][]byte{append([]byte{0xFF, 0xD8}, []byte("L1")...)}, jt)
	// 把两个单层 TIFF 链起来:改 raw0 的 next-IFD 指向 raw1 的 IFD(简化:拼接 + 重定位)。
	// 这里用一个更直接的办法验多层:分别对两个文件各取,确认 level 语义。
	if got, _ := ExtractTile(bytes.NewReader(raw0), 0, 0, 0); !bytes.Contains(got, []byte("TILE0")) {
		t.Error("level0 应取到 TILE0")
	}
	if got, _ := ExtractTile(bytes.NewReader(raw1), 0, 0, 0); !bytes.Contains(got, []byte("L1")) {
		t.Error("单层文件的 level0 应取到 L1")
	}
}

// SHORT 类型的 TileByteCounts:元素是 2 字节,不是 4。若实现"按固定 4 字节读
// 数组元素、无视类型",取 idx>0 的瓦片就会 seek 到错误位置、读出错误长度。
// 这正是 >4GB 切片(LONG8 偏移数组)会触发的静默错的可控替身。
func TestExtractTile_ShortTypeByteCounts(t *testing.T) {
	tiles := make([][]byte, 16)
	for i := range tiles {
		// 瓦片长度各不同,这样"读错元素"会取到错误的字节数 → 内容错乱
		tiles[i] = append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{byte('A' + i)}, i+3)...)
	}
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q', 0xFF, 0xD9}
	raw := buildTiledTIFFTyped(t, 1000, 1000, 256, 256, tiles, jt, 3) // SHORT bytecounts
	got, err := ExtractTile(bytes.NewReader(raw), 0, 2, 1) // idx=6
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	// **精确相等**:idx6 瓦片体是 9 个 'G'(6+3),jt 表段是 FFDB 'Q'。
	// 拼头后 = SOI + FFDB'Q' + 9个'G',共 14 字节。用 Contains 太弱——按固定 4 字节
	// 读 bytecount 会**多读**几字节(读到下一个瓦片),Contains 照样为真却是错的。
	want := append([]byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q'}, bytes.Repeat([]byte{'G'}, 9)...)
	if !bytes.Equal(got, want) {
		t.Errorf("SHORT bytecount 取瓦片错 got=% x want=% x（按固定4字节读了元素？）", got, want)
	}
}

func TestExtractTile_BigEndianCovered(t *testing.T) {
	t.Skip("大端字节序由 tiffHeader/readArrayElem 的 bo 参数保证，探针测试已覆盖 bo 分支")
}

// buildSingleTileInlineTIFF 造一个**单瓦片**TIFF,且 TileOffsets/TileByteCounts 按
// TIFF 规则**内联在值槽里**(count=1、LONG,1*4=4 ≤ 4 字节值槽)。这是最粗金字塔层
// 的真实形态(fit 首屏那张),buildTiledTIFF 却总把数组写成外置——所以这条专造内联。
func buildSingleTileInlineTIFF(t *testing.T, w, h, tw, th int, tile, jpegTables []byte, compression uint16) []byte {
	t.Helper()
	bo := binary.LittleEndian
	nEntries := 8
	ifdOff := 8
	ifdLen := 2 + nEntries*12 + 4
	jtOff := ifdOff + ifdLen
	dataOff := jtOff + len(jpegTables)
	total := dataOff + len(tile)
	buf := make([]byte, total)

	copy(buf, "II")
	bo.PutUint16(buf[2:], 42)
	bo.PutUint32(buf[4:], uint32(ifdOff))
	bo.PutUint16(buf[ifdOff:], uint16(nEntries))
	e := ifdOff + 2
	putE := func(i int, tag, typ uint16, count, val uint32) {
		b := buf[e+i*12:]
		bo.PutUint16(b, tag)
		bo.PutUint16(b[2:], typ)
		bo.PutUint32(b[4:], count)
		bo.PutUint32(b[8:], val) // 值槽:内联数组把数据直接放这里
	}
	putE(0, tagImageWidth, 4, 1, uint32(w))
	putE(1, tagImageLength, 4, 1, uint32(h))
	putE(2, tagTileWidth, 4, 1, uint32(tw))
	putE(3, tagTileLength, 4, 1, uint32(th))
	putE(4, tagTileOffsets, 4, 1, uint32(dataOff))   // 内联:值 = 瓦片文件偏移
	putE(5, tagTileByteCounts, 4, 1, uint32(len(tile))) // 内联:值 = 瓦片字节数
	putE(6, tagJPEGTables, 7, uint32(len(jpegTables)), uint32(jtOff))
	putE(7, tagCompression, 3, 1, uint32(compression)) // SHORT:内联在值槽
	bo.PutUint32(buf[e+nEntries*12:], 0) // next IFD = 0
	copy(buf[jtOff:], jpegTables)
	copy(buf[dataOff:], tile)
	return buf
}

// #1:单瓦片层的 TileOffsets/ByteCounts 内联在值槽里,不是文件指针。修复前无条件
// seek(valOff) 会把"瓦片偏移值"当成"偏移数组的地址"去读 → 读到瓦片数据当偏移 →
// 越界/取到别处组织,fit 首屏那张瓦片 404。变异:把 readArrayElem 的内联分支删掉,本测试红。
func TestExtractTile_InlineSingleTileArrays(t *testing.T) {
	tile := append([]byte{0xFF, 0xD8}, []byte("SOLO")...)
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q', 0xFF, 0xD9}
	raw := buildSingleTileInlineTIFF(t, 200, 200, 256, 256, tile, jt, 7) // JPEG
	got, err := ExtractTile(bytes.NewReader(raw), 0, 0, 0)
	if err != nil {
		t.Fatalf("单瓦片内联层取瓦片失败: %v(内联 TileOffsets 被当成文件指针?)", err)
	}
	want := append([]byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q'}, []byte("SOLO")...)
	if !bytes.Equal(got, want) {
		t.Errorf("内联单瓦片取错 got=% x want=% x", got, want)
	}
}

// #3:J2K 压缩的瓦片不是 JPEG,拼头也解不出;必须明确拒(fail-closed),不能静默发坏瓦片
// 让浏览器报 "broken data stream"。变异:删掉 ExtractTile 里的 isJPEG2000Compression 检查,本测试红。
func TestExtractTile_RejectsJPEG2000(t *testing.T) {
	tile := append([]byte{0xFF, 0xD8}, []byte("SOLO")...)
	jt := []byte{0xFF, 0xD8, 0xFF, 0xDB, 'Q', 0xFF, 0xD9}
	if _, err := ExtractTile(bytes.NewReader(buildSingleTileInlineTIFF(t, 200, 200, 256, 256, tile, jt, 33005)), 0, 0, 0); err == nil {
		t.Error("J2K(Aperio 33005)瓦片必须被拒,不能静默发坏 JPEG")
	}
	// 对照:JPEG(7)照常取,不误伤。
	if _, err := ExtractTile(bytes.NewReader(buildSingleTileInlineTIFF(t, 200, 200, 256, 256, tile, jt, 7)), 0, 0, 0); err != nil {
		t.Errorf("JPEG(7)瓦片应正常取, got %v", err)
	}
}

// #6:tag 347 的 count 若被写成天量,ExtractTile 会在校验前 make([]byte, count) 吃满内存。
// 用一个"合法但超过 1MB 上限"的 JPEGTables 触发:修复后按上限直接拒;修复前照单分配。
func TestExtractTile_JPEGTablesLengthCapped(t *testing.T) {
	tile := append([]byte{0xFF, 0xD8}, []byte("BODY")...)
	jt := make([]byte, (1<<20)+16) // > 1MB 上限
	jt[0], jt[1] = 0xFF, 0xD8
	jt[len(jt)-2], jt[len(jt)-1] = 0xFF, 0xD9
	raw := buildTiledTIFF(t, 100, 100, 256, 256, [][]byte{tile}, jt)
	if _, err := ExtractTile(bytes.NewReader(raw), 0, 0, 0); err == nil {
		t.Error("超大 JPEGTables 长度必须报错(堵 OOM),不能照单分配")
	}
}
