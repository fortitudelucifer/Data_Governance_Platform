import { describe, expect, it } from 'vitest'

import { applyWindowLevel, buildWindowLUT } from './windowLevel'

// 窗位映射是 C0.5 "16-bit 实时调窗" 的核心。用 DICOM LINEAR 公式,和放射科
// 工作站一致。这些断言钉死窗外钳制、窗内线性、以及 LUT 与标量实现一致。

describe('applyWindowLevel (DICOM LINEAR)', () => {
  // 软组织窗 W400 C40:窗 [C-0.5-(W-1)/2, C-0.5+(W-1)/2] ≈ [-159.5, 239.5].
  const W = 400
  const C = 40

  it('窗下端及以下 → 0', () => {
    expect(applyWindowLevel(-160, W, C)).toBe(0)
    expect(applyWindowLevel(-1024, W, C)).toBe(0) // 空气
  })

  it('窗上端以上 → 255', () => {
    expect(applyWindowLevel(240, W, C)).toBe(255)
    expect(applyWindowLevel(3000, W, C)).toBe(255) // 致密骨
  })

  it('窗中心 → 中灰(约 128)', () => {
    const mid = applyWindowLevel(C, W, C)
    expect(mid).toBeGreaterThanOrEqual(127)
    expect(mid).toBeLessThanOrEqual(128)
  })

  it('窗内单调非降', () => {
    let prev = -1
    for (let hu = -160; hu <= 240; hu += 5) {
      const d = applyWindowLevel(hu, W, C)
      expect(d).toBeGreaterThanOrEqual(prev)
      prev = d
    }
  })

  it('W<=1 退化为中心阈值(不除以零)', () => {
    expect(applyWindowLevel(39, 1, 40)).toBe(0)
    expect(applyWindowLevel(40, 1, 40)).toBe(255)
    expect(Number.isFinite(applyWindowLevel(40, 0, 40))).toBe(true)
  })

  it('肺窗 W1500 C-600:软组织与空气可分', () => {
    // 空气 -1000 应偏暗,软组织 0 应偏亮(窗中心是 -600)。
    expect(applyWindowLevel(-1000, 1500, -600)).toBeLessThan(applyWindowLevel(0, 1500, -600))
  })
})

describe('buildWindowLUT', () => {
  it('LUT 与标量实现逐点一致(经 slice 缩放)', () => {
    // PNG 像素 → 真值 real = p*slope + inter. int16 CT: slope 1, inter -33792
    // (= -1024 - 32768,C3.1b 的 SliceSclInter)。
    const slope = 1
    const inter = -33792
    const lut = buildWindowLUT(400, 40, slope, inter)
    for (const p of [0, 32768, 33808, 34032, 50000, 65535]) {
      const real = p * slope + inter
      expect(lut[p]).toBe(applyWindowLevel(real, 400, 40))
    }
  })

  it('覆盖全部 65536 项', () => {
    const lut = buildWindowLUT(400, 40, 1, -33792)
    expect(lut.length).toBe(65536)
    // 极小像素(≈ -33792 HU)钳到 0,极大像素钳到 255。
    expect(lut[0]).toBe(0)
    expect(lut[65535]).toBe(255)
  })
})
