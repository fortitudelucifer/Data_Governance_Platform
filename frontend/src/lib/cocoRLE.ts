// cocoRLE.ts — COCO 压缩 RLE 的编解码(执行方案-04 · C3.3).
//
// 标注员在切片上刷出的掩膜,在这里编成 **bbox-local** 的 COCO 压缩 RLE 存进
// voxel_mask 轨迹的关键帧;重新打开时再解回来继续编辑。位串必须与参考实现
// (pycocotools)逐字节一致——否则导出的标签体拿到 3D Slicer 里就是错的,
// 而平台内部自洽、毫无报错。三方锁夹具:testdata/rle/shapes.json
// (counts 由 pycocotools 产出,Go 与本文件各测一半)。
//
// 约定(与 pycocotools 一致):
//   · 掩膜按 **column-major(Fortran)** 展平;
//   · 游程从 **0 的游程**开始,交替计数;
//   · counts 用 LEB128 变体编码,第 3 个游程起对 counts[i-2] 做 delta。

export interface MaskRLE {
  /** [h, w] —— bbox-local 掩膜的尺寸(不是整卷/整片)。 */
  size: [number, number]
  counts: string
}

/**
 * 把 row-major 的 0/1 掩膜编成 COCO 压缩 RLE。
 * @param mask 长度 h*w,非 0 视为前景
 */
export function encodeCOCORLE(mask: Uint8Array, h: number, w: number): MaskRLE {
  if (h <= 0 || w <= 0) throw new Error(`cocoRLE: bad size ${h}x${w}`)
  if (mask.length !== h * w) throw new Error(`cocoRLE: mask has ${mask.length} px, want ${h * w}`)
  const counts: number[] = []
  let prev = 0
  let run = 0
  // column-major 遍历:列在外、行在内。
  for (let x = 0; x < w; x++) {
    for (let y = 0; y < h; y++) {
      const v = mask[y * w + x] !== 0 ? 1 : 0
      if (v === prev) {
        run++
      } else {
        counts.push(run)
        run = 1
        prev = v
      }
    }
  }
  counts.push(run)
  return { size: [h, w], counts: rleToString(counts) }
}

/** 解回 row-major 的 0/1 掩膜。位串与 size 对不上即抛错——歪掩膜最难发现。 */
export function decodeCOCORLE(rle: MaskRLE): { mask: Uint8Array; h: number; w: number } {
  const [h, w] = rle.size
  if (h <= 0 || w <= 0) throw new Error(`cocoRLE: bad size ${rle.size}`)
  const counts = rleFromString(rle.counts)
  let total = 0
  for (const c of counts) {
    if (c < 0) throw new Error(`cocoRLE: negative run ${c}`)
    total += c
  }
  if (total !== h * w) {
    throw new Error(`cocoRLE: runs sum to ${total}, want ${h * w} (size ${h}x${w})`)
  }
  const mask = new Uint8Array(h * w)
  let idx = 0 // column-major 线性下标
  let val = 0
  for (const c of counts) {
    if (val === 1) {
      for (let n = 0; n < c; n++) {
        const p = idx + n
        mask[(p % h) * w + Math.floor(p / h)] = 1
      }
    }
    idx += c
    val ^= 1
  }
  return { mask, h, w }
}

/** counts 数组 → 位串(pycocotools rleToString 的移植)。 */
function rleToString(counts: number[]): string {
  let s = ''
  for (let i = 0; i < counts.length; i++) {
    let x = counts[i]
    if (i > 2) x -= counts[i - 2]
    let more = true
    while (more) {
      let c = x & 0x1f
      x >>= 5
      more = (c & 0x10) !== 0 ? x !== -1 : x !== 0
      if (more) c |= 0x20
      s += String.fromCharCode(c + 48)
    }
  }
  return s
}

/** 位串 → counts 数组(pycocotools rleFrString 的移植)。 */
function rleFromString(s: string): number[] {
  const counts: number[] = []
  let p = 0
  while (p < s.length) {
    let x = 0
    let k = 0
    let more = true
    while (more) {
      if (p >= s.length) throw new Error('cocoRLE: truncated counts string')
      const c = s.charCodeAt(p) - 48
      x |= (c & 0x1f) << (5 * k)
      more = (c & 0x20) !== 0
      p++
      k++
      if (!more && (c & 0x10) !== 0) x |= -1 << (5 * k) // 符号扩展
    }
    if (counts.length > 2) x += counts[counts.length - 2]
    counts.push(x)
  }
  return counts
}

/** 契约规则 1 的护栏(00《稠密几何存储契约》),与后端常量同值。 */
export const MAX_KEYFRAME_RLE_BYTES = 64 * 1024
export const MAX_TRACK_GEOM_BYTES = 4 * 1024 * 1024
