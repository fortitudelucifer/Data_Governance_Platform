package service

// coco_rle.go — COCO 压缩 RLE 的编解码(执行方案-04 · C3.3).
//
// voxel_mask 轨迹的每个关键帧(= 一个 z 切片)存一份 **bbox-local** 的 COCO
// 压缩 RLE(04 §4.3):刻意偏离标准 COCO 的"全图"语义——一张 WSI 是 10 万×10 万
// 像素,全图 RLE 内联不了;bbox-local 才能保持在载荷行的 KB 量级(C0.3 实测
// 单切片最大 3.4KB)。**导出时必须按各自关键帧的 bbox 平移回全图坐标**,
// 忘了平移就是"掩膜整体错位但文件完全正常"。
//
// 前端负责画→编码(paint 后直接出 RLE 存进载荷),后端负责解码(C3.4 导出
// NIfTI 标签体)与**上限校验**(契约规则 1 的护栏)。两端共享 testdata/rle/
// 夹具:编码位串一个字节都不许漂,否则掩膜静默错乱。
//
// 游程约定(与 pycocotools 一致):
//   · 掩膜按 **column-major(Fortran)** 展平;
//   · 游程从 **0 的游程**开始,交替计数;
//   · counts 用 LEB128 变体编码,第 3 个游程起做 delta(见 rleToString)。

import (
	"fmt"

	paymodel "text-annotation-platform/internal/model/payload"
)

// 契约规则 1 的量化护栏(00《稠密几何存储契约》)。阈值经 C0.3 真实数据校准:
// 脑分割单切片最大 3.4KB、整 track 369KB,离护栏都很远——护栏是给"滑向稠密"
// 的极端情形准备的绊线,撞上就 400 并提示改走 voxel_label 外置工作流,
// **不静默转换**(静默转换 = 用户以为存下了,其实换了一种东西)。
const (
	MaxKeyframeRLEBytes = 64 * 1024       // 单关键帧 RLE 上限
	MaxTrackGeomBytes   = 4 * 1024 * 1024 // 整 track 几何总量上限
	// MaxCellsPerTrack 是 cells 轨迹(病理 C2)的单轨迹细胞数上限。单个 ROI 约 418
	// 个细胞(C-Q4),整片约 6.7 万。4MB 字节护栏兜不住这个(6.7万×42B≈2.8MB<4MB),
	// 所以另设一条**细胞数**绊线,从结构上强制"一个 ROI 一条轨迹"——任务粒度是 ROI,
	// 把整片塞进一条会锁死数天工作量。取值远高于任何单 ROI、远低于整片。
	MaxCellsPerTrack = 10000
	// maxRLEPixels 是解码时 h*w 的硬上限(#11):短 counts 能声明巨型 size 触发 TB 级分配。
	// 64M 像素(8192²)远超任何合法 bbox-local 掩膜/切片,又挡住构造攻击。
	maxRLEPixels = 64 << 20
)

// EncodeCOCORLE 把 bbox-local 掩膜(0/1,column-major 或 row-major 由 rowMajor
// 指定)编成 COCO 压缩 RLE。size = [h, w]。
func EncodeCOCORLE(mask []byte, h, w int, rowMajor bool) (*paymodel.MaskRLE, error) {
	if h <= 0 || w <= 0 {
		return nil, fmt.Errorf("rle: bad size %dx%d", h, w)
	}
	if len(mask) != h*w {
		return nil, fmt.Errorf("rle: mask has %d px, want %d", len(mask), h*w)
	}
	counts := make([]int, 0, 16)
	prev := byte(0)
	run := 0
	// column-major 遍历:x(列)外层、y(行)内层——COCO 的展平顺序。
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			var v byte
			if rowMajor {
				v = mask[y*w+x]
			} else {
				v = mask[x*h+y]
			}
			if v != 0 {
				v = 1
			}
			if v == prev {
				run++
			} else {
				counts = append(counts, run)
				run = 1
				prev = v
			}
		}
	}
	counts = append(counts, run)
	return &paymodel.MaskRLE{Size: [2]int{h, w}, Counts: rleToString(counts)}, nil
}

// DecodeCOCORLE 还原 bbox-local 掩膜,返回 row-major 的 0/1 字节(便于渲染与
// 导出逐行写)。
func DecodeCOCORLE(r *paymodel.MaskRLE) ([]byte, int, int, error) {
	if r == nil {
		return nil, 0, 0, fmt.Errorf("rle: nil")
	}
	h, w := r.Size[0], r.Size[1]
	if h <= 0 || w <= 0 {
		return nil, 0, 0, fmt.Errorf("rle: bad size %v", r.Size)
	}
	// #11 短 counts 可声明巨型 size(如 [1e6,1e6]),下面 total==h*w 校验也能被构造成立
	// → make([]byte, h*w) 分配 TB 级数组 OOM。解码前先按像素预算挡住(h*w 用 int64 防溢出)。
	if int64(h)*int64(w) > maxRLEPixels {
		return nil, 0, 0, fmt.Errorf("rle: size %d×%d 超过像素上限 %d(疑似构造/损坏)", h, w, maxRLEPixels)
	}
	counts, err := rleFromString(r.Counts)
	if err != nil {
		return nil, 0, 0, err
	}
	total := 0
	for _, c := range counts {
		if c < 0 {
			return nil, 0, 0, fmt.Errorf("rle: negative run %d", c)
		}
		total += c
	}
	if total != h*w {
		// 长度对不上说明位串或 size 有一方错了。**宁可报错也不铺一张歪掩膜**
		// ——歪掩膜在画面上看着像模像样,是最难发现的一类错。
		return nil, 0, 0, fmt.Errorf("rle: runs sum to %d, want %d (size %dx%d)", total, h*w, h, w)
	}
	out := make([]byte, h*w)
	idx := 0 // column-major 线性下标
	val := byte(0)
	for _, c := range counts {
		for n := 0; n < c; n++ {
			if val == 1 {
				x := idx / h
				y := idx % h
				out[y*w+x] = 1 // 写回 row-major
			}
			idx++
		}
		val ^= 1
	}
	return out, h, w, nil
}

// rleToString 是 pycocotools rleToString 的忠实移植:每个游程用 5 bit 一组的
// LEB128 变体编码,**第 3 个游程起先减去前 2 个位置的值**(delta),再逐组输出。
func rleToString(counts []int) string {
	buf := make([]byte, 0, len(counts)*3)
	for i, cnt := range counts {
		x := int64(cnt)
		if i > 2 {
			x -= int64(counts[i-2])
		}
		more := true
		for more {
			c := byte(x & 0x1f)
			x >>= 5
			if c&0x10 != 0 {
				more = x != -1
			} else {
				more = x != 0
			}
			if more {
				c |= 0x20
			}
			buf = append(buf, c+48)
		}
	}
	return string(buf)
}

// rleFromString 是上面的逆运算(pycocotools rleFrString)。
func rleFromString(s string) ([]int, error) {
	counts := make([]int, 0, 16)
	p := 0
	for p < len(s) {
		var x int64
		var k uint
		more := true
		for more {
			if p >= len(s) {
				return nil, fmt.Errorf("rle: truncated counts string")
			}
			c := int64(s[p]) - 48
			x |= (c & 0x1f) << (5 * k)
			more = c&0x20 != 0
			p++
			k++
			if !more && c&0x10 != 0 {
				x |= -1 << (5 * k) // 符号扩展
			}
		}
		if len(counts) > 2 {
			x += int64(counts[len(counts)-2])
		}
		counts = append(counts, int(x))
	}
	return counts, nil
}
