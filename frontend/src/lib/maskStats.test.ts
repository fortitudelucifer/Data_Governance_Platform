import { describe, expect, it } from 'vitest'

import type { Vec3 } from './mprGeometry'
import { formatVolume, segmentStats, sliceAreaMm2 } from './maskStats'

// 体积换算错了不会报错——面板上照样显示一个数,只是这个数是错的,而医生会拿它
// 判断病灶大小。所以用**各向异性**体素(三个方向间距都不同)来测:间距用错轴、
// 漏乘一个维度,结果都会不一样。

const dims: Vec3 = [4, 5, 3]
const spacing: Vec3 = [0.5, 2.0, 4.0] // 单体素体积 = 4 mm³

function emptyMask() {
  return new Uint8Array(dims[0] * dims[1] * dims[2])
}
const idx = (i: number, j: number, k: number) => i + j * dims[0] + k * dims[0] * dims[1]

describe('segmentStats — 体积', () => {
  it('体素数 × 单体素体积(各向异性下三个间距都要用上)', () => {
    const m = emptyMask()
    m[idx(0, 0, 0)] = 1
    m[idx(1, 0, 0)] = 1
    m[idx(2, 0, 0)] = 1
    const s = segmentStats(m, null, dims, spacing)
    expect(s.voxels).toBe(3)
    expect(s.volumeMm3).toBeCloseTo(3 * 0.5 * 2.0 * 4.0, 6) // 12 mm³
    expect(s.volumeMl).toBeCloseTo(0.012, 6)
  })

  it('空掩膜 → 全零且不报错', () => {
    const s = segmentStats(emptyMask(), null, dims, spacing)
    expect(s).toMatchObject({ voxels: 0, volumeMm3: 0, volumeMl: 0, sliceCount: 0 })
    expect(s.intensity).toBeNull()
    expect(s.centroid).toBeNull()
  })

  it('层数只数**有标注**的层', () => {
    const m = emptyMask()
    m[idx(0, 0, 0)] = 1
    m[idx(0, 0, 2)] = 1 // 跳过 k=1
    expect(segmentStats(m, null, dims, spacing).sliceCount).toBe(2)
  })
})

describe('segmentStats — 强度(CT 即 HU)', () => {
  it('只统计掩膜内的体素,并应用 slice 仿射', () => {
    const img = new Uint16Array(dims[0] * dims[1] * dims[2])
    img[idx(0, 0, 0)] = 1200
    img[idx(1, 0, 0)] = 1000
    img[idx(2, 0, 0)] = 9999 // 掩膜外，不该被统计
    const m = emptyMask()
    m[idx(0, 0, 0)] = 1
    m[idx(1, 0, 0)] = 1

    // slope 1 / inter -1024 → HU：176 与 -24
    const s = segmentStats(m, img, dims, spacing, 1, -1024)
    expect(s.intensity!.max).toBeCloseTo(176, 6)
    expect(s.intensity!.min).toBeCloseTo(-24, 6)
    expect(s.intensity!.mean).toBeCloseTo(76, 6)
  })

  it('不给影像体 → intensity 为 null(而不是编一个 0)', () => {
    const m = emptyMask()
    m[idx(0, 0, 0)] = 1
    expect(segmentStats(m, null, dims, spacing).intensity).toBeNull()
  })
})

describe('segmentStats — 质心', () => {
  it('对称分布的质心在中心', () => {
    const m = emptyMask()
    m[idx(1, 1, 1)] = 1
    m[idx(3, 3, 1)] = 1
    const c = segmentStats(m, null, dims, spacing).centroid!
    expect(c).toEqual([2, 2, 1])
  })
})

describe('sliceAreaMm2 / formatVolume', () => {
  it('单层面积只用面内两个间距', () => {
    expect(sliceAreaMm2(10, spacing)).toBeCloseTo(10 * 0.5 * 2.0, 6) // 不含 z 的 4.0
  })

  it('小体积用 mm³、大体积用 mL', () => {
    expect(formatVolume(12)).toBe('12.0 mm³')
    expect(formatVolume(2500)).toBe('2.50 mL')
    expect(formatVolume(0)).toBe('0')
  })
})
