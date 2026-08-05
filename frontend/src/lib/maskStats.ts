// maskStats.ts — 分割统计(执行方案-04 · C3.3)。
//
// 参照专业工具的分割面板做:3D Slicer 的 Segment Statistics 报"体积/表面积/平均
// 强度",OHIF v3.10 每个 segment 自动算"体积/最小最大强度/质心"。
//
// **"285 体素"对医生没有意义,"115 mm³"才有。** 体素数是实现细节,临床读的是
// 物理体积(病灶大小、器官体积都按 mm³/mL 说话),而我们手里就有 spacing,
// 不换算等于把有用信息藏起来。强度统计同理:CT 里病灶的平均 HU 是判读依据
// (脂肪 -100、水 0、软组织 +40、骨 +700),不给出来标注员还得自己一个个点。

import type { Vec3 } from './mprGeometry'

export interface SegmentStats {
  /** 前景体素数(实现口径,调试与护栏用)。 */
  voxels: number
  /** 物理体积 mm³ = 体素数 × 单体素体积。 */
  volumeMm3: number
  /** 毫升(= cm³ = 1000 mm³),病灶/器官体积的临床口径。 */
  volumeMl: number
  /** 有标注的切片层数。 */
  sliceCount: number
  /** 掩膜内的影像强度统计(CT 为 HU)。掩膜为空时为 null。 */
  intensity: { mean: number; min: number; max: number } | null
  /** 质心(体素坐标),用于"跳到该分割"。掩膜为空时为 null。 */
  centroid: Vec3 | null
}

/**
 * 统计整个掩膜体。单遍扫描同时得体积/强度/质心——体数据上千万体素,
 * 分三遍扫是能感觉到的卡顿。
 */
export function segmentStats(
  maskVol: Uint8Array,
  imageVol: Uint16Array | null,
  dims: Vec3,
  spacing: Vec3,
  sliceSclSlope = 1,
  sliceSclInter = 0,
): SegmentStats {
  const [nx, ny, nz] = dims
  const voxelMm3 = spacing[0] * spacing[1] * spacing[2]
  let voxels = 0
  let sum = 0
  let min = Infinity
  let max = -Infinity
  let cx = 0
  let cy = 0
  let cz = 0
  const sliceSeen = new Uint8Array(nz)

  for (let k = 0; k < nz; k++) {
    const base = k * nx * ny
    for (let j = 0; j < ny; j++) {
      const row = base + j * nx
      for (let i = 0; i < nx; i++) {
        if (!maskVol[row + i]) continue
        voxels++
        sliceSeen[k] = 1
        cx += i
        cy += j
        cz += k
        if (imageVol) {
          const real = imageVol[row + i] * sliceSclSlope + sliceSclInter
          sum += real
          if (real < min) min = real
          if (real > max) max = real
        }
      }
    }
  }

  let sliceCount = 0
  for (let k = 0; k < nz; k++) if (sliceSeen[k]) sliceCount++

  if (voxels === 0) {
    return { voxels: 0, volumeMm3: 0, volumeMl: 0, sliceCount: 0, intensity: null, centroid: null }
  }
  return {
    voxels,
    volumeMm3: voxels * voxelMm3,
    volumeMl: (voxels * voxelMm3) / 1000,
    sliceCount,
    intensity: imageVol ? { mean: sum / voxels, min, max } : null,
    centroid: [Math.round(cx / voxels), Math.round(cy / voxels), Math.round(cz / voxels)],
  }
}

/** 单层的物理面积 mm²(逐层清单里比体素数有意义)。 */
export function sliceAreaMm2(voxels: number, spacing: Vec3): number {
  return voxels * spacing[0] * spacing[1]
}

/** 体积的人类可读写法:小体积用 mm³,大的用 mL。 */
export function formatVolume(mm3: number): string {
  if (mm3 <= 0) return '0'
  if (mm3 < 1000) return `${mm3.toFixed(1)} mm³`
  return `${(mm3 / 1000).toFixed(2)} mL`
}
