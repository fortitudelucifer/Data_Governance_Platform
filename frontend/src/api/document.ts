import { client } from './client'
import type { TaskAssignPayload } from './imageTask'

export interface QAPair {
  question: string
  answer: string
  question_key?: string
  category?: string
  evidence?: string
  confidence?: number
  reason?: string
  source: string
  confirmed: boolean
  span_text?: string
  span_start?: number
  span_end?: number
  text_field?: string
  provider_id?: number
  provider_name?: string
  model?: string
  prompt_template_id?: number
  prompt_template_name?: string
  prompt_version?: number
  candidate_run_id?: string
  judge_run_id?: string
  source_candidate_run_ids?: string[]
  edited_after_adopt?: boolean
  meta?: Record<string, any>
}

export interface Document {
  doc_key: string
  version: number
  data: Record<string, any>
  assignee_id?: number | null
  reviewer_id?: number | null
  deadline_at?: string | null
  created_at?: string
  updated_at?: string
  annotator_name?: string
  annotation_stage?: string
  llm_refinement_enabled?: boolean
  llm_refinement_score?: number | null
  llm_refinement_reasoning?: string
  llm_refinement_version?: string
}

export interface PaginatedDocuments {
  items: Document[]
  total: number
  page: number
  page_size: number
}

export interface ImportReport {
  imported_count: number
  skipped_count: number
  failed_count: number
  skipped_keys?: string[]
}

export interface ImportFormat { format_id: string; extensions: string[] }

export interface DocumentStageResult {
  doc_key: string
  version: number
  stage: string
}

const dsParams = (datasetId?: number) =>
  datasetId ? { params: { dataset_id: datasetId } } : undefined

export const documentApi = {
  list: (datasetId: number, page = 1, pageSize = 20, query?: string) =>
    client.get<PaginatedDocuments>(`/datasets/${datasetId}/documents`, {
      params: { page, page_size: pageSize, ...(query ? { q: query } : {}) },
    }).then((r) => r.data),

  get: (key: string, datasetId?: number) =>
    client.get<Document>(`/documents/${key}`, dsParams(datasetId)).then((r) => r.data),

  import: (datasetId: number, file: File, mode = 'full') => {
    const fd = new FormData()
    fd.append('file', file)
    return client.post<ImportReport>(
      `/datasets/${datasetId}/documents/import?mode=${encodeURIComponent(mode)}`,
      fd,
      { headers: { 'Content-Type': 'multipart/form-data' } },
    ).then((r) => r.data)
  },

  delete: (datasetId: number, key: string) =>
    client.delete(`/datasets/${datasetId}/documents/${key}`),

  batchDelete: (datasetId: number, docKeys: string[]) =>
    client.post<{ deleted_count: number }>(`/datasets/${datasetId}/documents/batch_delete`, { doc_keys: docKeys }).then((r) => r.data),

  directComplete: (key: string, datasetId?: number) =>
    client.post<DocumentStageResult>(`/documents/${key}/direct_complete`, {}, dsParams(datasetId)).then((r) => r.data),

  reAnnotate: (key: string, datasetId?: number) =>
    client.post<DocumentStageResult>(`/documents/${key}/reannotate`, {}, dsParams(datasetId)).then((r) => r.data),

  // 批量设置/清除文档截止时间（deadline 为空串=清除）。截止值存于 doc.data.deadline。
  setDeadline: (datasetId: number, docKeys: string[], deadline: string) =>
    client.put<{ updated: number }>(`/datasets/${datasetId}/documents/deadline`, { doc_keys: docKeys, deadline }).then((r) => r.data),

  assign: (datasetId: number, docKeys: string[], payload: TaskAssignPayload) =>
    client.put<{ updated: number }>(`/datasets/${datasetId}/documents/assign`, { doc_keys: docKeys, ...payload }).then((r) => r.data),

  formats: () => client.get<ImportFormat[]>('/import/formats').then((r) => r.data),
}
