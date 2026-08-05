import { describe, expect, it } from 'vitest'

import { planeDims, type Vec3 } from './mprGeometry'
import { assembleVolume, renderPlane } from './mprRender'

// 重切 + 窗位 → RGBA 的合成锁。用**维度全不同**的体数据(2×3×4)且每个体素值
// 唯一可辨(i*100+j*10+k),轴混淆/转置/切片错位都藏不住——立方体或常量体
// 测不出这些(转置了也"看着对"),那正是本项目最怕的不报错的错。

const dims: Vec3 = [2, 3, 4] // nx=2, ny=3, nz=4
const NX = 2,
  NY = 3,
  NZ = 4

/** 体素值编码:i*100 + j*10 + k(最大 123,< 255,可被恒等 LUT 原样显示)。 */
const voxelValue = (i: number, j: number, k: number) => i * 100 + j * 10 + k

/** 恒等 LUT(钳到 255):显示灰阶 == 体素值,断言才能直接读出来源坐标。 */
const identityLUT = (() => {
  const l = new Uint8Array(65536)
  for (let p = 0; p < 65536; p++) l[p] = Math.min(p, 255)
  return l
})()

/** 逐 z 的 axial 切片(面内 x 最快),与后端 PNG 布局一致。 */
function makeSlices(): Uint16Array[] {
  const out: Uint16Array[] = []
  for (let k = 0; k < NZ; k++) {
    const s = new Uint16Array(NX * NY)
    for (let j = 0; j < NY; j++) {
      for (let i = 0; i < NX; i++) s[i + j * NX] = voxelValue(i, j, k)
    }
    out.push(s)
  }
  return out
}

const volume = assembleVolume(makeSlices(), dims)

/** 读某平面渲染结果的灰阶(r 通道)。 */
const grayAt = (r: { rgba: Uint8ClampedArray; width: number }, u: number, v: number) =>
  r.rgba[(v * r.width + u) * 4]

describe('assembleVolume', () => {
  it('按 z 顺序拼接,column-major 下标可寻回原体素', () => {
    for (let k = 0; k < NZ; k++) {
      for (let j = 0; j < NY; j++) {
        for (let i = 0; i < NX; i++) {
          expect(volume[i + j * NX + k * NX * NY]).toBe(voxelValue(i, j, k))
        }
      }
    }
  })

  it('切片数与 nz 不符 → 抛错(宁可炸也不静默错位)', () => {
    expect(() => assembleVolume(makeSlices().slice(0, 2), dims)).toThrow(/nz=4/)
  })

  it('单片长度不符 → 抛错', () => {
    const bad = makeSlices()
    bad[1] = new Uint16Array(NX * NY - 1)
    expect(() => assembleVolume(bad, dims)).toThrow(/slice 1/)
  })
})

describe('renderPlane — 面内尺寸与 planeDims 一致', () => {
  for (const plane of ['axial', 'coronal', 'sagittal'] as const) {
    it(`${plane} 的 width/height 等于 planeDims`, () => {
      const r = renderPlane(volume, dims, plane, 0, identityLUT)
      const [w, h] = planeDims(dims, plane)
      expect([r.width, r.height]).toEqual([w, h])
      expect(r.rgba.length).toBe(w * h * 4)
    })
  }
})

describe('renderPlane — 重切取值正确(每个像素能读回它的源体素)', () => {
  it('axial(固定 k):像素(u,v) = 体素(u,v,k)', () => {
    const k = 2
    const r = renderPlane(volume, dims, 'axial', k, identityLUT)
    for (let v = 0; v < NY; v++) {
      for (let u = 0; u < NX; u++) {
        expect(grayAt(r, u, v)).toBe(voxelValue(u, v, k))
      }
    }
  })

  it('coronal(固定 j):像素(u,v) = 体素(u,j,v)', () => {
    const j = 1
    const r = renderPlane(volume, dims, 'coronal', j, identityLUT)
    for (let v = 0; v < NZ; v++) {
      for (let u = 0; u < NX; u++) {
        expect(grayAt(r, u, v)).toBe(voxelValue(u, j, v))
      }
    }
  })

  it('sagittal(固定 i):像素(u,v) = 体素(i,u,v)', () => {
    const i = 1
    const r = renderPlane(volume, dims, 'sagittal', i, identityLUT)
    for (let v = 0; v < NZ; v++) {
      for (let u = 0; u < NY; u++) {
        expect(grayAt(r, u, v)).toBe(voxelValue(i, u, v))
      }
    }
  })

  it('三视在同一体素相交处读到同一个值', () => {
    const [i, j, k] = [1, 2, 3]
    const a = renderPlane(volume, dims, 'axial', k, identityLUT)
    const c = renderPlane(volume, dims, 'coronal', j, identityLUT)
    const s = renderPlane(volume, dims, 'sagittal', i, identityLUT)
    const want = voxelValue(i, j, k)
    expect(grayAt(a, i, j)).toBe(want)
    expect(grayAt(c, i, k)).toBe(want)
    expect(grayAt(s, j, k)).toBe(want)
  })
})

describe('renderPlane — RGBA 与 LUT', () => {
  it('灰度写满 r=g=b,alpha 不透明', () => {
    const r = renderPlane(volume, dims, 'axial', 1, identityLUT)
    for (let p = 0; p < r.width * r.height; p++) {
      const o = p * 4
      expect(r.rgba[o + 1]).toBe(r.rgba[o])
      expect(r.rgba[o + 2]).toBe(r.rgba[o])
      expect(r.rgba[o + 3]).toBe(255)
    }
  })

  it('LUT 真的被应用(换一张反相 LUT,输出随之反相)', () => {
    const inverted = new Uint8Array(65536)
    for (let p = 0; p < 65536; p++) inverted[p] = 255 - Math.min(p, 255)
    const a = renderPlane(volume, dims, 'axial', 0, identityLUT)
    const b = renderPlane(volume, dims, 'axial', 0, inverted)
    expect(grayAt(b, 1, 2)).toBe(255 - grayAt(a, 1, 2))
  })

  it('越界切片号 → 全黑但不崩(调用方钳制前的安全网)', () => {
    const r = renderPlane(volume, dims, 'axial', 99, identityLUT)
    expect(r.rgba.every((x, idx) => (idx % 4 === 3 ? x === 255 : x === 0))).toBe(true)
  })
})

describe('renderPlane — 掩膜叠加层', () => {
  const overlayVol = (() => {
    // 只在体素 (1,2,3) 处有掩膜。
    const v = new Uint8Array(NX * NY * NZ)
    v[1 + 2 * NX + 3 * NX * NY] = 1
    return v
  })()
  const RED: [number, number, number] = [255, 0, 0]

  it('掩膜像素被染色,其余像素不受影响', () => {
    const plain = renderPlane(volume, dims, 'axial', 3, identityLUT)
    const tinted = renderPlane(volume, dims, 'axial', 3, identityLUT, [{ volume: overlayVol, rgb: RED, alpha: 1 }])
    // (u=1, v=2) 是掩膜所在 → 纯红
    const o = (2 * NX + 1) * 4
    expect([tinted.rgba[o], tinted.rgba[o + 1], tinted.rgba[o + 2]]).toEqual([255, 0, 0])
    // 别的像素与不带叠加层时一致
    const o2 = (0 * NX + 0) * 4
    expect(tinted.rgba[o2]).toBe(plain.rgba[o2])
  })

  it('alpha 半透明是与底图的混合', () => {
    const r = renderPlane(volume, dims, 'axial', 3, identityLUT, [{ volume: overlayVol, rgb: RED, alpha: 0.5 }])
    const base = grayAt(renderPlane(volume, dims, 'axial', 3, identityLUT), 1, 2)
    const o = (2 * NX + 1) * 4
    // Uint8ClampedArray 会把 .5 就近取整,所以容差给 1(断言的是"确实做了混合",
    // 不是"浮点精确"——混合公式本身由上一条纯色测试钉住)。
    expect(Math.abs(r.rgba[o] - (base * 0.5 + 255 * 0.5))).toBeLessThanOrEqual(1)
    expect(Math.abs(r.rgba[o + 1] - base * 0.5)).toBeLessThanOrEqual(1)
    // 混合后必须严格介于底图与纯色之间(否则等于没混合)。
    expect(r.rgba[o]).toBeGreaterThan(base)
    expect(r.rgba[o]).toBeLessThan(255)
  })

  it('掩膜与影像在三视里走同一个体素下标(结构性对齐)', () => {
    // 同一个体素 (1,2,3),在三个平面各自的 (u,v) 处都应被染色。
    for (const [plane, slice, u, v] of [
      ['axial', 3, 1, 2],
      ['coronal', 2, 1, 3],
      ['sagittal', 1, 2, 3],
    ] as const) {
      const r = renderPlane(volume, dims, plane, slice, identityLUT, [{ volume: overlayVol, rgb: RED, alpha: 1 }])
      const o = (v * r.width + u) * 4
      expect([r.rgba[o], r.rgba[o + 1]], `${plane}`).toEqual([255, 0])
    }
  })

  it('不给叠加层时行为不变(回归)', () => {
    const a = renderPlane(volume, dims, 'axial', 1, identityLUT)
    const b = renderPlane(volume, dims, 'axial', 1, identityLUT, undefined)
    expect([...a.rgba]).toEqual([...b.rgba])
  })
})

describe('renderPlane — 显示翻转(C-H3 朝向)', () => {
  it('flipU 水平镜像、flipV 垂直镜像', () => {
    const plain = renderPlane(volume, dims, 'axial', 2, identityLUT)
    const fu = renderPlane(volume, dims, 'axial', 2, identityLUT, undefined, { flipU: true, flipV: false })
    const fv = renderPlane(volume, dims, 'axial', 2, identityLUT, undefined, { flipU: false, flipV: true })
    // 原图 (0,0) 的值应出现在水平镜像的 (W-1,0)、垂直镜像的 (0,H-1)
    expect(grayAt(fu, NX - 1, 0)).toBe(grayAt(plain, 0, 0))
    expect(grayAt(fv, 0, NY - 1)).toBe(grayAt(plain, 0, 0))
  })

  it('掩膜随影像一起翻转(不会各翻各的)', () => {
    const ov = new Uint8Array(NX * NY * NZ)
    ov[0 + 0 * NX + 2 * NX * NY] = 1 // 体素 (0,0,2)
    const r = renderPlane(volume, dims, 'axial', 2, identityLUT, [{ volume: ov, rgb: [255, 0, 0], alpha: 1 }], {
      flipU: true,
      flipV: true,
    })
    // 翻转后该体素应出现在右下角
    const o = ((NY - 1) * r.width + (NX - 1)) * 4
    expect([r.rgba[o], r.rgba[o + 1]]).toEqual([255, 0])
  })

  it('不给 flip 时与旧行为一致(回归)', () => {
    const a = renderPlane(volume, dims, 'coronal', 1, identityLUT)
    const b = renderPlane(volume, dims, 'coronal', 1, identityLUT, undefined, { flipU: false, flipV: false })
    expect([...a.rgba]).toEqual([...b.rgba])
  })
})

describe('renderPlane — 多分割叠加', () => {
  it('多层按顺序叠加,后者覆盖前者(重叠处显示上层颜色)', () => {
    const a = new Uint8Array(NX * NY * NZ)
    const b = new Uint8Array(NX * NY * NZ)
    const at = 1 + 2 * NX + 3 * NX * NY // 体素 (1,2,3)
    a[at] = 1
    b[at] = 1 // 两段重叠
    const r = renderPlane(volume, dims, 'axial', 3, identityLUT, [
      { volume: a, rgb: [255, 0, 0], alpha: 1 },
      { volume: b, rgb: [0, 0, 255], alpha: 1 },
    ])
    const o = (2 * NX + 1) * 4
    expect([r.rgba[o], r.rgba[o + 2]]).toEqual([0, 255]) // 蓝(后一层)胜出
  })

  it('各段只染自己的体素', () => {
    const a = new Uint8Array(NX * NY * NZ)
    const b = new Uint8Array(NX * NY * NZ)
    a[0 + 0 * NX + 3 * NX * NY] = 1 // (0,0,3)
    b[1 + 1 * NX + 3 * NX * NY] = 1 // (1,1,3)
    const r = renderPlane(volume, dims, 'axial', 3, identityLUT, [
      { volume: a, rgb: [255, 0, 0], alpha: 1 },
      { volume: b, rgb: [0, 255, 0], alpha: 1 },
    ])
    const oa = (0 * NX + 0) * 4
    const ob = (1 * NX + 1) * 4
    expect([r.rgba[oa], r.rgba[oa + 1]]).toEqual([255, 0])
    expect([r.rgba[ob], r.rgba[ob + 1]]).toEqual([0, 255])
  })

  it('空数组 / 不传 → 与无叠加一致(隐藏所有段就是干净底图)', () => {
    const plain = renderPlane(volume, dims, 'axial', 1, identityLUT)
    expect([...renderPlane(volume, dims, 'axial', 1, identityLUT, []).rgba]).toEqual([...plain.rgba])
  })
})
