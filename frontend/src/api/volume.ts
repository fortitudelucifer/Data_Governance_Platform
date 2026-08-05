import { client } from './client'
import { decodeGray16PNG } from '../lib/png16'
import { loadVolume } from '../lib/volumeLoader'
import type { Vec3 } from '../lib/mprGeometry'

// 3D 体数据(CT/MRI)的取数层(执行方案-04 · C3.1c/C3.2b)。
// 后端派生:volume_meta(单文件 JSON)+ volume_slices(前缀,逐 z 的 16-bit PNG)。

/** 与后端 service.Window 对应。 */
export interface VolumeWindow {
  name: string
  width: number
  center: number
}

/** 与后端 service.VolumeMeta 对应(字段名即 json tag)。 */
export interface VolumeMeta {
  dims: Vec3
  spacing: Vec3
  origin: Vec3
  /** 3×3 体素→世界 旋转,行优先。渲染朝向据此应用(C-H3)。 */
  direction: number[]
  /** +1 右手系,-1 左手系(单轴镜像的机器警报,见 C-H3)。 */
  handedness: number
  datatype: string
  modality: string
  scl_slope: number
  scl_inter: number
  intensity_lo: number
  intensity_hi: number
  windows: VolumeWindow[]
  /** PNG16 像素 → 真值(HU)的一次仿射:real = pixel*slope + inter。 */
  slice_scl_slope: number
  slice_scl_inter: number
}

/** 取 volume_meta(几何 + 窗位预设)。 */
export async function fetchVolumeMeta(assetId: number): Promise<VolumeMeta> {
  const { data } = await client.get<VolumeMeta>(`/assets/${assetId}/derivative/volume_meta`)
  return data
}

/** 取第 z 片的 PNG 字节。**必须拿原始字节自己解码**:走 <img>/canvas 会被截成 8-bit。 */
export async function fetchSliceBytes(assetId: number, z: number, signal?: AbortSignal): Promise<Uint8Array> {
  const { data } = await client.get<ArrayBuffer>(`/assets/${assetId}/slice/${z}`, {
    responseType: 'arraybuffer',
    signal,
  })
  return new Uint8Array(data)
}

/** 拉整卷:并发取片 → 解 16-bit → 按 z 装配。 */
export function loadAssetVolume(
  assetId: number,
  dims: Vec3,
  opts?: { concurrency?: number; onProgress?: (done: number, total: number) => void; signal?: AbortSignal },
): Promise<Uint16Array> {
  return loadVolume({
    dims,
    fetchSlice: (z) => fetchSliceBytes(assetId, z, opts?.signal),
    decode: decodeGray16PNG,
    concurrency: opts?.concurrency,
    onProgress: opts?.onProgress,
    signal: opts?.signal,
  })
}
