// volumeLoader.ts — 把逐片 PNG16 拉成一整卷 Uint16Array(执行方案-04 · C3.2b).
//
// 取数是并发的(一卷几百片,串行拉会等到天荒地老),而并发最阴的坑是**完成顺序
// 不等于请求顺序**:谁先回来就往数组里 push,切片就被打乱了——图还是"像那么回事"
// 的一摞断层,但解剖位置全错,且不报任何错。所以这里一律**按 z 下标写入**,
// 从不依赖完成顺序;单测专门用"乱序返回"的假取数器把这条钉死。
//
// 取数/解码都是注入的,所以这层纯逻辑可测,不需要真服务器、真 PNG。

import { assembleVolume } from './mprRender'
import type { Vec3 } from './mprGeometry'

export interface LoadVolumeOptions {
  dims: Vec3
  /** 取第 z 片的字节(通常是 GET /assets/:id/slice/:z)。 */
  fetchSlice: (z: number) => Promise<Uint8Array>
  /** 解码为 16-bit 像素(通常是 decodeGray16PNG)。 */
  decode: (bytes: Uint8Array) => Promise<{ width: number; height: number; pixels: Uint16Array }>
  /** 并发上限,默认 6(和浏览器同域连接数一个量级)。 */
  concurrency?: number
  /** 进度回调:已完成片数 / 总片数。 */
  onProgress?: (done: number, total: number) => void
  /** 取消信号(离开工作台时别继续拉几百片)。 */
  signal?: AbortSignal
}

/** 拉取并装配整卷。任一片失败即整体失败——半卷体数据比没有更危险。 */
export async function loadVolume(opts: LoadVolumeOptions): Promise<Uint16Array> {
  const { dims, fetchSlice, decode, concurrency = 6, onProgress, signal } = opts
  const [nx, ny, nz] = dims
  if (nx <= 0 || ny <= 0 || nz <= 0) throw new Error(`loadVolume: bad dims ${dims.join('×')}`)

  // 按 z 下标定位的槽位:并发完成顺序再乱,也只会落回自己的格子。
  const slices = new Array<Uint16Array | undefined>(nz)
  let next = 0
  let done = 0

  const worker = async () => {
    for (;;) {
      if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
      const z = next++
      if (z >= nz) return
      const bytes = await fetchSlice(z)
      const img = await decode(bytes)
      if (img.width !== nx || img.height !== ny) {
        throw new Error(`loadVolume: slice ${z} is ${img.width}×${img.height}, dims say ${nx}×${ny}`)
      }
      slices[z] = img.pixels // ← 按 z 写入,不 push
      done++
      onProgress?.(done, nz)
    }
  }

  await Promise.all(Array.from({ length: Math.min(concurrency, nz) }, worker))

  for (let z = 0; z < nz; z++) {
    if (!slices[z]) throw new Error(`loadVolume: slice ${z} missing after load`)
  }
  return assembleVolume(slices as Uint16Array[], dims)
}
