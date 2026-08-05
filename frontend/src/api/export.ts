import { client } from './client'

// 图片数据集导出格式（对应后端 /datasets/:id/export.* 路由）。导出的是「最终标注」。
export type ImageExportFormat = 'coco' | 'yolo-seg' | 'jsonl' | 'jsonld'

export const IMAGE_EXPORT_FORMATS: { fmt: ImageExportFormat; label: string }[] = [
  { fmt: 'coco', label: 'COCO (.json)' },
  { fmt: 'yolo-seg', label: 'YOLO-seg (.zip)' },
  { fmt: 'jsonl', label: '最终标注 (.jsonl)' },
  { fmt: 'jsonld', label: 'JSON-LD (.jsonld)' },
]

const PATHS: Record<ImageExportFormat, { path: (id: number) => string; fallback: (id: number) => string }> = {
  coco: { path: (id) => `/datasets/${id}/export.coco.json`, fallback: (id) => `dataset-${id}.coco.json` },
  'yolo-seg': { path: (id) => `/datasets/${id}/export.yolo-seg.zip`, fallback: (id) => `dataset-${id}.yolo-seg.zip` },
  jsonl: { path: (id) => `/datasets/${id}/final-annotations.jsonl`, fallback: (id) => `dataset-${id}.final.jsonl` },
  jsonld: { path: (id) => `/datasets/${id}/export.jsonld`, fallback: (id) => `dataset-${id}.jsonld` },
}

// 带鉴权下载导出文件（接口需 Bearer + 返回附件，故用 blob 触发下载，而非 window.open）。
// 文件名取 Content-Disposition，缺失时回退到 fallback。
async function downloadExport(path: string, params: Record<string, string> | undefined, fallback: string): Promise<void> {
  const res = await client.get(path, { responseType: 'blob', params })
  const cd = (res.headers['content-disposition'] as string | undefined) ?? ''
  const m = cd.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i)
  const filename = m ? decodeURIComponent(m[1]) : fallback
  const url = URL.createObjectURL(res.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// C1.4 病理区域 GeoJSON 导出（QuPath 原生格式，per-task）。默认读 FINALIZED 快照
// （交付真源）；draft=true 读工作台当前区域（核对用，文件与响应头都标 draft）。
export async function exportSlideRegions(taskId: number, draft = false): Promise<void> {
  const params = draft ? { source: 'draft' } : undefined
  await downloadExport(`/tasks/${taskId}/export.geojson`, params, `task_${taskId}_regions${draft ? '_draft' : ''}.geojson`)
}

// taskIds 非空时只导出选中的任务（后端 ?task_ids=1,2,3）；为空则导出整个数据集（已定稿部分）。
export async function exportImageDataset(datasetId: number, fmt: ImageExportFormat, taskIds?: number[]): Promise<void> {
  const { path, fallback } = PATHS[fmt]
  const params = taskIds && taskIds.length ? { task_ids: taskIds.join(',') } : undefined
  await downloadExport(path(datasetId), params, fallback(datasetId))
}

// 音频数据集导出格式（对应后端 /datasets/:id/export.audio?format=...）。同样导出「最终标注」。
export type AudioExportFormat = 'webvtt' | 'srt' | 'rttm' | 'csv' | 'jsonl'

export const AUDIO_EXPORT_FORMATS: { fmt: AudioExportFormat; label: string }[] = [
  { fmt: 'webvtt', label: 'WebVTT 字幕 (.zip)' },
  { fmt: 'srt', label: 'SRT 字幕 (.zip)' },
  { fmt: 'rttm', label: '说话人 RTTM (.rttm)' },
  { fmt: 'csv', label: '分段表 (.csv)' },
  { fmt: 'jsonl', label: '最终标注 (.jsonl)' },
]

export async function exportAudioDataset(datasetId: number, fmt: AudioExportFormat, taskIds?: number[]): Promise<void> {
  const params: Record<string, string> = { format: fmt }
  if (taskIds && taskIds.length) params.task_ids = taskIds.join(',')
  const ext = fmt === 'webvtt' || fmt === 'srt' ? 'zip' : fmt
  await downloadExport(`/datasets/${datasetId}/export.audio`, params, `dataset-${datasetId}.audio.${ext}`)
}

// 视频轨迹导出（对应后端 /datasets/:id/export.video?format=...）。导出 FINALIZED 快照。
export type VideoExportFormat = 'cvat' | 'mot' | 'yolo' | 'coco' | 'datumaro' | 'jsonl'

// CVAT-XML / Datumaro 保留 track + 属性（无损）；MOT 保留 track id；COCO / YOLO
// 是逐帧检测格式，会丢掉 track 身份（COCO 另存在 attributes 里）。
export const VIDEO_EXPORT_FORMATS: { fmt: VideoExportFormat; label: string }[] = [
  { fmt: 'cvat', label: 'CVAT-XML 无损 (.zip)' },
  { fmt: 'datumaro', label: 'Datumaro 无损 (.json)' },
  { fmt: 'mot', label: 'MOT 追踪 (.zip)' },
  { fmt: 'coco', label: 'COCO 逐帧检测 (.json)' },
  { fmt: 'yolo', label: 'YOLO 逐帧检测 (.zip)' },
  { fmt: 'jsonl', label: '轨迹 (.jsonl)' },
]

const VIDEO_EXPORT_EXT: Record<VideoExportFormat, string> = {
  cvat: 'zip', mot: 'zip', yolo: 'zip',
  coco: 'json', datumaro: 'json', jsonl: 'jsonl',
}

export async function exportVideoDataset(datasetId: number, fmt: VideoExportFormat, taskIds?: number[]): Promise<void> {
  const params: Record<string, string> = { format: fmt }
  if (taskIds && taskIds.length) params.task_ids = taskIds.join(',')
  await downloadExport(`/datasets/${datasetId}/export.video`, params, `dataset-${datasetId}.video.${VIDEO_EXPORT_EXT[fmt]}`)
}

// 通用 / 文本数据集导出（后端 /datasets/:id/export?format=...）。
export type GenericExportFormat = 'json' | 'jsonl' | 'csv'

export const GENERIC_EXPORT_FORMATS: { fmt: GenericExportFormat; label: string }[] = [
  { fmt: 'json', label: '数据集 (.json)' },
  { fmt: 'jsonl', label: '数据集 (.jsonl)' },
  { fmt: 'csv', label: '数据集 (.csv)' },
]

// stage 非空时只导出该 annotation_stage 的文档（后端 ?stage=refined）；默认导出全部。
export async function exportGenericDataset(
  datasetId: number,
  fmt: GenericExportFormat,
  docKeys?: string[],
  stage?: string,
): Promise<void> {
  const params: Record<string, string> = { format: fmt }
  if (docKeys && docKeys.length) params.doc_keys = docKeys.join(',')
  if (stage) params.stage = stage
  await downloadExport(`/datasets/${datasetId}/export`, params, `dataset-${datasetId}.${fmt}`)
}

// ---- 体数据(CT/MRI)分割导出:NIfTI 标签图(C3.4) ----

export interface VolumeExportResult {
  /** 重叠提示(后端百分号编码在 X-Overlap-Note 头里);无重叠时为空。 */
  overlapNote: string
  /** 'snapshot' = QA 冻结的终稿;'draft' = 未过 QA 的当前草稿。 */
  source: string
  /** 标签值 → 段名(单张多标签图才有)。 */
  labelMap: Record<string, string>
}

/**
 * 导出体数据分割。
 *
 * `split=false` 出单张多标签 .nii.gz(直接拖进 3D Slicer 就能看);
 * `split=true` 出 zip(逐段二值 + labels.json,**重叠无损**)。
 *
 * 重叠("肿瘤在肝内"是医学常态)在单张图里必然丢一部分——后端会在
 * `X-Overlap-Note` 里说清丢了多少,调用方**必须把它显示出来**,
 * 否则就是静默丢人工数据(00 契约第一条)。
 */
export async function exportVolumeSegmentation(
  taskId: number,
  opts: { split?: boolean; draft?: boolean } = {},
): Promise<VolumeExportResult> {
  const params: Record<string, string> = {}
  if (opts.split) params.split = '1'
  if (opts.draft) params.source = 'draft'
  const suffix = opts.split ? 'segments.zip' : 'seg.nii.gz'
  let res
  try {
    res = await client.get(`/tasks/${taskId}/export.nifti`, { responseType: 'blob', params })
  } catch (e: unknown) {
    // ⚠️ `responseType: 'blob'` 会让**错误响应体也变成 Blob**，于是常规的
    // `err.response.data.message` 取不到东西，界面只剩 "Request failed with
    // status code 409"。后端那句写得很仔细的中文（护栏该怎么办、草稿怎么导）
    // 恰恰在最需要它的时候丢了。所以这里把 Blob 读回文本再解析。
    const resp = (e as { response?: { data?: unknown; status?: number } }).response
    if (resp?.data instanceof Blob) {
      try {
        const text = await resp.data.text()
        const j = JSON.parse(text) as { message?: string; error?: string }
        resp.data = { message: j.message ?? j.error ?? text }
      } catch {
        /* 不是 JSON 就保持原样，至少不比现在更糟 */
      }
    }
    throw e
  }

  const cd = (res.headers['content-disposition'] as string | undefined) ?? ''
  const m = cd.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i)
  const filename = m ? decodeURIComponent(m[1]) : `task-${taskId}-${suffix}`
  const url = URL.createObjectURL(res.data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)

  // 头是百分号编码的 UTF-8(HTTP 头只能放 latin-1,而文案是中文)。
  const dec = (v: unknown) => {
    if (typeof v !== 'string' || !v) return ''
    try {
      return decodeURIComponent(v)
    } catch {
      return ''
    }
  }
  let labelMap: Record<string, string> = {}
  const lm = dec(res.headers['x-label-map'])
  if (lm) {
    try {
      labelMap = JSON.parse(lm) as Record<string, string>
    } catch {
      labelMap = {}
    }
  }
  return {
    overlapNote: dec(res.headers['x-overlap-note']),
    source: (res.headers['x-export-source'] as string) ?? '',
    labelMap,
  }
}
