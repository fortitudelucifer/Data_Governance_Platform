import { describe, expect, it } from 'vitest'

import {
  MPR_PLANES,
  planeAspect,
  planeDims,
  planePhysicalSize,
  planeSliceCount,
  planeToVoxel,
  voxelIndex,
  type Vec3,
} from './mprGeometry'

// 三视重切的坐标数学。轴搞错 = 显示成错平面/转置且不报错,所以逐条钉死。
// 用一个非立方、各向异性的体数据(维度全不同、spacing 全不同)——立方体测不出
// 轴混淆(转置也"看起来对")。
const dims: Vec3 = [4, 6, 10] // nx=4, ny=6, nz=10
const spacing: Vec3 = [0.5, 0.8, 3.0]

describe('planeSliceCount', () => {
  it('每个平面沿其法向轴计数', () => {
    expect(planeSliceCount(dims, 'axial')).toBe(10) // 沿 k=nz
    expect(planeSliceCount(dims, 'coronal')).toBe(6) // 沿 j=ny
    expect(planeSliceCount(dims, 'sagittal')).toBe(4) // 沿 i=nx
  })
})

describe('planeDims', () => {
  it('面内 [宽,高] 取正确的两个轴', () => {
    expect(planeDims(dims, 'axial')).toEqual([4, 6]) // i×j
    expect(planeDims(dims, 'coronal')).toEqual([4, 10]) // i×k
    expect(planeDims(dims, 'sagittal')).toEqual([6, 10]) // j×k
  })
})

describe('planePhysicalSize / planeAspect', () => {
  it('物理尺寸 = 体素数 × spacing', () => {
    expect(planePhysicalSize(dims, spacing, 'axial')).toEqual([4 * 0.5, 6 * 0.8]) // [2, 4.8]
    expect(planePhysicalSize(dims, spacing, 'coronal')).toEqual([4 * 0.5, 10 * 3.0]) // [2, 30]
    expect(planePhysicalSize(dims, spacing, 'sagittal')).toEqual([6 * 0.8, 10 * 3.0]) // [4.8, 30]
  })

  it('宽高比 = 物理高/物理宽(各向异性不压扁)', () => {
    // coronal: 高 30mm / 宽 2mm = 15(z 方向 3mm 层厚被拉高)
    expect(planeAspect(dims, spacing, 'coronal')).toBeCloseTo(15, 6)
  })
})

describe('planeToVoxel (重切采样映射)', () => {
  it('axial 固定 k=slice,(u,v)=(i,j)', () => {
    expect(planeToVoxel('axial', 7, 2, 3)).toEqual([2, 3, 7])
  })
  it('coronal 固定 j=slice,(u,v)=(i,k)', () => {
    expect(planeToVoxel('coronal', 5, 2, 8)).toEqual([2, 5, 8])
  })
  it('sagittal 固定 i=slice,(u,v)=(j,k)', () => {
    expect(planeToVoxel('sagittal', 3, 4, 9)).toEqual([3, 4, 9])
  })

  it('三个平面在同一体素相交:各自的 (slice,u,v) 都映回同一 (i,j,k)', () => {
    // 取体素 (i,j,k) = (2,5,8)。
    const [i, j, k]: Vec3 = [2, 5, 8]
    expect(planeToVoxel('axial', k, i, j)).toEqual([i, j, k])
    expect(planeToVoxel('coronal', j, i, k)).toEqual([i, j, k])
    expect(planeToVoxel('sagittal', i, j, k)).toEqual([i, j, k])
  })
})

describe('voxelIndex (column-major,与后端 PNG 布局一致)', () => {
  it('idx = i + j*nx + k*nx*ny', () => {
    expect(voxelIndex(dims, 0, 0, 0)).toBe(0)
    expect(voxelIndex(dims, 1, 0, 0)).toBe(1) // i 最快
    expect(voxelIndex(dims, 0, 1, 0)).toBe(4) // +nx
    expect(voxelIndex(dims, 0, 0, 1)).toBe(24) // +nx*ny
    expect(voxelIndex(dims, 3, 5, 9)).toBe(3 + 5 * 4 + 9 * 24)
  })
  it('越界 → -1', () => {
    expect(voxelIndex(dims, 4, 0, 0)).toBe(-1)
    expect(voxelIndex(dims, 0, 6, 0)).toBe(-1)
    expect(voxelIndex(dims, 0, 0, 10)).toBe(-1)
    expect(voxelIndex(dims, -1, 0, 0)).toBe(-1)
  })
})

describe('MPR_PLANES', () => {
  it('恰好三视', () => {
    expect([...MPR_PLANES]).toEqual(['axial', 'coronal', 'sagittal'])
  })
})
