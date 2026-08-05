import { describe, expect, it, vi } from 'vitest'

import type { Vec3 } from './mprGeometry'
import { loadVolume } from './volumeLoader'

// 并发取片最阴的坑:完成顺序 ≠ 请求顺序。若按完成顺序收集,切片会被打乱——
// 一摞看着正常的断层,解剖位置全错,且不报错。这里用"故意乱序返回"的假取数器
// 把"按 z 落位"钉死。

const dims: Vec3 = [2, 2, 6] // nx=2, ny=2, nz=6
const NX = 2,
  NY = 2,
  NZ = 6

/** 第 z 片的像素:每个体素都带着自己的 z,装错位置立刻能看出来。 */
const sliceFor = (z: number) => Uint16Array.from({ length: NX * NY }, (_, m) => z * 100 + m)

/** 假取数:返回一个标记了 z 的字节;延迟故意让高 z 先完成(完全逆序)。 */
function makeFetch(delayFor: (z: number) => number) {
  return async (z: number) => {
    await new Promise((r) => setTimeout(r, delayFor(z)))
    return new Uint8Array([z])
  }
}
const decode = async (bytes: Uint8Array) => ({
  width: NX,
  height: NY,
  pixels: sliceFor(bytes[0]),
})

describe('loadVolume', () => {
  it('乱序完成也按 z 正确落位（并发装配的核心）', async () => {
    // 高 z 先回来(z=5 最快,z=0 最慢),完全逆序完成。
    const vol = await loadVolume({ dims, fetchSlice: makeFetch((z) => (NZ - z) * 4), decode, concurrency: 6 })
    for (let z = 0; z < NZ; z++) {
      for (let m = 0; m < NX * NY; m++) {
        expect(vol[z * NX * NY + m], `voxel m=${m} of slice ${z}`).toBe(z * 100 + m)
      }
    }
  })

  it('串行(concurrency=1)与并发结果一致', async () => {
    const a = await loadVolume({ dims, fetchSlice: makeFetch(() => 0), decode, concurrency: 1 })
    const b = await loadVolume({ dims, fetchSlice: makeFetch((z) => (NZ - z) * 3), decode, concurrency: 6 })
    expect([...a]).toEqual([...b])
  })

  it('每片恰好取一次，且覆盖 0..nz-1', async () => {
    const seen: number[] = []
    const fetchSlice = async (z: number) => {
      seen.push(z)
      return new Uint8Array([z])
    }
    await loadVolume({ dims, fetchSlice, decode, concurrency: 3 })
    expect(seen.sort((x, y) => x - y)).toEqual([0, 1, 2, 3, 4, 5])
  })

  it('并发有上限（不把几百片一次全发出去）', async () => {
    let inFlight = 0
    let peak = 0
    const fetchSlice = async (z: number) => {
      inFlight++
      peak = Math.max(peak, inFlight)
      await new Promise((r) => setTimeout(r, 2))
      inFlight--
      return new Uint8Array([z])
    }
    await loadVolume({ dims, fetchSlice, decode, concurrency: 2 })
    expect(peak).toBeLessThanOrEqual(2)
  })

  it('进度回调数到总片数', async () => {
    const onProgress = vi.fn()
    await loadVolume({ dims, fetchSlice: makeFetch(() => 0), decode, concurrency: 2, onProgress })
    expect(onProgress).toHaveBeenCalledTimes(NZ)
    expect(onProgress).toHaveBeenLastCalledWith(NZ, NZ)
  })

  it('任一片失败 → 整体失败（半卷体数据比没有更危险）', async () => {
    const fetchSlice = async (z: number) => {
      if (z === 3) throw new Error('boom on slice 3')
      return new Uint8Array([z])
    }
    await expect(loadVolume({ dims, fetchSlice, decode, concurrency: 2 })).rejects.toThrow(/boom on slice 3/)
  })

  it('切片尺寸与 dims 不符 → 抛错（不静默拼出错卷）', async () => {
    const badDecode = async () => ({
      width: NX + 1,
      height: NY,
      pixels: new Uint16Array((NX + 1) * NY),
    })
    await expect(
      loadVolume({ dims, fetchSlice: makeFetch(() => 0), decode: badDecode, concurrency: 2 }),
    ).rejects.toThrow(/dims say 2×2/)
  })

  it('取消信号 → 中止', async () => {
    const ac = new AbortController()
    ac.abort()
    await expect(
      loadVolume({ dims, fetchSlice: makeFetch(() => 0), decode, concurrency: 2, signal: ac.signal }),
    ).rejects.toThrow(/aborted/)
  })

  it('非法 dims → 抛错', async () => {
    await expect(
      loadVolume({ dims: [0, 2, 3], fetchSlice: makeFetch(() => 0), decode }),
    ).rejects.toThrow(/bad dims/)
  })
})
