// mprRender.ts — 把 16-bit 体数据的一个平面切片渲成画布 RGBA(执行方案-04 · C3.2b).
//
// 这里把 C3.2a 的两块数学拼起来:planeToVoxel 重切 + LUT 窗位映射 → 一幅可上屏
// 的 RGBA。**关键:全程走原始 16-bit 体素,不经 canvas getImageData**——canvas
// 2D 每通道只有 8-bit,若先把 PNG16 塞进 <img>+canvas 再读回,16-bit 当场被截成
// 8-bit,C0.5 的位深保真就白做了。所以体数据以 Uint16Array 常驻内存,窗位在这里
// 逐像素查表,输出才交给 canvas。
//
// 体数据布局 = column-major(i 最快,与 NIfTI 及后端 PNG 切片一致):
//   volume[i + j*nx + k*nx*ny] = 体素(i,j,k) 的 PNG16 像素值.

import { planeDims, planeToVoxel, voxelIndex, type MPRPlane, type Vec3 } from './mprGeometry'
import { NO_FLIP, type FlipSpec } from './mprOrientation'

export interface RenderedPlane {
  rgba: Uint8ClampedArray // length w*h*4, ready for ctx.putImageData
  width: number
  height: number
}

/**
 * 掩膜叠加层:与影像体**同布局同尺寸**的 0/1 体,重切时走同一套 planeToVoxel,
 * 所以三视里掩膜和影像天然对齐——各画各的最容易错位(而错位后画面依然"像那么
 * 回事")。alpha ∈ [0,1]。
 */
export interface MaskOverlay {
  volume: Uint8Array
  rgb: [number, number, number]
  alpha: number
}

/**
 * assembleVolume 把逐 z 的 axial 切片(每片 Uint16Array,长 nx*ny,x 最快)拼成
 * 一整卷 Uint16Array(column-major)。两者的面内布局都是 x 最快,所以按 z 顺序
 * 拼接即可——顺序错了(比如按别的轴)会把切片打乱,而画面"看着还像那么回事",
 * 是不报错的那类 bug,所以单测钉死拼接顺序。
 */
export function assembleVolume(slices: Uint16Array[], dims: Vec3): Uint16Array {
  const [nx, ny, nz] = dims
  if (slices.length !== nz) {
    throw new Error(`assembleVolume: got ${slices.length} slices, dims say nz=${nz}`)
  }
  const sliceLen = nx * ny
  const vol = new Uint16Array(sliceLen * nz)
  for (let k = 0; k < nz; k++) {
    if (slices[k].length !== sliceLen) {
      throw new Error(`assembleVolume: slice ${k} has ${slices[k].length} px, want ${sliceLen}`)
    }
    vol.set(slices[k], k * sliceLen)
  }
  return vol
}

/**
 * renderPlane reslices `volume` at (plane, slice) and window-maps each voxel
 * through `lut` into an RGBA buffer. Out-of-range samples are opaque black.
 * The returned width/height are in VOXELS (aspect-ratio correction for
 * anisotropic spacing happens at draw time via planeAspect, not here — keeping
 * the pixel data 1:1 with voxels makes geometry math and picking exact).
 */
export function renderPlane(
  volume: Uint16Array,
  dims: Vec3,
  plane: MPRPlane,
  slice: number,
  lut: Uint8Array,
  /**
   * 掩膜叠加层,**按顺序叠加**(后者覆盖前者)。多分割场景每段一层;
   * 只传可见的段。所有层与影像走同一次采样映射,不可能各翻各的、各错各的。
   */
  overlays?: MaskOverlay[],
  /**
   * 显示翻转(C-H3):体素顺序 ≠ 解剖朝向。不翻的话 RAS 数据的脑子是上下颠倒的,
   * 而左右翻转更阴——看不出来,却意味着把左侧病灶报成右侧。翻转在**采样时**做,
   * 掩膜与影像因此走同一条路径,不可能各翻各的。
   */
  flip: FlipSpec = NO_FLIP,
): RenderedPlane {
  // 面内尺寸只有一个真源:planeDims。这里重算一遍就是等着两边漂。
  const [w, h] = planeDims(dims, plane)
  const rgba = new Uint8ClampedArray(w * h * 4)
  const layers = overlays ?? []
  for (let v = 0; v < h; v++) {
    for (let u = 0; u < w; u++) {
      // 显示坐标 →(翻转)→ 体素面内坐标。影像与掩膜共用这一次映射。
      const su = flip.flipU ? w - 1 - u : u
      const sv = flip.flipV ? h - 1 - v : v
      const [i, j, k] = planeToVoxel(plane, slice, su, sv)
      const idx = voxelIndex(dims, i, j, k)
      const o = (v * w + u) * 4
      let g = 0
      if (idx >= 0) {
        g = lut[volume[idx]]
      }
      let r = g
      let gg = g
      let b = g
      // 掩膜与影像走**同一个 idx**:对齐是结构保证的,不是靠两边各算一次。
      if (idx >= 0) {
        for (const ov of layers) {
          if (!ov.volume[idx]) continue
          const a = ov.alpha < 0 ? 0 : ov.alpha > 1 ? 1 : ov.alpha
          r = r * (1 - a) + ov.rgb[0] * a
          gg = gg * (1 - a) + ov.rgb[1] * a
          b = b * (1 - a) + ov.rgb[2] * a
        }
      }
      rgba[o] = r
      rgba[o + 1] = gg
      rgba[o + 2] = b
      rgba[o + 3] = 255
    }
  }
  return { rgba, width: w, height: h }
}
