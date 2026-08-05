import { describe, expect, it } from 'vitest'

import { affineToMatrix, imageToScreen, recoverAffine, screenToImage, type OverlayAffine } from './osdOverlayTransform'

// C1.3a 叠加层坐标同步。这套数学错了 → 标注框落在错的组织上,画面看着正常却是错的
// (坐标系纪律)。用能区分对错的数字做夹具:scale≠1、offset≠0,把"漏了平移/漏了缩放"
// 这类变异逼出来。

describe('osdOverlayTransform — image↔screen 仿射', () => {
  const a: OverlayAffine = { scale: 2, ox: 100, oy: 50 }

  it('imageToScreen 同时用到 scale 与 offset', () => {
    // 漏乘 scale → (600,350);漏加 offset → (1000,600);都与正解不同。
    expect(imageToScreen(a, 500, 300)).toEqual([1100, 650])
    expect(imageToScreen(a, 0, 0)).toEqual([100, 50]) // image 原点落在 offset 上
  })

  it('screenToImage 是 imageToScreen 的逆(往返恒等)', () => {
    for (const [x, y] of [[0, 0], [500, 300], [46000, 32914], [123.5, 987.25]]) {
      const [sx, sy] = imageToScreen(a, x, y)
      const [bx, by] = screenToImage(a, sx, sy)
      expect(bx).toBeCloseTo(x, 9)
      expect(by).toBeCloseTo(y, 9)
    }
  })

  it('recoverAffine 从两点屏幕位置反解 scale/offset', () => {
    // image(0,0)→屏幕(100,50), image(1,0)→屏幕(102,50):scale=2,原点(100,50)。
    const r = recoverAffine({ x: 100, y: 50 }, { x: 102, y: 50 })
    expect(r).toEqual({ scale: 2, ox: 100, oy: 50 })
    // 反解出的仿射把 image(1,0) 送回屏幕(102,50)——自洽。
    expect(imageToScreen(r, 1, 0)).toEqual([102, 50])
  })

  it('recoverAffine:两点重合 → 抛错(视口未就绪,绝不返回 scale=0)', () => {
    // scale=0 会让 screenToImage 除零得到 Infinity,后续几何全污染 → 宁可大声失败。
    expect(() => recoverAffine({ x: 100, y: 50 }, { x: 100, y: 50 })).toThrow()
  })

  it('affineToMatrix 输出 SVG 可用的 matrix 串', () => {
    expect(affineToMatrix(a)).toBe('matrix(2,0,0,2,100,50)')
  })

  it('缩放后一个已知 image 点的屏幕位置随 scale 改变(证明不是静态)', () => {
    // 同一 image 点 (200,200),放大一倍(scale 2→4、原点也随 OSD 平移)后屏幕位置必变。
    const zoomedIn: OverlayAffine = { scale: 4, ox: -100, oy: -100 }
    const before = imageToScreen(a, 200, 200) // (500,450)
    const after = imageToScreen(zoomedIn, 200, 200) // (700,700)
    expect(after).not.toEqual(before)
  })
})
