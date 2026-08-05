// maskSlice.ts — 切片掩膜 ↔ bbox-local RLE 关键帧的互转(执行方案-04 · C3.3).
//
// 标注员在整张切片上刷,但存进关键帧的是 **bbox-local** 的 RLE(04 §4.3:
// 刻意偏离标准 COCO 的全图语义,否则 10 万×10 万的 WSI 根本内联不了)。
// 于是每次保存要"裁到 bbox",每次读取要"放回原位"——**这一裁一放是整条链路
// 最容易出错、且错了最不容易发现的地方**:掩膜会整体平移几个像素,画面上依然
// 是个像模像样的形状,位置却是错的。所以这一层抽成纯函数并逐条单测。
//
// 约定:bbox = [x, y, w, h],x/y 是切片内的像素坐标(左上角原点),与
// Keyframe.Bbox 一致;RLE.size = [h, w] 是 bbox 的尺寸。

import { decodeCOCORLE, encodeCOCORLE, type MaskRLE } from './cocoRLE'

export interface BboxMask {
  /** [x, y, w, h] —— 掩膜在切片内的外接框。 */
  bbox: [number, number, number, number]
  rle: MaskRLE
}

/**
 * 整张切片掩膜(row-major, 0/1)→ bbox 裁剪 + RLE。
 * 掩膜全空时返回 null(空掩膜不该产生关键帧——那会存下一个"存在但什么都没有"
 * 的标注,导出时变成一个零面积对象)。
 */
export function maskToBboxRLE(mask: Uint8Array, w: number, h: number): BboxMask | null {
  if (mask.length !== w * h) throw new Error(`maskSlice: mask has ${mask.length} px, want ${w * h}`)
  let minX = w
  let minY = h
  let maxX = -1
  let maxY = -1
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      if (mask[y * w + x]) {
        if (x < minX) minX = x
        if (x > maxX) maxX = x
        if (y < minY) minY = y
        if (y > maxY) maxY = y
      }
    }
  }
  if (maxX < 0) return null // 全空

  const bw = maxX - minX + 1
  const bh = maxY - minY + 1
  const cropped = new Uint8Array(bw * bh)
  for (let y = 0; y < bh; y++) {
    for (let x = 0; x < bw; x++) {
      cropped[y * bw + x] = mask[(minY + y) * w + (minX + x)]
    }
  }
  return { bbox: [minX, minY, bw, bh], rle: encodeCOCORLE(cropped, bh, bw) }
}

/**
 * bbox-local RLE → 整张切片掩膜(放回原位)。
 * 越出切片范围的部分被裁掉而不是报错(bbox 可能来自另一分辨率的历史标注),
 * 但**尺寸不一致会抛错**——那说明 bbox 与 RLE.size 不是一对,继续画就会错位。
 */
export function bboxRLEToMask(bm: BboxMask, w: number, h: number): Uint8Array {
  const [bx, by, bw, bh] = bm.bbox
  const [rh, rw] = bm.rle.size
  if (rh !== bh || rw !== bw) {
    throw new Error(`maskSlice: bbox ${bw}x${bh} 与 RLE size ${rw}x${rh} 不符（错位的根源）`)
  }
  const { mask: cropped } = decodeCOCORLE(bm.rle)
  const full = new Uint8Array(w * h)
  for (let y = 0; y < bh; y++) {
    const ty = by + y
    if (ty < 0 || ty >= h) continue
    for (let x = 0; x < bw; x++) {
      const tx = bx + x
      if (tx < 0 || tx >= w) continue
      if (cropped[y * bw + x]) full[ty * w + tx] = 1
    }
  }
  return full
}

/** voxel_mask 轨迹的一个关键帧:frame = z 切片索引。 */
export interface MaskKeyframe {
  frame: number
  ts_ms: number
  bbox: number[]
  rle: MaskRLE
  outside: boolean
  occluded: boolean
}

/**
 * 掩膜体(整卷 0/1,column-major,与影像体同布局)→ 关键帧数组。
 * **只为非空切片产生关键帧**——空切片不该占一个关键帧(既浪费,又会在导出时
 * 变成零面积对象)。ts_ms 用 z 充当时间轴:voxel_mask 的"时间"就是 z
 * (04 §4.4),这样插值契约的 hold 语义直接适用于跨切片。
 */
export function maskVolumeToKeyframes(maskVol: Uint8Array, dims: [number, number, number]): MaskKeyframe[] {
  const [nx, ny, nz] = dims
  if (maskVol.length !== nx * ny * nz) {
    throw new Error(`maskSlice: mask volume has ${maskVol.length} voxels, want ${nx * ny * nz}`)
  }
  const out: MaskKeyframe[] = []
  const sliceLen = nx * ny
  for (let z = 0; z < nz; z++) {
    // 体是 column-major(i 最快),切片内即 row-major 的 (x + y*nx) —— 与
    // maskToBboxRLE 期望的布局一致,直接切出来用。
    const slice = maskVol.subarray(z * sliceLen, (z + 1) * sliceLen)
    const bm = maskToBboxRLE(slice, nx, ny)
    if (!bm) continue // 空切片:不产生关键帧
    out.push({
      frame: z,
      ts_ms: z,
      bbox: [bm.bbox[0], bm.bbox[1], bm.bbox[2], bm.bbox[3]],
      rle: bm.rle,
      outside: false,
      occluded: false,
    })
  }
  return out
}

/** 关键帧数组 → 掩膜体(读回已存标注继续编辑)。越界/损坏的关键帧会抛错。 */
export function keyframesToMaskVolume(
  keyframes: MaskKeyframe[],
  dims: [number, number, number],
): Uint8Array {
  const [nx, ny, nz] = dims
  const vol = new Uint8Array(nx * ny * nz)
  const sliceLen = nx * ny
  for (const kf of keyframes) {
    const z = kf.frame
    if (z < 0 || z >= nz) continue // 历史标注可能超出当前卷,跳过而不是崩
    if (kf.outside) continue // #9 与导出器对齐:outside 帧不产像素(否则前端显示、导出跳过 → 所见≠所出)
    if (!kf.rle) continue
    const [bx, by, bw, bh] = kf.bbox
    const full = bboxRLEToMask({ bbox: [bx, by, bw, bh], rle: kf.rle }, nx, ny)
    vol.set(full, z * sliceLen)
  }
  return vol
}

/** 某一层的掩膜统计:用于"已标注层"面板(标注员靠它回到画过的层)。 */
export interface SliceStat {
  z: number
  /** 该层的前景体素数(面积)。 */
  voxels: number
}

/**
 * 扫掩膜体,列出**有内容的层**及其面积。逐层勾画时标注员最需要的就是
 * "我画过哪些层、还能跳回去"——没有这个列表,215 层里找回自己画的那几层
 * 只能靠滚轮一层层碰运气。
 */
export function annotatedSlices(maskVol: Uint8Array, dims: [number, number, number]): SliceStat[] {
  const [nx, ny, nz] = dims
  const sliceLen = nx * ny
  const out: SliceStat[] = []
  for (let z = 0; z < nz; z++) {
    let n = 0
    const base = z * sliceLen
    for (let i = 0; i < sliceLen; i++) if (maskVol[base + i]) n++
    if (n > 0) out.push({ z, voxels: n })
  }
  return out
}

/** 在掩膜上画一个圆形笔刷(row-major,就地修改)。erase=true 时擦除。 */
export function paintBrush(
  mask: Uint8Array,
  w: number,
  h: number,
  cx: number,
  cy: number,
  radius: number,
  erase = false,
): void {
  const r2 = radius * radius
  const x0 = Math.max(0, Math.floor(cx - radius))
  const x1 = Math.min(w - 1, Math.ceil(cx + radius))
  const y0 = Math.max(0, Math.floor(cy - radius))
  const y1 = Math.min(h - 1, Math.ceil(cy + radius))
  for (let y = y0; y <= y1; y++) {
    for (let x = x0; x <= x1; x++) {
      const dx = x - cx
      const dy = y - cy
      if (dx * dx + dy * dy <= r2) mask[y * w + x] = erase ? 0 : 1
    }
  }
}

/**
 * 沿线段 (x0,y0)→(x1,y1) 连续落笔刷,填补快速拖动 / 浏览器合并 pointermove 时相邻采样
 * 之间的空洞(#20:小笔刷快移只画离散圆盘 → 中间体素漏标)。实时与草稿回放共用它
 * (经 applyOp),两路结果逐字节一致。
 */
export function paintBrushLine(
  mask: Uint8Array,
  w: number,
  h: number,
  x0: number,
  y0: number,
  x1: number,
  y1: number,
  radius: number,
  erase = false,
): void {
  const dist = Math.hypot(x1 - x0, y1 - y0)
  const n = Math.max(1, Math.ceil(dist / Math.max(1, radius * 0.5))) // 步长 = 半个半径,圆盘充分重叠
  for (let i = 0; i <= n; i++) {
    const t = i / n
    paintBrush(mask, w, h, x0 + (x1 - x0) * t, y0 + (y1 - y0) * t, radius, erase)
  }
}
