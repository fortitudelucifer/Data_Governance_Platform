import { describe, expect, it } from 'vitest'

import {
  displayToPlaneUV,
  IDENTITY_DIRECTION,
  isCanonicalDirection,
  planeOrientation,
  voxelAxisAnatomy,
} from './mprOrientation'

// C-H3 的显示侧:体素顺序 ≠ 解剖朝向。上下颠倒放射科一眼能看出来,**左右翻转
// 看不出来**——而左右翻转 = 把左侧病灶报成右侧。所以翻转规则与四边标签逐条钉死。
// 标签是安全性的关键:即使有人对翻转约定有异议,标签也让他一眼看出哪边是哪边。

describe('voxelAxisAnatomy', () => {
  it('单位矩阵 = 纯 RAS:i→R, j→A, k→S', () => {
    const a = voxelAxisAnatomy(IDENTITY_DIRECTION)
    expect(a.map((x) => x.positive)).toEqual(['R', 'A', 'S'])
    expect(a.map((x) => x.negative)).toEqual(['L', 'P', 'I'])
  })

  it('LPS 数据(DICOM 常见):i→L, j→P, k→S', () => {
    // diag(-1,-1,1):x 反向 → L,y 反向 → P
    const a = voxelAxisAnatomy([-1, 0, 0, 0, -1, 0, 0, 0, 1])
    expect(a.map((x) => x.positive)).toEqual(['L', 'P', 'S'])
  })

  it('轴顺序被置换时按主分量判定(非对角矩阵)', () => {
    // i→S, j→R, k→A(列 0 主分量在第 2 行 …)
    const dir = [0, 1, 0, 0, 0, 1, 1, 0, 0]
    const a = voxelAxisAnatomy(dir)
    expect(a.map((x) => x.positive)).toEqual(['S', 'R', 'A'])
  })
})

describe('planeOrientation — 纯 RAS 数据(如 MNI152)', () => {
  const dir = IDENTITY_DIRECTION

  it('轴位:前方朝上、病人左侧在画面右侧(放射科惯例)', () => {
    const o = planeOrientation(dir, 'axial')
    // u=i 正向是 R,但期望画面右侧是 L → 必须翻转
    expect(o.flipU).toBe(true)
    // v=j 正向是 A(向下),期望上方是 A → 必须翻转
    expect(o.flipV).toBe(true)
    expect(o.labels).toEqual({ top: 'A', bottom: 'P', left: 'R', right: 'L' })
  })

  it('冠状:上方为 S、病人左侧在画面右侧', () => {
    const o = planeOrientation(dir, 'coronal')
    expect(o.flipU).toBe(true) // i:R → 期望右侧 L
    expect(o.flipV).toBe(true) // k:S 向下 → 期望上方 S
    expect(o.labels).toEqual({ top: 'S', bottom: 'I', left: 'R', right: 'L' })
  })

  it('矢状:上方为 S、后方在画面右侧(鼻尖朝左)', () => {
    const o = planeOrientation(dir, 'sagittal')
    expect(o.flipU).toBe(true) // j:A → 期望右侧 P
    expect(o.flipV).toBe(true) // k:S 向下 → 期望上方 S
    expect(o.labels).toEqual({ top: 'S', bottom: 'I', left: 'A', right: 'P' })
  })
})

describe('planeOrientation — LPS 数据(DICOM 常见,与 RAS 左右相反)', () => {
  const dir = [-1, 0, 0, 0, -1, 0, 0, 0, 1] // i→L, j→P, k→S

  it('轴位:i 已经指向 L,不需要翻 u;标签仍如实反映朝向', () => {
    const o = planeOrientation(dir, 'axial')
    expect(o.flipU).toBe(false) // 期望右侧 L,而 i 正向就是 L
    expect(o.flipV).toBe(false) // v=j 正向 P(向下)→ 上方 A,已符合期望
    expect(o.labels).toEqual({ top: 'A', bottom: 'P', left: 'R', right: 'L' })
  })

  it('**两种数据最终显示朝向一致**——这正是做朝向归一化的意义', () => {
    const ras = planeOrientation(IDENTITY_DIRECTION, 'axial')
    const lps = planeOrientation(dir, 'axial')
    expect(lps.labels).toEqual(ras.labels)
  })
})

describe('displayToPlaneUV — 点击/涂抹映射回体素', () => {
  const W = 10
  const H = 6

  it('不翻转时原样', () => {
    expect(displayToPlaneUV({ flipU: false, flipV: false, labels: {} as never }, 3, 2, W, H)).toEqual([3, 2])
  })

  it('翻转后是镜像位置', () => {
    const o = { flipU: true, flipV: true, labels: {} as never }
    expect(displayToPlaneUV(o, 0, 0, W, H)).toEqual([W - 1, H - 1])
    expect(displayToPlaneUV(o, W - 1, H - 1, W, H)).toEqual([0, 0])
  })

  it('翻转自逆:来回两次回到原点(点选与画十字用同一函数才不会错位)', () => {
    const o = { flipU: true, flipV: false, labels: {} as never }
    const [u1, v1] = displayToPlaneUV(o, 7, 4, W, H)
    expect(displayToPlaneUV(o, u1, v1, W, H)).toEqual([7, 4])
  })
})

describe('isCanonicalDirection(#8 非轴对齐检测)', () => {
  it('单位阵 / 纯翻转 = 轴对齐(翻转足以处理)', () => {
    expect(isCanonicalDirection(IDENTITY_DIRECTION)).toBe(true)
    // 沿各轴翻转符号(仍是 i→x/j→y/k→z),翻转层能处理 → 轴对齐。
    expect(isCanonicalDirection([-1, 0, 0, 0, 1, 0, 0, 0, -1])).toBe(true)
    expect(isCanonicalDirection([])).toBe(true) // 无朝向信息:不误报
  })
  it('轴置换(i→S 等)= 非轴对齐(当前 MPR 会标错平面)', () => {
    // 列 0 主轴是 z(i→S),列 2 主轴是 x —— 轴序被置换。
    expect(isCanonicalDirection([0, 0, 1, 0, 1, 0, 1, 0, 0])).toBe(false)
  })
  it('oblique / 斜切 = 非轴对齐', () => {
    // 列 0 主分量 ~0.71,远不到 1 → 斜的。
    expect(isCanonicalDirection([0.71, 0.71, 0, -0.71, 0.71, 0, 0, 0, 1])).toBe(false)
  })
})
