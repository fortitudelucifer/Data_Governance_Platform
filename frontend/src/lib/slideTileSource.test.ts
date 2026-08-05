import { describe, expect, it } from 'vitest'

import { buildSlideTileSource, tileExistsAt, tileGridAt } from './slideTileSource'
import type { SlideMeta } from '@/api/slide'

// C1.2c 层级映射。这里的数错了 → 瓦片取到别处组织,画面看着正常但位置全错。
// 用**真实 CMU-1.svs 的实测数字**(46000×32914,层 ×1/×4/×16,tile 256)做夹具,
// 而不是编好看的整数——真实降采样比是 ×4 不是 ×2,正是这个不规则性在考验映射。

// 真实大切片 CMU-1.svs（tools/c11c_tile_probe.py 实测）。
const cmu1: SlideMeta = {
  width: 46000,
  height: 32914,
  mpp: 0.499,
  magnification: 20,
  vendor: 'aperio',
  has_label_image: true,
  levels: [
    { width: 46000, height: 32914, tile_width: 256, tile_height: 256, downsample: 1 },
    { width: 11500, height: 8228, tile_width: 256, tile_height: 256, downsample: 4 },
    { width: 2875, height: 2057, tile_width: 256, tile_height: 256, downsample: 16 },
  ],
}

describe('buildSlideTileSource — 真实 ×4 金字塔的层映射', () => {
  it('maxLevel 覆盖最大边', () => {
    const s = buildSlideTileSource(cmu1)
    // 46000 → ceil(log2)=16（2^16=65536 ≥ 46000）
    expect(s.maxLevel).toBe(16)
    expect(s.tileSize).toBe(256)
  })

  it('原生 ×1/×4/×16 精确落在 DZI 的 maxLevel / maxLevel-2 / maxLevel-4', () => {
    const s = buildSlideTileSource(cmu1)
    // ×1=2^0 → maxLevel; ×4=2^2 → maxLevel-2; ×16=2^4 → maxLevel-4
    expect(s.dziToNative.get(16)).toBe(0) // 全分辨率
    expect(s.dziToNative.get(14)).toBe(1) // /4
    expect(s.dziToNative.get(12)).toBe(2) // /16
    expect(s.dziToNative.size).toBe(3)
    // minLevel = 最粗原生层(×16)的 DZI 层 = 12,不是 0。
    // 设成 0 会让 fit-zoom 下什么都不加载(OSD 往更粗层回退到全 false)。
    expect(s.minLevel).toBe(12)
  })

  it('中间 DZI 层（15/13/…）没有原生瓦片 → tileExists 关掉,交给 OSD 放大', () => {
    const s = buildSlideTileSource(cmu1)
    // DZI 15、13 没有原生对应 → 任何瓦片都不存在
    expect(tileExistsAt(s, cmu1, 15, 0, 0)).toBe(false)
    expect(tileExistsAt(s, cmu1, 13, 0, 0)).toBe(false)
    // 而 16/14/12 有
    expect(tileExistsAt(s, cmu1, 16, 0, 0)).toBe(true)
  })

  it('瓦片网格用原生层真实尺寸算(边缘瓦片计入)', () => {
    // level0 46000/256 = 179.7 → 180 列;32914/256 = 128.6 → 129 行
    expect(tileGridAt(cmu1, 0, 256)).toEqual({ cols: 180, rows: 129 })
    // level1 11500/256=44.9→45; 8228/256=32.1→33
    expect(tileGridAt(cmu1, 1, 256)).toEqual({ cols: 45, rows: 33 })
  })

  it('tileExists 在网格边界上正确(最后一个瓦片在、越界不在)', () => {
    const s = buildSlideTileSource(cmu1)
    // level0 DZI16:最后一列 179、最后一行 128 应存在
    expect(tileExistsAt(s, cmu1, 16, 179, 128)).toBe(true)
    // 越界一格不存在——否则查看器请求会 404 或读到别的瓦片
    expect(tileExistsAt(s, cmu1, 16, 180, 0)).toBe(false)
    expect(tileExistsAt(s, cmu1, 16, 0, 129)).toBe(false)
    expect(tileExistsAt(s, cmu1, 16, -1, 0)).toBe(false)
  })
})

describe('buildSlideTileSource — 单层切片(CMU-1-Small)', () => {
  const small: SlideMeta = {
    width: 2220,
    height: 2967,
    mpp: 0.499,
    magnification: 20,
    vendor: 'aperio',
    has_label_image: true,
    levels: [{ width: 2220, height: 2967, tile_width: 240, tile_height: 240, downsample: 1 }],
  }
  it('单层 → 只有 maxLevel 有原生瓦片,tileSize 取该层(240)', () => {
    const s = buildSlideTileSource(small)
    expect(s.tileSize).toBe(240)
    expect(s.maxLevel).toBe(12) // ceil(log2(2967))=12
    expect(s.dziToNative.get(12)).toBe(0)
    expect(s.dziToNative.size).toBe(1)
    expect(s.minLevel).toBe(12) // 单层 → min=max=12,OSD 只用全分辨率、缩放填充
  })
  it('比全分辨率更粗的 DZI 层没有原生瓦片(靠 OSD 从 level0 缩)', () => {
    const s = buildSlideTileSource(small)
    expect(tileExistsAt(s, small, 12, 0, 0)).toBe(true)
    expect(tileExistsAt(s, small, 8, 0, 0)).toBe(false)
  })
})

describe('buildSlideTileSource — 边界与容错', () => {
  it('没有金字塔层 → 抛错(不是 WSI)', () => {
    const bad = { ...cmu1, levels: [] }
    expect(() => buildSlideTileSource(bad)).toThrow()
  })

  it('非 2 的幂降采样 → 四舍五入到最近 DZI 层,不抛错', () => {
    // 假想一个 ×3 层（个别厂商）：log2(3)=1.58 → round=2 → maxLevel-2
    const odd: SlideMeta = {
      ...cmu1,
      levels: [
        { width: 46000, height: 32914, tile_width: 256, tile_height: 256, downsample: 1 },
        { width: 15333, height: 10971, tile_width: 256, tile_height: 256, downsample: 3 },
      ],
    }
    const s = buildSlideTileSource(odd)
    expect(s.dziToNative.get(16)).toBe(0)
    // ×3 落到 maxLevel-2（最近的 2 的幂 =4）——最坏是该层不被完美命中,但不 404
    expect(s.dziToNative.has(14)).toBe(true)
  })
})
