// mprGeometry.ts — MPR 三视的重切几何(执行方案-04 · C3.2).
//
// 后端只派生 **axial** 切片(逐 z 的 PNG)。三视查看器要 coronal / sagittal,
// 靠在浏览器里从体数据重切(reslice)。这里是重切的纯坐标数学:平面 × 切片号 ×
// 面内坐标 (u,v) → 体素 (i,j,k)。轴搞错 = 显示成转置/错平面,且不报错——所以
// 抽出来单测(和 frameIndex/trackInterpolation 同一种"危险数学先锁死"的做法)。
//
// 几何一律在**体素索引空间**(C-H3):左右/上下的显示朝向由 volume_meta.direction
// 在渲染层应用,不混进这里的采样映射。各向异性体素(spacing 不等)的物理宽高比
// 也在这里给出——不校正会把图压扁。

export type MPRPlane = 'axial' | 'coronal' | 'sagittal'

export const MPR_PLANES: readonly MPRPlane[] = ['axial', 'coronal', 'sagittal'] as const

export type Vec3 = [number, number, number]

/** 该平面的切片总数(可滚动的层数)。 */
export function planeSliceCount(dims: Vec3, plane: MPRPlane): number {
  const [nx, ny, nz] = dims
  switch (plane) {
    case 'axial':
      return nz // 沿 k
    case 'coronal':
      return ny // 沿 j
    case 'sagittal':
      return nx // 沿 i
  }
}

/** 面内像素尺寸 [宽, 高](体素数)。 */
export function planeDims(dims: Vec3, plane: MPRPlane): [number, number] {
  const [nx, ny, nz] = dims
  switch (plane) {
    case 'axial':
      return [nx, ny] // u=i, v=j
    case 'coronal':
      return [nx, nz] // u=i, v=k
    case 'sagittal':
      return [ny, nz] // u=j, v=k
  }
}

/**
 * 面内物理尺寸 [宽mm, 高mm],用于宽高比校正(各向异性体素不校正会压扁图)。
 */
export function planePhysicalSize(dims: Vec3, spacing: Vec3, plane: MPRPlane): [number, number] {
  const [w, h] = planeDims(dims, plane)
  const [sx, sy, sz] = spacing
  switch (plane) {
    case 'axial':
      return [w * sx, h * sy]
    case 'coronal':
      return [w * sx, h * sz]
    case 'sagittal':
      return [w * sy, h * sz]
  }
}

/**
 * 面内像素宽高比(物理高/物理宽)。渲染时把画布按此拉伸,保证圆的病灶显示成圆。
 */
export function planeAspect(dims: Vec3, spacing: Vec3, plane: MPRPlane): number {
  const [pw, ph] = planePhysicalSize(dims, spacing, plane)
  return pw === 0 ? 1 : ph / pw
}

/**
 * 重切采样映射:平面 plane 的第 slice 层、面内坐标 (u,v) → 体素 (i,j,k)。
 * 这是三视重切的心脏——每个 (u,v) 从体数据的哪个体素取值。
 */
export function planeToVoxel(plane: MPRPlane, slice: number, u: number, v: number): Vec3 {
  switch (plane) {
    case 'axial':
      return [u, v, slice] // 固定 k=slice
    case 'coronal':
      return [u, slice, v] // 固定 j=slice
    case 'sagittal':
      return [slice, u, v] // 固定 i=slice
  }
}

/**
 * 体素线性下标(column-major / Fortran,与 NIfTI 及后端 PNG 切片布局一致):
 * idx = i + j*nx + k*nx*ny. 越界返回 -1(调用方据此判可采样)。
 */
export function voxelIndex(dims: Vec3, i: number, j: number, k: number): number {
  const [nx, ny, nz] = dims
  if (i < 0 || j < 0 || k < 0 || i >= nx || j >= ny || k >= nz) return -1
  return i + j * nx + k * nx * ny
}
