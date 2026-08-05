import { describe, expect, it } from 'vitest'

import { formatMicrons, lengthMicrons } from './slideMeasure'

// C1.35 测量。用**能算出期望值**的夹具(3-4-5、水平线),别写"合理区间"(C3.25 教训)。

describe('slideMeasure — 长度(µm)', () => {
  it('水平线:100 像素 × mpp 0.5 = 50µm', () => {
    // 漏乘 mpp → 100;算错 mpp → 别的数;都不是 50。
    expect(lengthMicrons(0, 0, 100, 0, 0.5)).toBeCloseTo(50, 9)
  })

  it('斜线 3-4-5:(0,0)→(3,4) × mpp 2 = 10µm(逐轴平方和,不是逐轴各乘)', () => {
    expect(lengthMicrons(0, 0, 3, 4, 2)).toBeCloseTo(10, 9)
  })

  it('真实量级:CMU-1 mpp 0.499,500 像素 ≈ 249.5µm', () => {
    expect(lengthMicrons(1000, 2000, 1500, 2000, 0.499)).toBeCloseTo(249.5, 6)
  })

  it('mpp 未知(0 或负)→ null,绝不返回 0 或编数', () => {
    // 这条锁死 C1.1a 纪律:探针拿不到 MPP 置 0,测量必须禁用。
    expect(lengthMicrons(0, 0, 100, 0, 0)).toBeNull()
    expect(lengthMicrons(0, 0, 100, 0, -1)).toBeNull()
    expect(lengthMicrons(0, 0, 100, 0, NaN)).toBeNull()
  })

  it('formatMicrons:µm/mm 分界在 1000', () => {
    expect(formatMicrons(50)).toBe('50.0 µm')
    expect(formatMicrons(999)).toBe('999.0 µm')
    expect(formatMicrons(1000)).toBe('1.00 mm')
    expect(formatMicrons(2495)).toBe('2.50 mm')
  })
})
