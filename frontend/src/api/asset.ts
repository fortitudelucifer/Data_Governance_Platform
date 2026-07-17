import { client } from './client'

export interface AnnotationTaskMeta {
  id: number
  asset_id: number
  dataset_id: number
  route_strategy: string
  state: string
  trace_id: string
  version: number
  assignee_id?: number | null
  reviewer_id?: number | null
  deadline_at?: string | null
  created_at: string
  updated_at: string
}

export interface ImageAsset {
  id: number
  dataset_id: number
  modality: string
  storage_uri: string
  original_name: string
  mime: string
  width: number
  height: number
  size_bytes: number
  qc_status: string
  uploader_id: number
  created_at: string
  task?: AnnotationTaskMeta
  // A/V 字段（image 为空）
  duration_ms?: number | null
  fps?: number | null
  sample_rate?: number | null
  preprocess_status?: string
  preprocess_error?: string
  rotation?: number
}

export interface AssetPage {
  items: ImageAsset[]
  total: number
  page: number
  page_size: number
}

export interface UploadAssetResp {
  asset: ImageAsset
  deduplicated: boolean
  task?: AnnotationTaskMeta
}

export const assetApi = {
  list: (datasetId: number, page = 1, pageSize = 24, qcStatus?: string) =>
    client.get<AssetPage>(`/datasets/${datasetId}/assets`, {
      params: { page, page_size: pageSize, ...(qcStatus ? { qc_status: qcStatus } : {}) },
    }).then((r) => r.data),

  // allow_duplicate 已随后端 M6 唯一约束移除：同数据集同内容只可能有一行。
  upload: (datasetId: number, file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return client.post<UploadAssetResp>(`/datasets/${datasetId}/assets`, fd, {
      headers: { 'Content-Type': 'multipart/form-data' },
    }).then((r) => r.data)
  },

  detail: (id: number) => client.get<ImageAsset>(`/assets/${id}`).then((r) => r.data),

  // 硬删除样本：blob + 派生物 + 任务 + 标注/track + 资产行（需 admin/审核员）。
  remove: (id: number) => client.delete(`/assets/${id}`).then((r) => r.data),

  // 图片二进制需带 JWT header，故用 axios 取 blob 再转 object URL（img src 无法带 header）
  fetchBlobUrl: async (id: number): Promise<string> => {
    const res = await client.get(`/assets/${id}/body`, { responseType: 'blob' })
    return URL.createObjectURL(res.data)
  },

  fetchDerivativeBlobUrl: async (id: number, kind: string): Promise<string> => {
    const res = await client.get(`/assets/${id}/derivative/${kind}`, { responseType: 'blob' })
    return URL.createObjectURL(res.data)
  },
}
