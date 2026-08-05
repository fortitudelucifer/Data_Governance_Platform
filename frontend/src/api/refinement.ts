import { client } from './client'
import type { QAPair } from './document'

export interface RefinementState {
  doc_key: string
  stage: string
  cursor: number
  qa_pairs: QAPair[]
  etag: string
}

const dsParams = (datasetId?: number) =>
  datasetId ? { params: { dataset_id: datasetId } } : undefined

export const refinementApi = {
  start: (docKey: string, datasetId?: number) =>
    client.post<RefinementState>(`/documents/${docKey}/start_refinement`, {}, dsParams(datasetId)).then((r) => r.data),

  navigateCursor: (docKey: string, action: string, etag: string, index?: number, datasetId?: number) =>
    client.put<RefinementState>(`/documents/${docKey}/refinement_cursor`, { action, index, etag }, dsParams(datasetId)).then((r) => r.data),

  editQAPair: (docKey: string, index: number, pair: Partial<QAPair>, etag: string, datasetId?: number) =>
    client.put<RefinementState>(`/documents/${docKey}/qa_pairs/${index}`, { ...pair, etag }, dsParams(datasetId)).then((r) => r.data),

  deleteQAPair: (docKey: string, index: number, etag: string, datasetId?: number) =>
    client.delete<RefinementState>(`/documents/${docKey}/qa_pairs/${index}`, { ...dsParams(datasetId), data: { etag } }).then((r) => r.data),

  addQAPair: (docKey: string, payload: Partial<QAPair>, etag: string, datasetId?: number) =>
    client.post<RefinementState>(`/documents/${docKey}/qa_pairs`, { ...payload, etag }, dsParams(datasetId)).then((r) => r.data),

  bulkUpdate: (docKey: string, qaPairs: QAPair[], etag: string, datasetId?: number) =>
    client.put<RefinementState>(`/documents/${docKey}/qa_pairs_bulk`, { qa_pairs: qaPairs, etag }, dsParams(datasetId)).then((r) => r.data),

  complete: (docKey: string, etag?: string, datasetId?: number) =>
    client.post<RefinementState>(`/documents/${docKey}/complete_refinement`, etag ? { etag } : {}, dsParams(datasetId)).then((r) => r.data),

  llmRefine: (docKey: string, params: { enabled: boolean; model?: string; provider_id?: number }, datasetId?: number) =>
    client.post(`/documents/${docKey}/llm-refine`, params, dsParams(datasetId)).then((r) => r.data),

  rollbackLlmRefine: (docKey: string, datasetId?: number) =>
    client.delete(`/documents/${docKey}/llm-refine`, dsParams(datasetId)).then((r) => r.data),
}

export interface AutoAnnotateStatus {
  completed: number
  failed: number
  total: number
  results: { doc_key: string; stage: string; error?: string }[]
}

export const autoAnnotateApi = {
  trigger: (datasetId: number, docKeys: string[], providerId: number) =>
    client.post<AutoAnnotateStatus>(`/datasets/${datasetId}/auto_annotate`, { doc_keys: docKeys, provider_id: providerId }).then((r) => r.data),
  cancel: (datasetId: number, docKeys: string[]) =>
    client.post(`/datasets/${datasetId}/auto_annotate/cancel`, { doc_keys: docKeys }).then((r) => r.data),
  status: (datasetId: number) =>
    client.get(`/datasets/${datasetId}/auto_annotate/status`).then((r) => r.data),
}
