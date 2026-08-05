package service

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
)

// WSI 附属图剥离 — C1.1a 的 D-1b 欠账兑现。
//
// # 要防的 PHI
//
// 临床切片自带 label / macro 两张附属图,label 上**烧录着病人姓名/病历号**(PS3.15
// 像素文字)。探针已识别并置 HasLabelImage,但直到这里才真正把它们从文件里剥掉——
// C-Q1:去标识发生在导入、存的就是去标识后的字节(sha256 也算在去标识后)。
//
// # 为什么是"改链 + 抹像素"而不是整文件重写
//
// 病理金字塔(C1.1c)是**按需从原片取原生瓦片、不重编码**的,所以剥离绝不能动金字塔
// 那几个 IFD 的瓦片偏移——一动,ExtractTile 就得跟着改,极易出"看着对却错一格"的静默
// 错。做法是**外科式**的两刀,金字塔 IFD 一个字节都不碰:
//   ① **改 IFD 链**:把保留 IFD 的"下一个 IFD"指针重接,跳过 label/macro——探针/查看器/
//      OpenSlide 顺链走都到不了它们(重探针 HasLabelImage=false)。
//   ② **抹像素**:把被删 IFD 的 strip 像素数据(StripOffsets/StripByteCounts 指向的字节)
//      全写 0——PHI 像素**物理消失**,不只是"隐藏"(取证扫文件也扒不出那张 label JPEG)。
// 金字塔瓦片在别的字节区,两刀都不碰它 → 剥离后瓦片逐字节不变(测试用 ExtractTile 对拍)。
//
// # 会静默出错的点
//
//   · **BigTIFF 偏移 8 字节**:改链指针写 4 字节会在大切片上把链改坏(同探针那条)。
//   · **strip 偏移/字节数可能是 SHORT/LONG/LONG8 且可能不内联**(count>1 时是数组偏移)——
//     按 TIFF 类型/内联规则读,写死会抹错范围(抹不掉 PHI、或误伤金字塔)。

const (
	tagStripOffsets    = 273
	tagStripByteCounts = 279
)

// slideIFD 是剥离用的一个 IFD 的最小画像。
type slideIFD struct {
	offset       int64 // IFD 在文件里的起始偏移
	nextFieldPos int64 // 该 IFD "下一个 IFD 偏移" 字段在文件里的位置
	tiled        bool  // 有 TileWidth/Length → 金字塔层(保留,绝不碰)
	desc         string
	stripRanges  [][2]int64 // (offset, length) 像素数据块,删除时抹 0
}

// StripAssociatedImages 从金字塔 TIFF(SVS…)里剥掉 label/macro 附属图,把结果写到 w。
// 金字塔层与缩略图保留;瓦片数据逐字节不动。返回被剥掉的附属图名(如 ["label","macro"]）。
//
// 只读文件头/IFD(几 KB),像素数据在流式拷贝时按需抹 0 —— 不把整张切片读进内存
// (与 slide_derive 一致的纪律:切片以 GB 计)。
func StripAssociatedImages(rs io.ReadSeeker, size int64, w io.Writer) ([]string, error) {
	hdr := make([]byte, 16)
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rs, hdr); err != nil {
		return nil, fmt.Errorf("strip: read header: %w", err)
	}
	var bo binary.ByteOrder
	switch string(hdr[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, fmt.Errorf("strip: not a TIFF (byte order %q)", hdr[0:2])
	}
	magic := bo.Uint16(hdr[2:4])
	big := magic == 43
	if magic != 42 && magic != 43 {
		return nil, fmt.Errorf("strip: not a TIFF (magic %d)", magic)
	}

	// 头里"第一个 IFD 偏移"字段的位置 + 大小,以及第一个 IFD 偏移值。改链时它是链头。
	var headerFieldPos int64
	var first int64
	offSize := int64(4)
	if big {
		if osz := bo.Uint16(hdr[4:6]); osz != 8 {
			return nil, fmt.Errorf("strip: unsupported BigTIFF offset size %d", osz)
		}
		offSize = 8
		headerFieldPos = 8
		first = int64(bo.Uint64(hdr[8:16]))
	} else {
		headerFieldPos = 4
		first = int64(bo.Uint32(hdr[4:8]))
	}

	ifds, err := walkSlideIFDs(rs, bo, big, first, size)
	if err != nil {
		return nil, err
	}

	// 分类:tiled → 金字塔(保留);非 tiled 且 desc 含 label/macro → 删;其余(缩略图)保留。
	var kept []slideIFD
	var removed []string
	var zeroRanges [][2]int64
	for _, f := range ifds {
		d := strings.ToLower(f.desc)
		isLabel := strings.Contains(d, "label")
		isMacro := strings.Contains(d, "macro")
		if !f.tiled && (isLabel || isMacro) {
			if isLabel {
				removed = append(removed, "label")
			} else {
				removed = append(removed, "macro")
			}
			zeroRanges = append(zeroRanges, f.stripRanges...)
			continue
		}
		kept = append(kept, f)
	}

	// 组织字节补丁:① 改链指针(头 → K0 → K1 → … → Km → 0);② strip 像素抹 0。
	patches := make([]bytePatch, 0, len(kept)+1+len(zeroRanges))
	writeOff := func(pos, target int64) {
		b := make([]byte, offSize)
		if big {
			bo.PutUint64(b, uint64(target))
		} else {
			bo.PutUint32(b, uint32(target))
		}
		patches = append(patches, bytePatch{off: pos, data: b})
	}
	prevFieldPos := headerFieldPos
	for _, k := range kept {
		writeOff(prevFieldPos, k.offset) // 上一环指向本保留 IFD
		prevFieldPos = k.nextFieldPos
	}
	writeOff(prevFieldPos, 0) // 最后一个保留 IFD 的 next = 0(链尾)
	for _, r := range zeroRanges {
		if r[1] > 0 {
			patches = append(patches, bytePatch{off: r[0], data: make([]byte, r[1])})
		}
	}

	if err := applyPatchesStreaming(rs, size, w, patches); err != nil {
		return nil, err
	}
	return removed, nil
}

// walkSlideIFDs 顺 IFD 链走一遍,给每个 IFD 记下改链/抹像素所需的最小信息。
func walkSlideIFDs(rs io.ReadSeeker, bo binary.ByteOrder, big bool, first, size int64) ([]slideIFD, error) {
	var out []slideIFD
	next := first
	const maxIFDs = 256
	for guard := 0; next > 0; guard++ {
		if guard >= maxIFDs {
			// fail-closed:IFD 链过长疑似损坏或成环。宁可拒绝也不静默截断——截断会
			// 让第 256 个之后的 label 逃过剥离,而函数照样报"成功"(#8)。
			return nil, fmt.Errorf("strip: IFD chain exceeds %d entries (corrupt or cyclic)", maxIFDs)
		}
		if _, err := rs.Seek(next, io.SeekStart); err != nil {
			return nil, err
		}
		var n uint64
		if big {
			b := make([]byte, 8)
			if _, err := io.ReadFull(rs, b); err != nil {
				return nil, err
			}
			n = bo.Uint64(b)
		} else {
			b := make([]byte, 2)
			if _, err := io.ReadFull(rs, b); err != nil {
				return nil, err
			}
			n = uint64(bo.Uint16(b))
		}
		if n > 4096 {
			return nil, fmt.Errorf("strip: implausible IFD entry count %d", n)
		}
		entrySize := int64(12)
		countSize := int64(2)
		if big {
			entrySize = 20
			countSize = 8
		}
		buf := make([]byte, int64(n)*entrySize)
		if _, err := io.ReadFull(rs, buf); err != nil {
			return nil, err
		}
		info := slideIFD{offset: next, nextFieldPos: next + countSize + int64(n)*entrySize}

		var descLen uint64
		var descVal []byte
		var soType, sbType uint16
		var soCount, sbCount uint64
		var soVal, sbVal []byte
		for i := int64(0); i < int64(n); i++ {
			e := buf[i*entrySize:]
			tag := bo.Uint16(e[0:2])
			typ := bo.Uint16(e[2:4])
			var count uint64
			var valField []byte
			if big {
				count = bo.Uint64(e[4:12])
				valField = e[12:20]
			} else {
				count = uint64(bo.Uint32(e[4:8]))
				valField = e[8:12]
			}
			switch tag {
			case tagTileWidth, tagTileLength:
				info.tiled = true
			case tagImageDesc:
				descLen = count
				descVal = append([]byte(nil), valField...)
			case tagStripOffsets:
				soType, soCount, soVal = typ, count, append([]byte(nil), valField...)
			case tagStripByteCounts:
				sbType, sbCount, sbVal = typ, count, append([]byte(nil), valField...)
			}
		}
		// 读 desc(只为分类;非 tiled 的才可能是 label/macro)。内联短描述现在照读(#7a)。
		if descLen > 0 && descLen <= 1<<20 {
			info.desc = readTiffDesc(rs, bo, big, descVal, descLen)
		}
		// 非金字塔 IFD 读 strip 范围(用于抹像素)。fail-closed:这些 IFD 可能就是要剥的
		// label/macro,strip 元数据但凡不完整(数量不等 / 类型不支持 / 数组截断 / 越界),就
		// 无法保证把 PHI 像素抹干净——宁可整体报错,也绝不返回"看着成功、其实残留"(#8)。
		if !info.tiled && (soCount > 0 || sbCount > 0) {
			if soCount != sbCount {
				return nil, fmt.Errorf("strip: IFD@%d StripOffsets/ByteCounts count mismatch (%d vs %d)", next, soCount, sbCount)
			}
			offs, oerr := readTiffIntArray(rs, bo, soType, soCount, soVal, big)
			if oerr != nil {
				return nil, fmt.Errorf("strip: IFD@%d StripOffsets: %w", next, oerr)
			}
			lens, lerr := readTiffIntArray(rs, bo, sbType, sbCount, sbVal, big)
			if lerr != nil {
				return nil, fmt.Errorf("strip: IFD@%d StripByteCounts: %w", next, lerr)
			}
			if int64(len(offs)) != int64(soCount) || int64(len(lens)) != int64(sbCount) {
				return nil, fmt.Errorf("strip: IFD@%d strip array short read", next)
			}
			for i := 0; i < len(offs); i++ {
				off, ln := offs[i], lens[i]
				if off < 0 || ln < 0 || off > size || ln > size-off {
					return nil, fmt.Errorf("strip: IFD@%d strip[%d] out of bounds (off=%d len=%d size=%d)", next, i, off, ln, size)
				}
				info.stripRanges = append(info.stripRanges, [2]int64{off, ln})
			}
		}

		// 读"下一个 IFD"偏移。
		if _, err := rs.Seek(info.nextFieldPos, io.SeekStart); err != nil {
			return nil, err
		}
		t := make([]byte, 8)
		if big {
			if _, err := io.ReadFull(rs, t); err != nil {
				// 截断在"下一个 IFD 偏移"处:fail-closed,别拿已走到的一半 IFD 当成功返回
				// ——后面可能正藏着没被处理的 label(#8)。
				return nil, fmt.Errorf("strip: read next IFD offset: %w", err)
			}
			next = int64(bo.Uint64(t))
		} else {
			if _, err := io.ReadFull(rs, t[:4]); err != nil {
				return nil, fmt.Errorf("strip: read next IFD offset: %w", err)
			}
			next = int64(bo.Uint32(t[:4]))
		}
		out = append(out, info)
	}
	return out, nil
}

// readTiffOff 从值字段读一个偏移(4 或 8 字节)。
func readTiffOff(bo binary.ByteOrder, big bool, valField []byte) uint64 {
	if big {
		return bo.Uint64(valField[:8])
	}
	return uint64(bo.Uint32(valField[:4]))
}

// readTiffDesc 读 ImageDescription(短则内联在值字段,长则按偏移取)。
//
// #7a:内联情形以前直接返回空 → 短到能塞进值字段的 "label"/"macro" 描述永远分类
// 不出来,那张附属图被当缩略图保留、PHI 泄露。现在照读内联字节(descVal 是原始值字段)。
func readTiffDesc(rs io.ReadSeeker, bo binary.ByteOrder, big bool, descVal []byte, ln uint64) string {
	if ln == 0 {
		return ""
	}
	inlineCap := uint64(4)
	if big {
		inlineCap = 8
	}
	if ln <= inlineCap {
		if int(ln) > len(descVal) {
			return ""
		}
		return strings.TrimRight(string(descVal[:ln]), "\x00")
	}
	off := readTiffOff(bo, big, descVal)
	if _, err := rs.Seek(int64(off), io.SeekStart); err != nil {
		return ""
	}
	b := make([]byte, ln)
	if _, err := io.ReadFull(rs, b); err != nil {
		return ""
	}
	return string(b)
}

// readTiffIntArray 读整型数组(SHORT/LONG/LONG8);内联则从值字段取,否则按偏移取。
// fail-closed:类型不支持、数量离谱、越界 seek、截断读一律报错(不再静默返回 nil)。
// 静默返回空会让调用方以为"没有像素要抹",把 PHI 留在文件里却报成功(#8)。
func readTiffIntArray(rs io.ReadSeeker, bo binary.ByteOrder, typ uint16, count uint64, valField []byte, big bool) ([]int64, error) {
	tsz := int64(tiffTypeSize[typ])
	if tsz == 0 {
		return nil, fmt.Errorf("unsupported TIFF type %d", typ)
	}
	if count == 0 {
		return nil, nil
	}
	if count > 1<<20 {
		// strip 数(≈图高 / 每 strip 行数)不该有百万级;这么大八成是损坏或构造,
		// 先挡住 count*tsz 溢出与随后的 OOM 分配。
		return nil, fmt.Errorf("implausible element count %d", count)
	}
	total := int64(count) * tsz
	inlineCap := int64(4)
	if big {
		inlineCap = 8
	}
	var raw []byte
	if total <= inlineCap {
		raw = valField[:total]
	} else {
		off := readTiffOff(bo, big, valField)
		if _, err := rs.Seek(int64(off), io.SeekStart); err != nil {
			return nil, err
		}
		raw = make([]byte, total)
		if _, err := io.ReadFull(rs, raw); err != nil {
			return nil, err
		}
	}
	out := make([]int64, 0, count)
	for i := int64(0); i < int64(count); i++ {
		e := raw[i*tsz:]
		switch tsz {
		case 2:
			out = append(out, int64(bo.Uint16(e[:2])))
		case 4:
			out = append(out, int64(bo.Uint32(e[:4])))
		case 8:
			out = append(out, int64(bo.Uint64(e[:8])))
		}
	}
	return out, nil
}

// bytePatch 是文件里某偏移处要覆盖的字节。
type bytePatch struct {
	off  int64
	data []byte
}

// applyPatchesStreaming 把 rs 的 [0,size) 流式拷贝到 w,途中把落在补丁范围的字节
// 用补丁内容替换。补丁按偏移排序、互不重叠(改链在 IFD 头、抹像素在 strip 区,天然分离)。
func applyPatchesStreaming(rs io.ReadSeeker, size int64, w io.Writer, patches []bytePatch) error {
	sort.Slice(patches, func(i, j int) bool { return patches[i].off < patches[j].off })
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var pos int64
	for _, p := range patches {
		if p.off < pos {
			return fmt.Errorf("strip: overlapping patch at %d (pos %d)", p.off, pos)
		}
		if n := p.off - pos; n > 0 {
			if _, err := io.CopyN(w, rs, n); err != nil {
				return err
			}
		}
		if _, err := w.Write(p.data); err != nil {
			return err
		}
		// 源前进过补丁覆盖的等长区间。
		if _, err := rs.Seek(int64(len(p.data)), io.SeekCurrent); err != nil {
			return err
		}
		pos = p.off + int64(len(p.data))
	}
	if n := size - pos; n > 0 {
		if _, err := io.CopyN(w, rs, n); err != nil {
			return err
		}
	}
	return nil
}
