import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import { decodeGray16PNG } from './png16'

// 跨语言锁的 TS 那一半:夹具 testdata/volume/gray16_ramp.png 由**生产 Go 编码器**
// encodeSlicePNG16 产出(Go 侧 volume_slice_fixture_test.go 断言编码器仍能逐字节
// 重现它)。这里断言 TS 解码器从同一份字节读出同一批 16-bit 值。
// 任一端读错字节序/滤波/位深,两个测试里必有一个红。

const HERE = dirname(fileURLToPath(import.meta.url))
const FIXTURE = join(HERE, '..', '..', '..', 'testdata', 'volume', 'gray16_ramp.png')

// 与 Go 侧 gray16FixtureValues 同一批(行优先,x 最快)。刻意含:
//  · >255 的值 → 8-bit 截断会暴露;
//  · 258(0x0102) 与 513(0x0201) 互为字节交换 → 字节序读反会暴露;
//  · 0 与 65535 → 边界。
const WANT = [1000, 6000, 11000, 16000, 258, 513, 32768, 40000, 65535, 0, 4660, 43981]
const W = 4
const H = 3

describe('decodeGray16PNG（与 Go 编码器共享夹具）', () => {
  it('尺寸与像素值逐点还原', async () => {
    const img = await decodeGray16PNG(new Uint8Array(readFileSync(FIXTURE)))
    expect([img.width, img.height]).toEqual([W, H])
    expect(img.pixels.length).toBe(W * H)
    expect([...img.pixels]).toEqual(WANT)
  })

  it('保住 16-bit：存在 >255 的值（8-bit 截断会让它们全挤到 255）', async () => {
    const img = await decodeGray16PNG(new Uint8Array(readFileSync(FIXTURE)))
    expect([...img.pixels].filter((v) => v > 255).length).toBeGreaterThan(5)
    expect(img.pixels[8]).toBe(65535) // 满量程原样保留
  })

  it('字节序正确：0x0102 与 0x0201 没被读反', async () => {
    const img = await decodeGray16PNG(new Uint8Array(readFileSync(FIXTURE)))
    expect(img.pixels[4]).toBe(258) // 0x0102
    expect(img.pixels[5]).toBe(513) // 0x0201
  })

  it('非 PNG 输入 → 抛错（不猜、不将就）', async () => {
    await expect(decodeGray16PNG(new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8, 9]))).rejects.toThrow(/bad magic/)
  })

  it('位深/颜色类型不符 → 抛错', async () => {
    const bytes = new Uint8Array(readFileSync(FIXTURE))
    const bad = bytes.slice()
    bad[8 + 8 + 8] = 8 // IHDR.bitDepth 16 → 8
    await expect(decodeGray16PNG(bad)).rejects.toThrow(/bit depth 8/)

    const bad2 = bytes.slice()
    bad2[8 + 8 + 9] = 2 // IHDR.colorType 0 → 2 (truecolor)
    await expect(decodeGray16PNG(bad2)).rejects.toThrow(/color type 2/)
  })
})

// 第二份夹具专为**去滤波分支覆盖**而造:Go 的编码器按行择优,常量行选 Up、
// 斜坡选 Sub、二维渐变选 Paeth、伪随机选 None。真实 512² CT 切片必然走到
// Up/Paeth——那两个分支若写错,解出来"像图但像素全错",不报任何错。
const FILTERS_FIXTURE = join(HERE, '..', '..', '..', 'testdata', 'volume', 'gray16_filters.png')
const FW = 16
const FH = 16

/** 与 Go 侧 filtersFixtureValue 同一份生成式。 */
function filtersValue(x: number, y: number): number {
  if (y < 4) return 3000 + y * 7
  if (y < 8) return 500 + x * 400
  if (y < 12) return 1000 + x * 300 + y * 250
  return (x * 7919 + y * 104729) % 65536
}

/** 取出各扫描行的 filter 类型(自己 inflate,不动 png16.ts 的对外形状)。 */
async function filterTypesOf(bytes: Uint8Array, width: number, height: number): Promise<number[]> {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
  let off = 8
  const idat: Uint8Array[] = []
  while (off + 8 <= bytes.length) {
    const len = view.getUint32(off)
    const type = String.fromCharCode(bytes[off + 4], bytes[off + 5], bytes[off + 6], bytes[off + 7])
    if (type === 'IDAT') idat.push(bytes.subarray(off + 8, off + 8 + len))
    if (type === 'IEND') break
    off += 8 + len + 4
  }
  const merged = new Uint8Array(idat.reduce((n, p) => n + p.length, 0))
  let at = 0
  for (const p of idat) {
    merged.set(p, at)
    at += p.length
  }
  const stream = new Blob([merged as BlobPart]).stream().pipeThrough(new DecompressionStream('deflate'))
  const raw = new Uint8Array(await new Response(stream).arrayBuffer())
  const stride = width * 2
  return Array.from({ length: height }, (_, y) => raw[y * (stride + 1)])
}

describe('decodeGray16PNG — 去滤波分支覆盖（第二份夹具）', () => {
  it('逐点还原 16×16 全部像素（覆盖 Sub/Up/Paeth 行）', async () => {
    const img = await decodeGray16PNG(new Uint8Array(readFileSync(FILTERS_FIXTURE)))
    expect([img.width, img.height]).toEqual([FW, FH])
    for (let y = 0; y < FH; y++) {
      for (let x = 0; x < FW; x++) {
        expect(img.pixels[x + y * FW], `pixel(${x},${y})`).toBe(filtersValue(x, y))
      }
    }
  })

  it('两份夹具合起来覆盖全部 5 种 filter（覆盖本身也被锁住）', async () => {
    const a = await filterTypesOf(new Uint8Array(readFileSync(FIXTURE)), W, H)
    const b = await filterTypesOf(new Uint8Array(readFileSync(FILTERS_FIXTURE)), FW, FH)
    const seen = new Set([...a, ...b])
    for (const ft of [0, 1, 2, 3, 4]) {
      expect(seen.has(ft), `filter type ${ft} 没有任何夹具覆盖 —— 该去滤波分支在裸奔`).toBe(true)
    }
  })
})
