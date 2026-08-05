// mprOrientation.ts — 用 direction 矩阵把体素顺序摆成解剖学正确的显示朝向
// (执行方案-04 · C-H3)。
//
// **为什么必须做这件事**:体素索引顺序 ≠ 显示朝向。MNI152 这类 RAS 数据里
// j 轴指向**前方**,而画布第 0 行在**上方**——直接按体素序上屏,脑子就是上下
// 颠倒的。颠倒的脑子放射科医生一眼能看出来,但**左右翻转看不出来**,而左右
// 翻转意味着把左侧病灶报成右侧。所以这里做两件事:
//   ① 按 direction 决定每个平面要不要翻转 u/v,摆成惯例朝向;
//   ② 给出四边的方位标签(A/P/L/R/S/I)——**标签是安全性的关键**:即使有人
//      对翻转约定有异议,标签也让他一眼看出哪边是哪边,无法被"看着挺正常"骗过。
//
// 采用**放射科惯例**(CT/MRI 阅片标准):轴位/冠状里病人左侧显示在**画面右侧**
// (如同从足侧看向病人);上方一律是解剖学上方(轴位为前方 A,冠冠/矢状为上方 S)。

import type { MPRPlane, Vec3 } from './mprGeometry'

/** 解剖方位代码。 */
export type AnatomyCode = 'R' | 'L' | 'A' | 'P' | 'S' | 'I'

/** 某个体素轴指向的解剖方向:正向代码 + 反向代码。 */
export interface AxisAnatomy {
  /** 体素下标增大时走向的方位(如 'R')。 */
  positive: AnatomyCode
  /** 体素下标减小时走向的方位(如 'L')。 */
  negative: AnatomyCode
}

const OPPOSITE: Record<AnatomyCode, AnatomyCode> = { R: 'L', L: 'R', A: 'P', P: 'A', S: 'I', I: 'S' }
// NIfTI 的世界坐标是 RAS+:世界 x/y/z 的正向分别是 R / A / S。
const WORLD_POSITIVE: AnatomyCode[] = ['R', 'A', 'S']

/**
 * 从 3×3 direction(行优先,体素→世界)判断每个体素轴指向哪个解剖方向。
 * 取每列绝对值最大的分量作为主方向(医学数据几乎总是接近轴对齐)。
 */
export function voxelAxisAnatomy(direction: number[]): [AxisAnatomy, AxisAnatomy, AxisAnatomy] {
  const out: AxisAnatomy[] = []
  for (let c = 0; c < 3; c++) {
    let best = 0
    let bestAbs = -1
    for (let r = 0; r < 3; r++) {
      const v = Math.abs(direction[r * 3 + c])
      if (v > bestAbs) {
        bestAbs = v
        best = r
      }
    }
    const positive = direction[best * 3 + c] >= 0 ? WORLD_POSITIVE[best] : OPPOSITE[WORLD_POSITIVE[best]]
    out.push({ positive, negative: OPPOSITE[positive] })
  }
  return out as [AxisAnatomy, AxisAnatomy, AxisAnatomy]
}

export interface PlaneOrientation {
  /** 面内 u 轴(列)是否需要翻转才符合惯例。 */
  flipU: boolean
  /** 面内 v 轴(行)是否需要翻转。 */
  flipV: boolean
  /** 四边标签(翻转之后的实际朝向),给画布边缘标注。 */
  labels: { top: AnatomyCode; bottom: AnatomyCode; left: AnatomyCode; right: AnatomyCode }
}

/** 各平面的面内轴 → 体素轴下标(与 mprGeometry.planeToVoxel 的约定一致)。 */
const PLANE_AXES: Record<MPRPlane, { u: 0 | 1 | 2; v: 0 | 1 | 2 }> = {
  axial: { u: 0, v: 1 }, // u=i, v=j
  coronal: { u: 0, v: 2 }, // u=i, v=k
  sagittal: { u: 1, v: 2 }, // u=j, v=k
}

/** 惯例:各平面希望"画面右侧"与"画面上方"分别是哪个方位。 */
const DESIRED: Record<MPRPlane, { right: AnatomyCode; top: AnatomyCode }> = {
  // 放射科惯例:如同从足侧看向病人 → 病人左侧在画面右侧;上方为前方。
  axial: { right: 'L', top: 'A' },
  // 冠状同样病人左侧在画面右侧;上方为解剖上方。
  coronal: { right: 'L', top: 'S' },
  // 矢状:上方为解剖上方;画面右侧为后方(即前方朝左,鼻尖朝左的常见显示)。
  sagittal: { right: 'P', top: 'S' },
}

/**
 * 给定 direction 与平面,算出显示需要的翻转与四边标签。
 * 若该平面的轴根本不指向期望的方位族(例如斜采集),则不翻转,但标签仍如实反映
 * 真实朝向——**宁可标签显示一个不常见的方位,也不假装它是常见的**。
 */
export function planeOrientation(direction: number[], plane: MPRPlane): PlaneOrientation {
  const anat = voxelAxisAnatomy(direction)
  const { u, v } = PLANE_AXES[plane]
  const uAnat = anat[u]
  const vAnat = anat[v]
  const want = DESIRED[plane]

  // u 下标增大默认走向 uAnat.positive(画面向右)。若与期望的"右侧"相反则翻转。
  const flipU = uAnat.positive === OPPOSITE[want.right]
  // v 下标增大是画面向下,所以"画面上方"对应 vAnat.negative。若上方与期望相反则翻转。
  const flipV = vAnat.negative === OPPOSITE[want.top]

  const right = flipU ? uAnat.negative : uAnat.positive
  const top = flipV ? vAnat.positive : vAnat.negative
  return {
    flipU,
    flipV,
    labels: { right, left: OPPOSITE[right], top, bottom: OPPOSITE[top] },
  }
}

/** 面内显示坐标 (u,v) → 未翻转的体素面内坐标,用于把点击/涂抹映射回体素。 */
export function displayToPlaneUV(
  o: PlaneOrientation,
  u: number,
  v: number,
  w: number,
  h: number,
): [number, number] {
  return [o.flipU ? w - 1 - u : u, o.flipV ? h - 1 - v : v]
}

/** 体素面内坐标 → 显示坐标(画十字线用)。与上面互为逆。 */
export function planeUVToDisplay(
  o: PlaneOrientation,
  u: number,
  v: number,
  w: number,
  h: number,
): [number, number] {
  return displayToPlaneUV(o, u, v, w, h) // 翻转是自逆的
}

/** 供渲染直接使用:是否翻转 + 尺寸。 */
export type FlipSpec = Pick<PlaneOrientation, 'flipU' | 'flipV'>

export const NO_FLIP: FlipSpec = { flipU: false, flipV: false }

/** 常用:单位方向矩阵(RAS 轴对齐)。 */
export const IDENTITY_DIRECTION: number[] = [1, 0, 0, 0, 1, 0, 0, 0, 1]

/**
 * 当前 MPR 只做**翻转**(沿轴镜像),不做**轴置换或斜切重采样**——所以只有 direction
 * 是"带符号的单位置换、且轴序为 i→x/j→y/k→z"时,固定 k 的视图才真是轴位、平面标签才对
 * (#8)。轴被置换(如 i→S)会把冠状面标成轴位、画笔沿错解剖轴;oblique/shear 更会让长度/
 * 角度漏掉 affine 交叉项。这些情况下页面"看着正常"但语义错——检出后应显式阻断/提示,
 * 而不是静默渲染。返回 true = 轴对齐(翻转足以处理);false = 置换/斜切,当前 MPR 不支持。
 */
export function isCanonicalDirection(direction: number[]): boolean {
  if (direction.length < 9) return true // 无朝向信息(退化为单位阵),不误报
  const used = [false, false, false]
  for (let c = 0; c < 3; c++) {
    let best = 0
    let bestAbs = 0
    for (let r = 0; r < 3; r++) {
      const a = Math.abs(direction[r * 3 + c])
      if (a > bestAbs) {
        bestAbs = a
        best = r
      }
    }
    if (bestAbs < 0.999) return false // 主分量不接近 1 → oblique/shear
    if (used[best] || best !== c) return false // 轴被置换 / 退化(两列同轴)
    used[best] = true
  }
  return true
}

export type { Vec3 }
