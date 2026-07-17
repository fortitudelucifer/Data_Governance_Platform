import { client } from './client'

// 服务端预计算波形峰值（M0 T0.3 派生物）。格式贴近 audiowaveform/waveform-data：
// { bits, channels, data:[min0,max0,min1,max1,...] }（int16 交错 min/max）。
export interface WaveformData {
  bits: number
  channels: number
  data: number[]
  sample_rate?: number
  length?: number
}

export const audioApi = {
  // 波形峰值派生物（asset.preprocess_status=ready 后可取）。
  getWaveform: (assetId: number) =>
    client.get<WaveformData>(`/assets/${assetId}/derivative/waveform`).then((r) => r.data),

  // 同源 + cookie 鉴权下，wavesurfer/<audio> 可直接用该 URL 播放（后端支持 Range/seek，PH-1）。
  bodyUrl: (assetId: number) => `/api/assets/${assetId}/body`,
}

// 把后端 int16 交错 min/max 峰值归一化为 wavesurfer 单通道 peaks（[-1,1]）。
export function normalizePeaks(w: WaveformData): number[] {
  const scale = w.bits === 8 ? 128 : 32768
  return w.data.map((v) => Math.max(-1, Math.min(1, v / scale)))
}
