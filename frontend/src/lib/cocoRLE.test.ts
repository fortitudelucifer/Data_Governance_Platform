import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import { decodeCOCORLE, encodeCOCORLE, MAX_KEYFRAME_RLE_BYTES, MAX_TRACK_GEOM_BYTES } from './cocoRLE'

// 三方锁的 TS 那一半:夹具 testdata/rle/shapes.json 的 counts 由**参考实现
// pycocotools** 产出并核验;Go 侧 coco_rle_test.go 断言 Go 编码器与之一致,
// 这里断言 TS 编码器也一致、且能解回同一掩膜。
// 往返测试只能证明自洽——自洽但非标的编码器照样往返成功,直到导出的掩膜
// 拿到 3D Slicer 里才发现全错。所以必须钉在参考实现上。

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURE = join(HERE, '..', '..', '..', 'testdata', 'rle', 'shapes.json')

interface Shape {
  name: string
  h: number
  w: number
  rows: string[]
  counts: string
}
const shapes: Shape[] = JSON.parse(readFileSync(FIXTURE, 'utf8')).shapes

function maskFromRows(rows: string[]): { mask: Uint8Array; h: number; w: number } {
  const h = rows.length
  const w = rows[0].length
  const mask = new Uint8Array(h * w)
  rows.forEach((r, y) => {
    for (let x = 0; x < w; x++) if (r[x] === '#') mask[y * w + x] = 1
  })
  return { mask, h, w }
}
function rowsFromMask(mask: Uint8Array, h: number, w: number): string[] {
  return Array.from({ length: h }, (_, y) =>
    Array.from({ length: w }, (_, x) => (mask[y * w + x] ? '#' : '.')).join(''),
  )
}

describe('cocoRLE — 与参考实现 pycocotools 的夹具一致', () => {
  it('夹具非空(空了这道锁就没了)', () => {
    expect(shapes.length).toBeGreaterThan(0)
  })

  for (const s of shapes) {
    it(`${s.name}: 编码位串与 pycocotools 逐字节一致`, () => {
      const { mask, h, w } = maskFromRows(s.rows)
      expect([h, w]).toEqual([s.h, s.w])
      const rle = encodeCOCORLE(mask, h, w)
      expect(rle.size).toEqual([s.h, s.w])
      expect(rle.counts, '偏离参考实现 → 导出的掩膜到 3D Slicer 里会是错的').toBe(s.counts)
    })

    it(`${s.name}: 解参考位串还原同一掩膜`, () => {
      const { mask, h, w } = decodeCOCORLE({ size: [s.h, s.w], counts: s.counts })
      expect(rowsFromMask(mask, h, w)).toEqual(s.rows)
    })
  }
})

describe('cocoRLE — 往返与边界', () => {
  it('随机掩膜往返无损(含全 0 / 全 1 / 稀疏)', () => {
    const h = 17
    const w = 23 // 刻意非方、非 8 的倍数
    for (const density of [0, 0.03, 0.5, 1]) {
      const mask = new Uint8Array(h * w)
      let seed = 12345
      for (let i = 0; i < mask.length; i++) {
        seed = (seed * 1103515245 + 12345) & 0x7fffffff
        mask[i] = seed / 0x7fffffff < density ? 1 : 0
      }
      const back = decodeCOCORLE(encodeCOCORLE(mask, h, w))
      expect([...back.mask], `density=${density}`).toEqual([...mask])
    }
  })

  it('column-major 展平:单点位置能区分行列(转置会被抓到)', () => {
    // 只有 (y=0, x=1) 亮。column-major 下首个 0 游程长度 = h。
    const h = 3
    const w = 4
    const mask = new Uint8Array(h * w)
    mask[0 * w + 1] = 1
    const rle = encodeCOCORLE(mask, h, w)
    const back = decodeCOCORLE(rle)
    expect(back.mask[0 * w + 1]).toBe(1)
    expect(back.mask[1 * w + 0]).toBe(0) // 转置的话这里会是 1
  })

  it('尺寸与位串不符 → 抛错(不铺歪掩膜)', () => {
    expect(() => decodeCOCORLE({ size: [4, 4], counts: encodeCOCORLE(new Uint8Array(9), 3, 3).counts })).toThrow(
      /runs sum to/,
    )
    expect(() => decodeCOCORLE({ size: [0, 4], counts: '' })).toThrow(/bad size/)
    expect(() => encodeCOCORLE(new Uint8Array(5), 2, 3)).toThrow(/want 6/)
  })

  it('护栏常量与后端同值(契约规则 1)', () => {
    expect(MAX_KEYFRAME_RLE_BYTES).toBe(64 * 1024)
    expect(MAX_TRACK_GEOM_BYTES).toBe(4 * 1024 * 1024)
  })
})
