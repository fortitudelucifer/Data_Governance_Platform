import { client } from './client'

// 数据资产列表级批量自动标注（item 4）。后端：/datasets/:id/assets/auto_annotate
export interface BatchJobStatus {
  job_id?: string
  dataset_id?: number
  capability?: string
  model?: string
  total?: number
  done?: number
  failed?: number
  status: string // idle | running | completed | cancelled
  started_at?: string
}

export const batchAnnotateApi = {
  start: (datasetId: number, body: { task_ids: number[]; capability: string; model?: string; concurrency?: number }) =>
    client.post<BatchJobStatus>(`/datasets/${datasetId}/assets/auto_annotate`, body).then((r) => r.data),
  status: (datasetId: number) =>
    client.get<BatchJobStatus>(`/datasets/${datasetId}/assets/auto_annotate/status`).then((r) => r.data),
  cancel: (datasetId: number) =>
    client.post<{ cancelled: boolean }>(`/datasets/${datasetId}/assets/auto_annotate/cancel`).then((r) => r.data),
}

// 模态 → 可批量调用的能力（与工作台一致）。下拉只展示有已注册模型的能力。
export const MODALITY_CAPS: Record<string, string[]> = {
  audio: ['asr.transcribe', 'audio.classifier'],
  image: ['ocr.structure', 'vlm.structured_extract', 'vlm.caption', 'seg.instance'],
  video: ['video.detect_track'],
}

export const CAP_LABELS: Record<string, string> = {
  'asr.transcribe': '语音转写+说话人',
  'audio.classifier': '音频分类',
  'ocr.structure': 'OCR 结构化',
  'vlm.structured_extract': 'VLM 抽取',
  'vlm.caption': 'VLM 描述',
  'seg.instance': '实例分割',
  'video.detect_track': '检测+追踪',
}
