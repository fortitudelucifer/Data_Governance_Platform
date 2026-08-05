import { client } from './client'

// 病理全切片(WSI)取数层(C1.2)。后端派生 slide_meta(几何/mpp/附属图),
// 瓦片按需从原片取(C1.1c 定案:不预生成 DZI)。

/** 与后端 service.SlideLevel 对应。 */
export interface SlideLevel {
  width: number
  height: number
  tile_width: number
  tile_height: number
  /** level0Width / levelWidth（≈1,4,16…；厂商不保证 2 的幂，查看器必须用这个）。 */
  downsample: number
}

/** 与后端 service.SlideMeta 对应。 */
export interface SlideMeta {
  width: number
  height: number
  levels: SlideLevel[]
  /** 微米/像素。**0 = 未知**——为 0 时不得显示物理单位（后端绝不默认 1.0）。 */
  mpp: number
  magnification: number
  vendor: string
  associated?: string[]
  /** 切片标签/宏观照可能印着病人标识（PHI）。真实临床切片导入前须剥离(D-1b)。 */
  has_label_image: boolean
}

export async function fetchSlideMeta(assetId: number): Promise<SlideMeta> {
  const res = await client.get<SlideMeta>(`/assets/${assetId}/derivative/slide_meta`)
  return res.data
}

/** 瓦片端点 URL（相对，供 OpenSeadragon 的 ajax 加载器带 JWT 请求）。 */
export function tileUrl(assetId: number, level: number, col: number, row: number): string {
  return `/api/assets/${assetId}/tile/${level}/${col}/${row}`
}
