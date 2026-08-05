import { describe, expect, it } from 'vitest'

import {
  angleDeg,
  distanceMm,
  formatDeg,
  formatMm,
  planeSpacing,
  describeMeasurement,
  type Measurement,
} from './measure'
import type { Vec3 } from './mprGeometry'

// C3.25 测量。这里的数会进医学报告（RECIST 长径直接影响"缓解还是进展"的判断），
// 所以每条测试都用**各向异性**体素——各向同性下把两个轴的 spacing 搞反、
// 或者拿像素当毫米，结果照样"对"，什么也测不出来。

// 三个轴的 spacing 刻意两两不同且差距明显：搞混任意两个都会让数字大幅偏移。
const spacing: Vec3 = [0.5, 2.0, 5.0] // i=0.5mm, j=2mm, k=5mm（典型 CT 层厚）

describe('planeSpacing：面内两轴对应哪两个体素轴', () => {
  it('三个平面各自取到正确的 spacing 对', () => {
    // axial(u=i, v=j) / coronal(u=i, v=k) / sagittal(u=j, v=k)
    expect(planeSpacing('axial', spacing)).toEqual([0.5, 2.0])
    expect(planeSpacing('coronal', spacing)).toEqual([0.5, 5.0])
    expect(planeSpacing('sagittal', spacing)).toEqual([2.0, 5.0])
  })

  it('与重切共用同一套轴映射（不另写 switch）', () => {
    // 三个平面的 spacing 对必须两两不同——若某两个相同，说明轴映射塌了，
    // 而画面可能仍然正常，只有数字是错的。
    const all = [planeSpacing('axial', spacing), planeSpacing('coronal', spacing), planeSpacing('sagittal', spacing)]
    const uniq = new Set(all.map((p) => p.join(',')))
    expect(uniq.size).toBe(3)
  })
})

describe('distanceMm', () => {
  it('沿 u 轴：只用 u 的 spacing', () => {
    // axial 的 u 轴是 i（0.5mm）：10 格 = 5mm
    expect(distanceMm({ u: 0, v: 0 }, { u: 10, v: 0 }, 'axial', spacing)).toBeCloseTo(5, 6)
  })

  it('沿 v 轴：只用 v 的 spacing', () => {
    // axial 的 v 轴是 j（2mm）：10 格 = 20mm
    expect(distanceMm({ u: 0, v: 0 }, { u: 0, v: 10 }, 'axial', spacing)).toBeCloseTo(20, 6)
  })

  it('**斜线必须逐轴换算**（先求像素距离再乘平均 spacing 是错的）', () => {
    // (0,0)→(10,10)：正确 = hypot(10*0.5, 10*2) = hypot(5,20) ≈ 20.6155
    // 若先算像素距离 hypot(10,10)=14.14 再乘"平均 spacing"1.25 → 17.68，差 3mm。
    // 3mm 在 RECIST 阈值附近足以改变结论，而它看起来完全像个正常的数。
    const got = distanceMm({ u: 0, v: 0 }, { u: 10, v: 10 }, 'axial', spacing)
    expect(got).toBeCloseTo(Math.hypot(5, 20), 6)
    expect(got).not.toBeCloseTo(Math.hypot(10, 10) * 1.25, 1)
  })

  it('同一段像素长度在不同平面给出不同物理长度', () => {
    // 这正是各向异性的意义：屏幕上一样长 ≠ 实际一样长。
    const p = { u: 0, v: 0 }
    const q = { u: 0, v: 10 }
    expect(distanceMm(p, q, 'axial', spacing)).toBeCloseTo(20, 6) // v=j → 2mm
    expect(distanceMm(p, q, 'coronal', spacing)).toBeCloseTo(50, 6) // v=k → 5mm
  })

  it('对称：a→b 与 b→a 相同', () => {
    const a = { u: 3, v: 7 }
    const b = { u: 11, v: 2 }
    expect(distanceMm(a, b, 'sagittal', spacing)).toBeCloseTo(distanceMm(b, a, 'sagittal', spacing), 9)
  })

  it('两点重合 → 0', () => {
    expect(distanceMm({ u: 5, v: 5 }, { u: 5, v: 5 }, 'axial', spacing)).toBe(0)
  })
})

describe('angleDeg', () => {
  it('**物理空间**里的直角（体素空间看起来不是直角）', () => {
    // axial: u 轴 0.5mm、v 轴 2mm。取 u 方向 4 格 = 2mm，v 方向 1 格 = 2mm。
    // 体素坐标上这两条边长度悬殊（4 vs 1），但物理上等长且垂直 → 90°。
    const deg = angleDeg({ u: 4, v: 0 }, { u: 0, v: 0 }, { u: 0, v: 1 }, 'axial', spacing)
    expect(deg).toBeCloseTo(90, 6)
  })

  it('不换算 spacing 会得到明显不同的角度（证明换算真的在起作用）', () => {
    // 同样三点，若按各向同性算：向量 (4,0) 与 (0,1) 仍是 90°——测不出差别。
    // 换一组：(4,0) 与 (4,1)。物理上 = (2mm,0) 与 (2mm,2mm) → 45°；
    // 体素空间里 = (4,0) 与 (4,1) → 约 14°。差 31°，一眼可辨。
    const deg = angleDeg({ u: 4, v: 0 }, { u: 0, v: 0 }, { u: 4, v: 1 }, 'axial', spacing)
    expect(deg).toBeCloseTo(45, 6)
    expect(deg).not.toBeCloseTo(14.04, 1)
  })

  it('共线：0° 与 180°', () => {
    expect(angleDeg({ u: 1, v: 0 }, { u: 0, v: 0 }, { u: 5, v: 0 }, 'axial', spacing)).toBeCloseTo(0, 6)
    expect(angleDeg({ u: -1, v: 0 }, { u: 0, v: 0 }, { u: 5, v: 0 }, 'axial', spacing)).toBeCloseTo(180, 6)
  })

  it('浮点越界不产出 NaN（真实触发 cos = 1.0000000000000002 的一组点）', () => {
    // ⚠️ 这组数字是**搜出来的**，不是编的。第一版写了个"看起来会越界"的用例
    // （沿轴共线、整数坐标），结果 cos 恰好等于 1，去掉夹逼照样通过——变异测试
    // 当场戳穿：那条测试什么也没测。
    // 于是随机搜了 300 万组共线三点，找到这一组让 (v1·v2)/(|v1||v2|) 真正 > 1；
    // 没有夹逼时 Math.acos 返回 NaN，角度显示成 "NaN°"。
    const a = { u: -17.364738328042336, v: 148.86989559297842 }
    const b = { u: -95.91946285218, v: -96.69043035246432 }
    const c = { u: -86.20408632895769, v: -66.32037889881671 }
    const d = angleDeg(a, b, c, 'axial', spacing)
    expect(Number.isNaN(d), 'cos 越界时必须夹逼，否则 acos 得 NaN').toBe(false)
    expect(d).toBeCloseTo(0, 6) // 三点共线 → 0°
  })

  it('退化（顶点与端点重合）→ 0，不抛错也不 NaN', () => {
    const d = angleDeg({ u: 0, v: 0 }, { u: 0, v: 0 }, { u: 5, v: 5 }, 'axial', spacing)
    expect(d).toBe(0)
  })

  it('角度与三点顺序无关（a、c 互换相同）', () => {
    const a = { u: 3, v: 1 }
    const b = { u: 0, v: 0 }
    const c = { u: 1, v: 4 }
    expect(angleDeg(a, b, c, 'coronal', spacing)).toBeCloseTo(angleDeg(c, b, a, 'coronal', spacing), 9)
  })
})

describe('格式化', () => {
  it('长度一位小数、单位 mm（RECIST 惯例）', () => {
    expect(formatMm(20.6155)).toBe('20.6 mm')
    expect(formatMm(120)).toBe('120.0 mm') // 超过 100 仍用 mm，不换 cm
  })

  it('角度一位小数带度号', () => {
    expect(formatDeg(45)).toBe('45.0°')
  })

  it('摘要含平面与层号（1-based，与界面一致）', () => {
    const m: Measurement = {
      kind: 'ruler', plane: 'axial', slice: 106,
      a: { u: 0, v: 0 }, b: { u: 10, v: 10 }, lengthMm: 20.6155,
    }
    expect(describeMeasurement(m)).toBe('轴位 第 107 层 · 长度 20.6 mm')
  })

  it('角度测量的摘要', () => {
    const m: Measurement = {
      kind: 'angle', plane: 'coronal', slice: 0,
      a: { u: 1, v: 0 }, b: { u: 0, v: 0 }, c: { u: 0, v: 1 }, degrees: 90,
    }
    expect(describeMeasurement(m)).toBe('冠状 第 1 层 · 角度 90.0°')
  })
})
