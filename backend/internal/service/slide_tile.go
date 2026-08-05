package service

import (
	"encoding/binary"
	"fmt"
	"io"
)

// WSI tile extraction (C1.2a) — pull ONE tile by (level, col, row) and return a
// standalone-decodable JPEG, reading only a few KB regardless of slide size.
//
// # Why this is more than "read bytes at offset"
//
// Two facts about Aperio SVS, both established by real-sample measurement
// (tools/c11c_tile_probe.py on CMU-1.svs, 46000×32914):
//
//  1. **Tiles are "abbreviated JPEG".** The shared quantization/Huffman tables
//     live once in the file's JPEGTables tag (347), NOT in each tile. A raw
//     tile begins ffd8ffc0 with no DQT/DHT — a browser decoding it gets
//     "broken data stream". So every tile we serve must have the ~289-byte
//     table block spliced into its header. Zero re-encode, just a byte insert.
//  2. **The tile-offset array is huge but we need one entry.** Level 0 has
//     23,220 tiles, so TileOffsets/TileByteCounts are 23k-entry arrays. We seek
//     straight to entry [idx] and read a single element — never the whole array
//     — so memory is O(1) in the slide's tile count.
//
// The IFD is a linked list of offsets, so walking to the Nth tiled level and
// reading three tags is a handful of small seeks. This is the same discipline
// as ParseSlideMeta: gigapixel files, kilobyte reads.

// tiff array element sizes by TIFF type code.
var tiffTypeSize = map[uint16]int{
	1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 16: 8, 17: 8,
}

const (
	tagCompression    = 259
	tagTileOffsets    = 324
	tagTileByteCounts = 325
	tagJPEGTables     = 347
)

// isJPEG2000Compression 判定 TIFF Compression 是否 Aperio 的 JPEG2000 变体(#3)。
// 这些瓦片是 J2K codestream,不是 JPEG——拼 JPEGTables 头也解不出,浏览器报
// "broken data stream"。当前瓦片端点只支持 JPEG,遇到这些要明确拒而非静默发坏瓦片。
func isJPEG2000Compression(c uint64) bool { return c == 33003 || c == 33004 || c == 33005 }

// levelIFD holds the pointers needed to fetch a tile from one pyramid level.
type levelIFD struct {
	width, height       int
	tileW, tileH        int
	offArrOff, offType  uint64 // TileOffsets: array file-offset + element type
	cntArrOff, cntType  uint64 // TileByteCounts
	count               uint64 // tile count (both arrays)
	offVal, cntVal      []byte // 原始 4/8 字节值槽 —— 内联数组(单/少瓦片的最粗层)住在这里
	compression         uint64 // TIFF Compression (259):J2K 变体不受支持,拒发
	jpegTablesOff, jpegTablesLen uint64
}

// ExtractTile returns a standalone JPEG for the tile at (level, col, row).
//
// The returned bytes are directly decodable (JPEGTables spliced in) and are
// served to the browser as-is — no server-side decode. Out-of-range level/tile
// is an error, not a blank tile: a viewer asking for a tile that does not exist
// is a bug worth surfacing, and a silent empty tile would look like a hole in
// the slide.
func ExtractTile(rs io.ReadSeeker, level, col, row int) ([]byte, error) {
	bo, big, first, err := tiffHeader(rs)
	if err != nil {
		return nil, err
	}
	lv, err := walkToTiledLevel(rs, bo, big, first, level)
	if err != nil {
		return nil, err
	}
	// #3:J2K 瓦片不是 JPEG,拼头也解不出——与其静默发一堆"broken data stream"坏瓦片,
	// 不如明确拒(fail-closed)。真正支持 J2K 要接 openjpeg 转码,是后续欠账。
	if isJPEG2000Compression(lv.compression) {
		return nil, fmt.Errorf("此切片瓦片用 JPEG2000(compression %d)编码,当前瓦片端点只支持 JPEG,无法直出", lv.compression)
	}

	tilesX := (lv.width + lv.tileW - 1) / lv.tileW
	tilesY := (lv.height + lv.tileH - 1) / lv.tileH
	if col < 0 || row < 0 || col >= tilesX || row >= tilesY {
		return nil, fmt.Errorf("tile (%d,%d) out of range for level %d grid %dx%d", col, row, level, tilesX, tilesY)
	}
	idx := uint64(row*tilesX + col)
	if idx >= lv.count {
		return nil, fmt.Errorf("tile index %d exceeds tile count %d", idx, lv.count)
	}

	off, err := readArrayElem(rs, bo, lv.offVal, lv.offArrOff, lv.offType, lv.count, idx)
	if err != nil {
		return nil, fmt.Errorf("read tile offset: %w", err)
	}
	cnt, err := readArrayElem(rs, bo, lv.cntVal, lv.cntArrOff, lv.cntType, lv.count, idx)
	if err != nil {
		return nil, fmt.Errorf("read tile bytecount: %w", err)
	}
	if cnt == 0 || cnt > 32<<20 {
		return nil, fmt.Errorf("implausible tile byte count %d", cnt)
	}

	if _, err := rs.Seek(int64(off), io.SeekStart); err != nil {
		return nil, err
	}
	tile := make([]byte, cnt)
	if _, err := io.ReadFull(rs, tile); err != nil {
		return nil, fmt.Errorf("read tile bytes: %w", err)
	}

	// Splice JPEGTables (if present) into the tile header. Aperio tiles need it;
	// a defensive check keeps the path correct for the rare slide that already
	// carries a self-contained JPEG.
	if lv.jpegTablesLen == 0 {
		return tile, nil
	}
	// JPEGTables 实测 ~289 字节(共享量化/Huffman 表);1MB 已是 3000× 余量。tag 347 的
	// count 若被写成数百 MB/GB(损坏或构造),这里 make 会在校验前吃满内存把进程打死
	// (#6)——先按上限挡住,和瓦片字节数的 32MB 上限同一道防线。
	if lv.jpegTablesLen > 1<<20 {
		return nil, fmt.Errorf("implausible JPEGTables length %d", lv.jpegTablesLen)
	}
	tables := make([]byte, lv.jpegTablesLen)
	if _, err := rs.Seek(int64(lv.jpegTablesOff), io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rs, tables); err != nil {
		return nil, fmt.Errorf("read jpegtables: %w", err)
	}
	return spliceJPEGTables(tile, tables), nil
}

// spliceJPEGTables inserts the shared table segments (DQT/DHT) right after the
// tile's SOI marker. JPEGTables is itself a mini-JPEG wrapped in SOI…EOI; we
// drop its SOI/EOI and keep only the table markers in between.
func spliceJPEGTables(tile, tables []byte) []byte {
	if len(tile) < 2 || tile[0] != 0xFF || tile[1] != 0xD8 {
		return tile // not a JPEG we recognise; return unchanged rather than corrupt
	}
	inner := tables
	if len(tables) >= 4 && tables[0] == 0xFF && tables[1] == 0xD8 &&
		tables[len(tables)-2] == 0xFF && tables[len(tables)-1] == 0xD9 {
		inner = tables[2 : len(tables)-2]
	}
	out := make([]byte, 0, len(tile)+len(inner))
	out = append(out, 0xFF, 0xD8)  // SOI
	out = append(out, inner...)    // shared tables
	out = append(out, tile[2:]...) // rest of the tile (its own SOF/SOS/data)
	return out
}

// tiffHeader reads byte order + first IFD offset. Shared shape with the probe;
// kept local so tile extraction is self-contained.
func tiffHeader(rs io.ReadSeeker) (binary.ByteOrder, bool, int64, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, false, 0, err
	}
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(rs, hdr); err != nil {
		return nil, false, 0, err
	}
	var bo binary.ByteOrder
	switch string(hdr[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, false, 0, fmt.Errorf("not a TIFF")
	}
	magic := bo.Uint16(hdr[2:4])
	big := magic == 43
	if magic != 42 && magic != 43 {
		return nil, false, 0, fmt.Errorf("not a TIFF (magic %d)", magic)
	}
	if big {
		return bo, true, int64(bo.Uint64(hdr[8:16])), nil
	}
	return bo, false, int64(bo.Uint32(hdr[4:8])), nil
}

// walkToTiledLevel follows the IFD chain to the level-th **tiled** IFD.
func walkToTiledLevel(rs io.ReadSeeker, bo binary.ByteOrder, big bool, next int64, level int) (*levelIFD, error) {
	tiledSeen := 0
	for hops := 0; next > 0 && hops < 64; hops++ {
		lv, nxt, tiled, err := readTileIFD(rs, bo, big, next)
		if err != nil {
			return nil, err
		}
		if tiled {
			if tiledSeen == level {
				return lv, nil
			}
			tiledSeen++
		}
		next = nxt
	}
	return nil, fmt.Errorf("level %d not found (only %d tiled levels)", level, tiledSeen)
}

// readTileIFD parses one IFD for the array pointers needed to fetch tiles.
func readTileIFD(rs io.ReadSeeker, bo binary.ByteOrder, big bool, off int64) (lv *levelIFD, next int64, tiled bool, err error) {
	if _, err = rs.Seek(off, io.SeekStart); err != nil {
		return
	}
	var n uint64
	if big {
		b := make([]byte, 8)
		if _, err = io.ReadFull(rs, b); err != nil {
			return
		}
		n = bo.Uint64(b)
	} else {
		b := make([]byte, 2)
		if _, err = io.ReadFull(rs, b); err != nil {
			return
		}
		n = uint64(bo.Uint16(b))
	}
	if n > 4096 {
		err = fmt.Errorf("implausible IFD entry count %d", n)
		return
	}
	entrySize := 12
	if big {
		entrySize = 20
	}
	buf := make([]byte, int(n)*entrySize)
	if _, err = io.ReadFull(rs, buf); err != nil {
		return
	}

	l := &levelIFD{}
	for i := 0; i < int(n); i++ {
		e := buf[i*entrySize:]
		tag := bo.Uint16(e[0:2])
		typ := bo.Uint16(e[2:4])
		var count, valOff uint64
		var valSlot []byte
		if big {
			count = bo.Uint64(e[4:12])
			valOff = bo.Uint64(e[12:20])
			valSlot = e[12:20]
		} else {
			count = uint64(bo.Uint32(e[4:8]))
			valOff = uint64(bo.Uint32(e[8:12]))
			valSlot = e[8:12]
		}
		scalar := func() uint64 {
			if typ == 3 && !big {
				return uint64(bo.Uint16(e[8:10]))
			}
			return valOff
		}
		switch tag {
		case tagCompression:
			l.compression = scalar()
		case tagImageWidth:
			l.width = int(scalar())
		case tagImageLength:
			l.height = int(scalar())
		case tagTileWidth:
			l.tileW = int(scalar())
		case tagTileLength:
			l.tileH = int(scalar())
		case tagTileOffsets:
			l.offArrOff, l.offType, l.count = valOff, uint64(typ), count
			l.offVal = append([]byte(nil), valSlot...)
		case tagTileByteCounts:
			l.cntArrOff, l.cntType = valOff, uint64(typ)
			l.cntVal = append([]byte(nil), valSlot...)
		case tagJPEGTables:
			l.jpegTablesOff, l.jpegTablesLen = valOff, count
		}
	}

	// Next IFD offset sits right after the entries.
	tail := make([]byte, 8)
	if big {
		if _, e := io.ReadFull(rs, tail); e == nil {
			next = int64(bo.Uint64(tail))
		}
	} else {
		if _, e := io.ReadFull(rs, tail[:4]); e == nil {
			next = int64(bo.Uint32(tail[:4]))
		}
	}

	tiled = l.tileW > 0 && l.tileH > 0 && l.offArrOff > 0
	return l, next, tiled, nil
}

// readArrayElem reads a single element of a TIFF array (TileOffsets/ByteCounts),
// seeking straight to it. Keeps memory O(1) on gigapixel slides — we never
// materialise the 23k-entry offset array, only the one entry we need.
//
// **内联 vs 外置**:当 count*elemSize 塞得进值槽(经典 4B / BigTIFF 8B)时,TIFF
// 规则要求数组**内联在值槽里**,valOff 此时不是文件指针而是数据本身。最粗那层常常
// 只有 1 个瓦片(甚至 BigTIFF 下 2 个 LONG),正好触发内联。以前无条件 seek(valOff)
// 会读到瓦片数据当偏移 → fit 首屏那张瓦片 404 / 读到别处组织,且不报错。
func readArrayElem(rs io.ReadSeeker, bo binary.ByteOrder, valSlot []byte, arrOff, typ, count, idx uint64) (uint64, error) {
	sz, ok := tiffTypeSize[uint16(typ)]
	if !ok || (sz != 2 && sz != 4 && sz != 8) {
		return 0, fmt.Errorf("unsupported tile-array element type %d", typ)
	}
	usz := uint64(sz)
	var b []byte
	if count*usz <= uint64(len(valSlot)) {
		// 内联:元素就在值槽里,不 seek。
		start := idx * usz
		if start+usz > uint64(len(valSlot)) {
			return 0, fmt.Errorf("inline tile-array index %d out of value slot", idx)
		}
		b = valSlot[start : start+usz]
	} else {
		if _, err := rs.Seek(int64(arrOff)+int64(idx)*int64(sz), io.SeekStart); err != nil {
			return 0, err
		}
		b = make([]byte, sz)
		if _, err := io.ReadFull(rs, b); err != nil {
			return 0, err
		}
	}
	switch sz {
	case 2:
		return uint64(bo.Uint16(b)), nil
	case 4:
		return uint64(bo.Uint32(b)), nil
	default:
		return bo.Uint64(b), nil
	}
}
